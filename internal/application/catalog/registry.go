package catalog

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/rushteam/getdocs/internal/domain/docs"
)

// LoadRegistry 加载登记的文档树并合并到配置的文档树中，应在 Restore 之前调用；可重复调用以加载新登记项。
// 与配置文件重名的登记项会被忽略：配置文件优先。
func (s *Service) LoadRegistry(ctx context.Context) error {
	if s.deps.Registry == nil {
		return nil
	}
	trees, err := s.deps.Registry.Load(ctx)
	if err != nil {
		return fmt.Errorf("load registry: %w", err)
	}
	s.specMu.Lock()
	defer s.specMu.Unlock()
	for _, t := range trees {
		if _, loaded := s.registered[t.Key()]; loaded {
			continue // 重复加载（如只读进程定期刷新）时只追加新登记的文档树
		}
		if _, exists := s.specs[t.Key()]; exists {
			s.deps.Logger.WarnContext(ctx, "registered tree conflicts with config, ignored", "tree", t.Key())
			continue
		}
		spec, err := s.specFor(t)
		if err != nil {
			s.deps.Logger.WarnContext(ctx, "invalid registered tree, ignored", "tree", t.Key(), "err", err)
			continue
		}
		s.addSpecLocked(spec)
		s.registered[t.Key()] = t
	}
	return nil
}

// Registered 判断文档树是否为登记的（可通过页面删除），配置文件中的文档树返回 false。
func (s *Service) Registered(key docs.TreeKey) bool {
	s.specMu.RLock()
	defer s.specMu.RUnlock()
	_, ok := s.registered[key]
	return ok
}

// CanRegister 判断是否支持登记文档树。
func (s *Service) CanRegister() bool { return s.deps.Registry != nil }

// Credentials 返回可引用的凭据名称。
func (s *Service) Credentials() []string {
	var names []string
	for n := range s.opts.Credentials {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}

// AddTree 登记一棵 git 文档树并持久化；成功后调度器开始定时同步，调用方可随即触发首次同步。
func (s *Service) AddTree(ctx context.Context, t docs.RegisteredTree) (*docs.TreeSpec, error) {
	if s.deps.Registry == nil {
		return nil, fmt.Errorf("tree registry is not configured: %w", docs.ErrInvalid)
	}
	t.Namespace, t.Name, t.Repo = strings.TrimSpace(t.Namespace), strings.TrimSpace(t.Name), strings.TrimSpace(t.Repo)
	t.Branch, t.Root = strings.TrimSpace(t.Branch), strings.Trim(strings.TrimSpace(t.Root), "/")
	t.Provider = strings.ToLower(strings.TrimSpace(t.Provider))
	t.CreatedAt = time.Now().UTC()
	spec, err := s.specFor(t)
	if err != nil {
		return nil, err
	}

	s.specMu.Lock()
	if _, exists := s.specs[t.Key()]; exists {
		s.specMu.Unlock()
		return nil, fmt.Errorf("tree %s: %w", t.Key(), docs.ErrConflict)
	}
	if err := s.saveRegistryLocked(ctx, t, true); err != nil {
		s.specMu.Unlock()
		return nil, err
	}
	s.addSpecLocked(spec)
	s.registered[t.Key()] = t
	onAdd := s.onAdd
	s.specMu.Unlock()

	s.deps.Logger.InfoContext(ctx, "tree registered", "tree", t.Key(), "repo", spec.Source.Display())
	if onAdd != nil {
		onAdd(spec)
	}
	return spec, nil
}

// RemoveTree 删除登记的文档树，通知下游清理（如搜索索引）。配置文件中的文档树不能删除。
// 已保存的快照与同步记录保留在存储中，重新登记同名文档树时可直接复用。
func (s *Service) RemoveTree(ctx context.Context, key docs.TreeKey) error {
	s.specMu.Lock()
	if _, ok := s.registered[key]; !ok {
		_, exists := s.specs[key]
		s.specMu.Unlock()
		if exists {
			return fmt.Errorf("tree %s is defined in the config file and cannot be removed here: %w", key, docs.ErrInvalid)
		}
		return fmt.Errorf("tree %s: %w", key, docs.ErrNotFound)
	}
	if err := s.saveRegistryLocked(ctx, docs.RegisteredTree{Namespace: key.Namespace, Name: key.Tree}, false); err != nil {
		s.specMu.Unlock()
		return err
	}
	delete(s.registered, key)
	delete(s.specs, key)
	for i, ns := range s.namespaces {
		if ns.Name != key.Namespace {
			continue
		}
		ns.Trees = slices.DeleteFunc(slices.Clone(ns.Trees), func(t *docs.TreeSpec) bool { return t.Key == key })
		if len(ns.Trees) == 0 && ns.Registered {
			s.namespaces = slices.Delete(s.namespaces, i, i+1)
		}
		break
	}
	s.specMu.Unlock()

	s.mu.Lock()
	prev := s.current[key]
	delete(s.current, key)
	delete(s.pending, key)
	s.mu.Unlock()
	s.deps.Logger.InfoContext(ctx, "tree removed", "tree", key)
	s.runHooks(ctx, docs.SyncEvent{Key: key, Previous: prev, Removed: true})
	return nil
}

// saveRegistryLocked 把加入（add 为 true）或移除 t 之后的登记列表写入存储。调用方持有 specMu。
func (s *Service) saveRegistryLocked(ctx context.Context, t docs.RegisteredTree, add bool) error {
	list := make([]docs.RegisteredTree, 0, len(s.registered)+1)
	for _, r := range s.registered {
		if r.Key() != t.Key() {
			list = append(list, r)
		}
	}
	if add {
		list = append(list, t)
	}
	slices.SortFunc(list, func(a, b docs.RegisteredTree) int { return cmp.Compare(a.Key().String(), b.Key().String()) })
	if err := s.deps.Registry.Save(ctx, list); err != nil {
		return fmt.Errorf("save registry: %w", err)
	}
	return nil
}

// addSpecLocked 把文档树加入所属命名空间，命名空间不存在时创建。调用方持有 specMu。
func (s *Service) addSpecLocked(spec *docs.TreeSpec) {
	s.specs[spec.Key] = spec
	for _, ns := range s.namespaces {
		if ns.Name == spec.Key.Namespace {
			ns.Trees = append(slices.Clone(ns.Trees), spec)
			return
		}
	}
	s.namespaces = append(s.namespaces, &docs.Namespace{
		Name: spec.Key.Namespace, Title: spec.Key.Namespace, Trees: []*docs.TreeSpec{spec}, Registered: true,
	})
}

// specFor 校验登记项并转换为文档树配置。仓库地址只接受 https 与 ssh，避免访问服务器本地文件。
func (s *Service) specFor(t docs.RegisteredTree) (*docs.TreeSpec, error) {
	bad := func(format string, args ...any) error {
		return fmt.Errorf("%s: %w", fmt.Sprintf(format, args...), docs.ErrInvalid)
	}
	switch {
	case !docs.ValidName(t.Namespace):
		return nil, bad("namespace %q must match %s", t.Namespace, docs.NamePattern)
	case !docs.ValidName(t.Name):
		return nil, bad("tree name %q must match %s", t.Name, docs.NamePattern)
	case !docs.IsRemoteRepo(t.Repo):
		return nil, bad("repo %q must be an https or ssh git url", t.Repo)
	case strings.HasPrefix(t.Branch, "-") || strings.ContainsAny(t.Branch, " \t\n~^:?*[\\"):
		return nil, bad("invalid branch %q", t.Branch)
	case strings.Contains(t.Root, ".."):
		return nil, bad("root must not contain ..")
	}
	p := docs.Provider(t.Provider)
	if p == "" {
		p = docs.DetectProvider(t.Repo)
	} else if !slices.Contains(docs.Providers, p) {
		return nil, bad("unknown provider %q", t.Provider)
	}
	filter := docs.Filter{Include: t.Include, Exclude: t.Exclude}
	if err := filter.Validate(); err != nil {
		return nil, bad("%v", err)
	}
	var auth docs.Auth
	if t.Credential != "" {
		a, ok := s.opts.Credentials[t.Credential]
		if !ok {
			return nil, bad("unknown credential %q", t.Credential)
		}
		auth = a
		if auth.Username == "" && auth.Password != "" {
			if auth.Username = docs.DefaultTokenUser(p); auth.Username == "" {
				return nil, bad("credential %q needs a username for %s", t.Credential, p.Title())
			}
		}
	}
	interval := cmp.Or(s.opts.DefaultInterval, 15*time.Minute)
	return &docs.TreeSpec{
		Key:         t.Key(),
		Title:       cmp.Or(t.Title, t.Name),
		Description: t.Description,
		Source: docs.Source{Kind: docs.SourceGit, URL: t.Repo, Branch: t.Branch, Provider: p, Auth: auth,
			BrowseURL: docs.InferBrowseURL(t.Repo, p)},
		Root:     t.Root,
		Filter:   filter,
		Interval: interval,
		AutoSync: true,
	}, nil
}

// subscribe 注册新增文档树的回调，供调度器为其启动定时同步。
func (s *Service) subscribe(fn func(*docs.TreeSpec)) {
	s.specMu.Lock()
	defer s.specMu.Unlock()
	s.onAdd = fn
}

// active 判断 spec 仍是该文档树的当前配置（未被删除或替换）。
func (s *Service) active(spec *docs.TreeSpec) bool {
	cur, err := s.Spec(spec.Key)
	return err == nil && cur == spec
}
