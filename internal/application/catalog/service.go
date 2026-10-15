// Package catalog 编排文档同步与查询：拉取仓库 → 构建快照 → 持久化 → 发布到内存 → 通知下游扩展。
package catalog

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/rushteam/getdocs/internal/domain/docs"
)

// Options 是同步行为配置。
type Options struct {
	KeepVersions int           // 每棵文档树保留的快照版本数
	SyncTimeout  time.Duration // 单次同步（不含扩展钩子）的超时
	HookTimeout  time.Duration // 单个扩展钩子的超时
	Concurrency  int           // SyncAll 的并发度
	// DefaultInterval 是登记的文档树的定时同步间隔。
	DefaultInterval time.Duration
	// Credentials 是可供登记的文档树按名称引用的仓库凭据。
	Credentials map[string]docs.Auth
}

// Deps 是用例依赖的端口集合。
type Deps struct {
	Fetcher   docs.Fetcher
	Parser    docs.Parser
	Snapshots docs.SnapshotStore
	States    docs.StateStore
	Registry  docs.TreeRegistry // 为 nil 时不支持登记文档树
	Logger    *slog.Logger
}

// Service 管理全部文档树的同步与读取。
type Service struct {
	deps    Deps
	opts    Options
	builder *Builder

	specMu     sync.RWMutex
	namespaces []*docs.Namespace
	specs      map[docs.TreeKey]*docs.TreeSpec
	registered map[docs.TreeKey]docs.RegisteredTree // 通过页面或 API 登记的文档树
	onAdd      func(*docs.TreeSpec)                 // 调度器订阅新增的文档树

	mu      sync.RWMutex
	current map[docs.TreeKey]*docs.Snapshot
	running map[docs.TreeKey]*docs.SyncRun
	pending map[docs.TreeKey]pendingSync // 同步进行中收到的异步请求，结束后合并执行一次

	hookMu sync.RWMutex
	hooks  []docs.SyncHook

	inflight sync.WaitGroup // 进行中的同步，停机时等待其写完运行记录
}

type pendingSync struct {
	ctx     context.Context
	trigger docs.Trigger
	force   bool
}

// New 创建服务。namespaces 来自配置；登记的文档树由 LoadRegistry 加载，可在运行期间增删。
func New(deps Deps, opts Options, namespaces []*docs.Namespace) *Service {
	if opts.KeepVersions <= 0 {
		opts.KeepVersions = 5
	}
	if opts.SyncTimeout <= 0 {
		opts.SyncTimeout = 10 * time.Minute
	}
	if opts.HookTimeout <= 0 {
		opts.HookTimeout = 10 * time.Minute
	}
	if opts.Concurrency <= 0 {
		opts.Concurrency = 2
	}
	s := &Service{
		deps:       deps,
		opts:       opts,
		builder:    &Builder{Parser: deps.Parser, Logger: deps.Logger},
		namespaces: namespaces,
		specs:      map[docs.TreeKey]*docs.TreeSpec{},
		registered: map[docs.TreeKey]docs.RegisteredTree{},
		current:    map[docs.TreeKey]*docs.Snapshot{},
		running:    map[docs.TreeKey]*docs.SyncRun{},
		pending:    map[docs.TreeKey]pendingSync{},
	}
	for _, ns := range namespaces {
		for _, t := range ns.Trees {
			s.specs[t.Key] = t
		}
	}
	return s
}

// AddHook 注册同步扩展钩子（RAG 索引、外部通知等）。应在 Restore 之前调用。
func (s *Service) AddHook(h docs.SyncHook) {
	s.hookMu.Lock()
	defer s.hookMu.Unlock()
	s.hooks = append(s.hooks, h)
}

// Restore 从存储加载每棵文档树的当前快照，使服务重启后无需等待同步即可提供文档。
// 重复调用时只替换版本发生变化的文档树，可用于只读进程（如 stdio MCP）跟随其他进程的同步结果。
func (s *Service) Restore(ctx context.Context) {
	for _, spec := range s.Specs() {
		version, err := s.deps.Snapshots.CurrentVersion(ctx, spec.Key)
		if errors.Is(err, docs.ErrNotFound) {
			continue
		}
		if err == nil && version == s.currentVersion(spec.Key) {
			continue
		}
		var snap *docs.Snapshot
		if err == nil {
			snap, err = s.deps.Snapshots.Load(ctx, spec.Key, version)
		}
		if err != nil {
			s.deps.Logger.ErrorContext(ctx, "restore snapshot failed", "tree", spec.Key, "err", err)
			continue
		}
		s.mu.Lock()
		prev := s.current[spec.Key]
		if prev != nil && prev.Version == snap.Version {
			s.mu.Unlock()
			continue
		}
		s.current[spec.Key] = snap
		s.mu.Unlock()
		s.deps.Logger.InfoContext(ctx, "snapshot restored", "tree", spec.Key, "version", snap.Version, "docs", len(snap.Docs))
		s.runHooks(ctx, docs.SyncEvent{Key: spec.Key, Previous: prev, Current: snap, Changes: docs.Diff(prev, snap), Restored: true})
	}
}

func (s *Service) currentVersion(key docs.TreeKey) string {
	if snap := s.snapshot(key); snap != nil {
		return snap.Version
	}
	return ""
}

// Sync 同步一棵文档树。force 为 true 时即使提交未变也重新构建。
// 同一文档树已在同步时返回 docs.ErrSyncInProgress。
func (s *Service) Sync(ctx context.Context, key docs.TreeKey, trigger docs.Trigger, force bool) (docs.SyncRun, error) {
	spec, err := s.Spec(key)
	if err != nil {
		return docs.SyncRun{}, err
	}
	run := docs.SyncRun{ID: newID(), Key: key, Trigger: trigger, Force: force, Status: docs.RunRunning, StartedAt: time.Now().UTC()}
	s.mu.Lock()
	if s.running[key] != nil {
		s.mu.Unlock()
		return docs.SyncRun{}, docs.ErrSyncInProgress
	}
	cp := run
	s.running[key] = &cp
	s.inflight.Add(1)
	s.mu.Unlock()
	defer s.inflight.Done()
	defer s.finish(key)

	log := s.deps.Logger.With("tree", key, "run", run.ID, "trigger", trigger)
	log.InfoContext(ctx, "sync started", "force", force)

	ev, err := s.syncTree(ctx, spec, &run)
	run.FinishedAt = time.Now().UTC()
	if err != nil {
		run.Status, run.Error = docs.RunFailed, err.Error()
		log.ErrorContext(ctx, "sync failed", "err", err, "duration", run.Duration())
	} else {
		log.InfoContext(ctx, "sync finished", "status", run.Status, "commit", run.Revision.ShortCommit(),
			"docs", run.Docs, "changes", run.Changes.String(), "duration", run.Duration())
	}

	if ev != nil {
		ev.Run = run
		run.HookErrors = s.runHooks(ctx, *ev)
	}
	s.recordRun(ctx, spec, run)
	return run, err
}

// syncTree 执行拉取与构建；产生新快照时返回待发布给钩子的事件。
func (s *Service) syncTree(ctx context.Context, spec *docs.TreeSpec, run *docs.SyncRun) (*docs.SyncEvent, error) {
	ctx, cancel := context.WithTimeout(ctx, s.opts.SyncTimeout)
	defer cancel()

	co, err := s.deps.Fetcher.Fetch(ctx, spec.Source)
	if err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}
	defer co.Close()
	run.Revision = co.Revision()

	prev := s.snapshot(spec.Key)
	state, err := s.deps.States.State(ctx, spec.Key)
	if err != nil {
		return nil, fmt.Errorf("load state: %w", err)
	}
	fingerprint := spec.Fingerprint()
	// 提交未变时跳过构建。本地目录可能有未提交的修改，始终重新扫描。
	if !run.Force && prev != nil && spec.Source.Kind == docs.SourceGit && run.Revision.Commit != "" &&
		state.LastRevision.Commit == run.Revision.Commit && state.LastFingerprint == fingerprint {
		run.Status, run.Version, run.Docs = docs.RunUnchanged, prev.Version, len(prev.Docs)
		return nil, nil
	}

	snap, blobs, err := s.builder.Build(ctx, spec, co)
	if err != nil {
		return nil, fmt.Errorf("build: %w", err)
	}
	run.Changes = docs.Diff(prev, snap)
	run.Docs = len(snap.Docs)

	// 只改了代码、没改文档的提交不产生新版本，保持下游（如 RAG 索引）稳定。
	if !run.Force && prev != nil && run.Changes.Empty() && prev.Fingerprint == fingerprint {
		run.Status, run.Version = docs.RunUnchanged, prev.Version
		return nil, nil
	}

	if err := s.deps.Snapshots.Save(ctx, snap, blobs); err != nil {
		return nil, fmt.Errorf("save snapshot: %w", err)
	}
	if err := s.deps.Snapshots.Prune(ctx, spec.Key, s.opts.KeepVersions); err != nil {
		s.deps.Logger.WarnContext(ctx, "prune snapshots failed", "tree", spec.Key, "err", err)
	}
	if _, err := s.Spec(spec.Key); err != nil {
		return nil, fmt.Errorf("tree removed during sync: %w", err)
	}
	s.mu.Lock()
	s.current[spec.Key] = snap
	s.mu.Unlock()

	run.Status, run.Version = docs.RunSuccess, snap.Version
	return &docs.SyncEvent{Key: spec.Key, Previous: prev, Current: snap, Changes: run.Changes}, nil
}

func (s *Service) recordRun(ctx context.Context, spec *docs.TreeSpec, run docs.SyncRun) {
	// 同步本身可能因 ctx 取消而结束，记录结果不应再被取消打断。
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	st, err := s.deps.States.State(ctx, spec.Key)
	if err != nil {
		s.deps.Logger.ErrorContext(ctx, "load state failed", "tree", spec.Key, "err", err)
		st = docs.TreeState{Key: spec.Key}
	}
	st.Key = spec.Key
	st.LastSyncAt = run.FinishedAt
	st.LastRun = &run
	if run.Status != docs.RunFailed {
		st.LastRevision = run.Revision
		st.LastFingerprint = spec.Fingerprint()
		st.LastSuccessAt = run.FinishedAt
	}
	if err := s.deps.States.SaveState(ctx, st); err != nil {
		s.deps.Logger.ErrorContext(ctx, "save state failed", "tree", spec.Key, "err", err)
	}
	if err := s.deps.States.AppendRun(ctx, run); err != nil {
		s.deps.Logger.ErrorContext(ctx, "append run failed", "tree", spec.Key, "err", err)
	}
}

// runHooks 依次调用扩展钩子，单个钩子失败不影响其他钩子与同步结果。
func (s *Service) runHooks(ctx context.Context, ev docs.SyncEvent) []string {
	s.hookMu.RLock()
	hooks := append([]docs.SyncHook(nil), s.hooks...)
	s.hookMu.RUnlock()
	var errs []string
	for _, h := range hooks {
		hctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.opts.HookTimeout)
		err := safeHook(hctx, h, ev)
		cancel()
		if err != nil {
			s.deps.Logger.ErrorContext(ctx, "sync hook failed", "hook", h.Name(), "tree", ev.Key, "err", err)
			errs = append(errs, h.Name()+": "+err.Error())
		}
	}
	return errs
}

func safeHook(ctx context.Context, h docs.SyncHook, ev docs.SyncEvent) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return h.OnSync(ctx, ev)
}

// SyncResult 是批量同步中单棵文档树的结果。
type SyncResult struct {
	Key docs.TreeKey `json:"key"`
	Run docs.SyncRun `json:"run"`
	Err string       `json:"error,omitempty"`
}

// SyncAll 以有限并发同步给定文档树（为空表示全部）。
func (s *Service) SyncAll(ctx context.Context, keys []docs.TreeKey, trigger docs.Trigger, force bool) []SyncResult {
	if len(keys) == 0 {
		for _, spec := range s.Specs() {
			keys = append(keys, spec.Key)
		}
	}
	results := make([]SyncResult, len(keys))
	sem := make(chan struct{}, s.opts.Concurrency)
	var wg sync.WaitGroup
	for i, k := range keys {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			run, err := s.Sync(ctx, k, trigger, force)
			results[i] = SyncResult{Key: k, Run: run}
			if err != nil {
				results[i].Err = err.Error()
			}
		})
	}
	wg.Wait()
	return results
}

// SyncAsync 在后台同步并立即返回。文档树正在同步时请求会被合并，当前同步结束后再执行一次，
// 保证同步期间到达的推送事件不会丢失；此时 queued 为 true。
// ctx 应为服务生命周期 ctx，而不是请求 ctx，以免请求结束后同步被取消。
func (s *Service) SyncAsync(ctx context.Context, key docs.TreeKey, trigger docs.Trigger, force bool) (queued bool, err error) {
	if _, err := s.Spec(key); err != nil {
		return false, err
	}
	s.mu.Lock()
	if s.running[key] != nil {
		p := s.pending[key]
		s.pending[key] = pendingSync{ctx: ctx, trigger: trigger, force: force || p.force}
		s.mu.Unlock()
		return true, nil
	}
	s.mu.Unlock()
	go s.runAsync(pendingSync{ctx: ctx, trigger: trigger, force: force}, key)
	return false, nil
}

func (s *Service) runAsync(p pendingSync, key docs.TreeKey) {
	if _, err := s.Sync(p.ctx, key, p.trigger, p.force); errors.Is(err, docs.ErrSyncInProgress) {
		_, _ = s.SyncAsync(p.ctx, key, p.trigger, p.force) // 被其他同步抢先，排队等待其结束
	}
}

// finish 清除运行状态，并执行同步期间排队的请求。
func (s *Service) finish(key docs.TreeKey) {
	s.mu.Lock()
	delete(s.running, key)
	p, ok := s.pending[key]
	delete(s.pending, key)
	s.mu.Unlock()
	if _, err := s.Spec(key); ok && err == nil && p.ctx.Err() == nil {
		go s.runAsync(p, key)
	}
}

// Wait 等待进行中的同步结束（它们应已因生命周期 ctx 取消而尽快退出），ctx 到期时放弃等待。
func (s *Service) Wait(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		s.inflight.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func newID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return time.Now().UTC().Format("20060102150405") + "-" + hex.EncodeToString(b)
}
