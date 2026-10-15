// Package docs 定义文档服务的核心领域模型：命名空间 → 文档树 → 文档。
//
// 本包只依赖标准库与纯算法库，不感知 git、HTTP、存储等外部细节。
package docs

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"
)

// TreeKey 唯一标识一棵文档树。
type TreeKey struct {
	Namespace string `json:"namespace"`
	Tree      string `json:"tree"`
}

func (k TreeKey) String() string { return k.Namespace + "/" + k.Tree }

// ParseTreeKey 解析 "namespace/tree" 形式的标识。
func ParseTreeKey(s string) (TreeKey, error) {
	ns, tree, ok := strings.Cut(strings.Trim(s, "/"), "/")
	if !ok || ns == "" || tree == "" || strings.Contains(tree, "/") {
		return TreeKey{}, fmt.Errorf("invalid tree key %q, want namespace/tree", s)
	}
	return TreeKey{Namespace: ns, Tree: tree}, nil
}

// Namespace 是文档树的分组，通常对应一个团队或业务域。
type Namespace struct {
	Name        string
	Title       string
	Description string
	Trees       []*TreeSpec
	Registered  bool // 因登记文档树而自动创建，不在配置文件中
}

// TreeSpec 描述一棵文档树从哪里来、如何筛选。
type TreeSpec struct {
	Key         TreeKey
	Title       string
	Description string
	Source      Source
	// Root 是文档根（相对仓库根），可以是目录或单个 markdown 文件；为空表示自动探测。
	Root     string
	Filter   Filter
	Interval time.Duration
	AutoSync bool
}

// Fingerprint 标识影响快照内容的配置；配置变化时即使提交未变也需要重建。
func (s *TreeSpec) Fingerprint() string {
	b, _ := json.Marshal(struct {
		Kind, URL, Branch, Root string
		Filter                  Filter
	}{string(s.Source.Kind), s.Source.URL, s.Source.Branch, s.Root, s.Filter})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

// SourceKind 是文档来源类型。
type SourceKind string

const (
	SourceGit   SourceKind = "git"
	SourceLocal SourceKind = "local"
)

// Provider 是代码托管平台。不同平台的令牌认证用户名、文件浏览地址规则不同；为空表示通用 git 服务。
type Provider string

const (
	ProviderGitHub    Provider = "github"
	ProviderGitLab    Provider = "gitlab"
	ProviderGitee     Provider = "gitee"
	ProviderGitea     Provider = "gitea"
	ProviderBitbucket Provider = "bitbucket"
)

// Providers 是支持的托管平台。
var Providers = []Provider{ProviderGitHub, ProviderGitLab, ProviderGitee, ProviderGitea, ProviderBitbucket}

// Title 返回平台的展示名称。
func (p Provider) Title() string {
	switch p {
	case ProviderGitHub:
		return "GitHub"
	case ProviderGitLab:
		return "GitLab"
	case ProviderGitee:
		return "Gitee"
	case ProviderGitea:
		return "Gitea"
	case ProviderBitbucket:
		return "Bitbucket"
	}
	return string(p)
}

// Source 描述文档仓库。
type Source struct {
	Kind     SourceKind
	URL      string   // git 地址，或 local 类型的本地目录
	Branch   string   // 为空表示远端默认分支
	Provider Provider // 托管平台，仅用于 git 来源
	Auth     Auth
	// BrowseURL 是浏览仓库文件的地址模板，支持 {branch}、{commit}、{path} 占位符，
	// 如 https://github.com/org/repo/blob/{branch}/{path}；为空则不生成“查看源文件”链接。
	BrowseURL string
}

// Auth 是访问仓库的凭据。
type Auth struct {
	Username string
	Password string // 密码或访问令牌
	SSHKey   string // 私钥文件路径
}

// Display 返回可公开展示的来源：去掉 URL 中内嵌的凭据，本地目录不暴露服务器路径。
func (s Source) Display() string {
	if s.Kind == SourceLocal {
		return "local"
	}
	if u, err := url.Parse(s.URL); err == nil && u.Scheme != "" && u.User != nil {
		u.User = nil
		return u.String()
	}
	return s.URL
}

// FileURL 返回仓库中某个文件在指定版本下的浏览地址，无法生成时返回空。
func (s Source) FileURL(rev Revision, repoPath string) string {
	if s.BrowseURL == "" {
		return ""
	}
	branch := rev.Branch
	if branch == "" {
		branch = s.Branch
	}
	if branch == "" {
		branch = rev.Commit
	}
	return strings.NewReplacer(
		"{branch}", branch,
		"{commit}", rev.Commit,
		"{path}", strings.TrimPrefix(repoPath, "/"),
	).Replace(s.BrowseURL)
}

// Revision 是一次同步拉取到的版本信息。
type Revision struct {
	Commit  string    `json:"commit,omitempty"`
	Branch  string    `json:"branch,omitempty"`
	Author  string    `json:"author,omitempty"`
	Message string    `json:"message,omitempty"`
	Time    time.Time `json:"time,omitzero"`
}

// ShortCommit 返回缩短的提交号。
func (r Revision) ShortCommit() string {
	if len(r.Commit) > 8 {
		return r.Commit[:8]
	}
	return r.Commit
}

// Heading 是文档中的标题，用于目录与结构化切分。
type Heading struct {
	Level int    `json:"level"`
	Text  string `json:"text"`
	ID    string `json:"id"`
}

// Document 是文档树中的一个 markdown 文件。
type Document struct {
	Path     string         `json:"path"` // 相对文档根，"/" 分隔
	Slug     string         `json:"slug"` // URL 标识，去掉扩展名；目录首页为目录路径
	Title    string         `json:"title"`
	Hash     string         `json:"hash"`
	Size     int64          `json:"size"`
	Weight   int            `json:"weight,omitempty"`
	Meta     map[string]any `json:"meta,omitempty"`
	Headings []Heading      `json:"headings,omitempty"`

	Sections []Section `json:"sections,omitempty"` // 按标题切分的纯文本，供检索 / RAG 使用

	Content []byte   `json:"-"` // 原始 markdown（含 front matter），由存储按 Hash 单独保存
	Links   []string `json:"-"` // 文档中出现的链接与图片地址，仅在构建快照时使用
}

// Section 是文档中某个标题下的纯文本内容。
type Section struct {
	Heading string   `json:"heading,omitempty"`
	Level   int      `json:"level,omitempty"`
	Anchor  string   `json:"anchor,omitempty"`
	Path    []string `json:"path,omitempty"` // 从顶层到当前标题的标题链
	Text    string   `json:"text"`
}

// Text 返回文档全文纯文本。
func (d *Document) Text() string {
	var b strings.Builder
	for _, s := range d.Sections {
		if s.Heading != "" {
			b.WriteString(s.Heading)
			b.WriteString("\n")
		}
		b.WriteString(s.Text)
		b.WriteString("\n")
	}
	return b.String()
}

// Asset 是文档引用的非 markdown 文件（图片等）。
type Asset struct {
	Path string `json:"path"` // 相对仓库根：资源可能位于文档根之外，如 ../images/a.png
	Hash string `json:"hash"`
	Size int64  `json:"size"`
}

// Snapshot 是一棵文档树在某个版本的完整、不可变视图。
type Snapshot struct {
	Key         TreeKey     `json:"key"`
	Version     string      `json:"version"`
	Revision    Revision    `json:"revision"`
	Root        string      `json:"root"` // 文档根目录（相对仓库根），"." 表示仓库根
	Fingerprint string      `json:"fingerprint"`
	BuiltAt     time.Time   `json:"built_at"`
	Docs        []*Document `json:"docs"`
	Assets      []Asset     `json:"assets"`

	byPath map[string]*Document
	bySlug map[string]*Document
	assets map[string]Asset
	nav    *NavNode
}

// NewVersion 生成快照版本号：同步时间 + 短提交号，可按字典序排序。
func NewVersion(at time.Time, rev Revision) string {
	v := at.UTC().Format("20060102T150405.000000000")
	v = strings.ReplaceAll(v, ".", "")
	if c := rev.ShortCommit(); c != "" {
		v += "-" + c
	}
	return v
}

// Seal 为快照分配 slug、构建导航并按导航顺序排列文档；构建或加载快照后必须调用。
func (s *Snapshot) Seal() {
	s.byPath = make(map[string]*Document, len(s.Docs))
	s.bySlug = make(map[string]*Document, len(s.Docs))
	for _, d := range s.Docs {
		s.byPath[d.Path] = d
	}
	// 先为目录首页分配 slug，保证 guide/README.md 优先占用 "guide"。
	for _, wantIndex := range []bool{true, false} {
		for _, d := range s.Docs {
			if IsIndexDoc(d.Path) != wantIndex {
				continue
			}
			slug := SlugOf(d.Path)
			if _, taken := s.bySlug[slug]; taken {
				slug = d.Path
			}
			d.Slug = slug
			s.bySlug[slug] = d
		}
	}
	s.nav = BuildNav(s.Docs)
	s.Docs = s.nav.Flatten()
	s.assets = make(map[string]Asset, len(s.Assets))
	for _, a := range s.Assets {
		s.assets[a.Path] = a
	}
}

// Nav 返回文档树的导航结构。
func (s *Snapshot) Nav() *NavNode { return s.nav }

// Neighbors 返回导航顺序中的上一篇与下一篇文档。
func (s *Snapshot) Neighbors(d *Document) (prev, next *Document) {
	for i, x := range s.Docs {
		if x == d {
			if i > 0 {
				prev = s.Docs[i-1]
			}
			if i+1 < len(s.Docs) {
				next = s.Docs[i+1]
			}
			return prev, next
		}
	}
	return nil, nil
}

// DocByPath 按相对路径查找文档。
func (s *Snapshot) DocByPath(p string) *Document { return s.byPath[p] }

// DocBySlug 按 slug 查找文档。
func (s *Snapshot) DocBySlug(slug string) *Document { return s.bySlug[slug] }

// Lookup 按 slug 或路径查找文档，兼容带 .md 扩展名的链接。
func (s *Snapshot) Lookup(ref string) *Document {
	ref = strings.Trim(ref, "/")
	if ref == "." {
		ref = ""
	}
	if d := s.bySlug[ref]; d != nil {
		return d
	}
	if d := s.byPath[ref]; d != nil {
		return d
	}
	for _, ext := range slices.Concat(MarkdownExts, ProtoExts) {
		if d := s.byPath[ref+ext]; d != nil {
			return d
		}
	}
	return nil
}

// HasAsset 判断路径（相对仓库根）是否为已收录的资源文件。
func (s *Snapshot) HasAsset(p string) bool { _, ok := s.assets[p]; return ok }

// AssetByPath 按相对仓库根的路径查找资源。
func (s *Snapshot) AssetByPath(p string) (Asset, bool) { a, ok := s.assets[p]; return a, ok }

// RepoPath 把相对文档根的路径转换为相对仓库根的路径。
func (s *Snapshot) RepoPath(p string) string {
	if s.Root == "." || s.Root == "" {
		return p
	}
	return s.Root + "/" + p
}

// ChangeSet 是相邻两个快照之间的文档变更（路径相对文档根），下游（RAG 等）据此做增量处理。
type ChangeSet struct {
	Added         []string `json:"added"`
	Modified      []string `json:"modified"`
	Deleted       []string `json:"deleted"`
	Unchanged     int      `json:"unchanged"`
	AssetsChanged bool     `json:"assets_changed,omitempty"`
}

func (c ChangeSet) Empty() bool {
	return len(c.Added) == 0 && len(c.Modified) == 0 && len(c.Deleted) == 0 && !c.AssetsChanged
}

func (c ChangeSet) String() string {
	return fmt.Sprintf("+%d ~%d -%d =%d", len(c.Added), len(c.Modified), len(c.Deleted), c.Unchanged)
}

// Trigger 是同步的触发来源。
type Trigger string

const (
	TriggerStartup  Trigger = "startup"
	TriggerSchedule Trigger = "schedule"
	TriggerManual   Trigger = "manual"
	TriggerWebhook  Trigger = "webhook"
	TriggerCLI      Trigger = "cli"
)

// RunStatus 是一次同步的结果状态。
type RunStatus string

const (
	RunRunning   RunStatus = "running"
	RunSuccess   RunStatus = "success"
	RunUnchanged RunStatus = "unchanged" // 远端无新提交且配置未变，跳过重建
	RunFailed    RunStatus = "failed"
)

// SyncRun 记录一次同步过程。
type SyncRun struct {
	ID         string    `json:"id"`
	Key        TreeKey   `json:"key"`
	Trigger    Trigger   `json:"trigger"`
	Force      bool      `json:"force,omitempty"`
	Status     RunStatus `json:"status"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at,omitzero"`
	Revision   Revision  `json:"revision,omitzero"`
	Version    string    `json:"version,omitempty"`
	Docs       int       `json:"docs"`
	Changes    ChangeSet `json:"changes"`
	Error      string    `json:"error,omitempty"`
	HookErrors []string  `json:"hook_errors,omitempty"`
}

// Duration 返回同步耗时。
func (r SyncRun) Duration() time.Duration {
	if r.FinishedAt.IsZero() {
		return time.Since(r.StartedAt)
	}
	return r.FinishedAt.Sub(r.StartedAt)
}

// SyncEvent 在文档树同步出新快照后发布，是 RAG 索引、外部通知等下游能力的接入点。
type SyncEvent struct {
	Key      TreeKey
	Run      SyncRun
	Previous *Snapshot // 首次同步时为 nil
	Current  *Snapshot
	Changes  ChangeSet
	// Restored 为 true 表示快照是服务启动时从存储恢复的，并非新同步产生；
	// 内存索引需要据此重建，而持久化的下游（如向量库）通常可以忽略。
	Restored bool
	// Removed 为 true 表示文档树已被删除，Current 为 nil；下游应清理该文档树的数据。
	Removed bool
}
