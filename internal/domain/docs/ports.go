package docs

import (
	"context"
	"errors"
	"time"
)

var (
	ErrNotFound       = errors.New("not found")
	ErrSyncInProgress = errors.New("sync already in progress")
	ErrInvalid        = errors.New("invalid request")
	ErrConflict       = errors.New("already exists")
)

// Fetcher 把文档来源更新到最新版本。实现方负责缓存工作副本、并发互斥与凭据处理。
type Fetcher interface {
	Fetch(ctx context.Context, src Source) (Checkout, error)
}

// Checkout 是一次拉取得到的只读仓库视图，使用完毕必须 Close 以释放工作副本。
type Checkout interface {
	Revision() Revision
	// Files 列出仓库中全部文件（相对仓库根，"/" 分隔），不要求已下载文件内容。
	Files(ctx context.Context) ([]string, error)
	// Prepare 声明即将读取的目录，实现方可据此批量下载内容（如 git sparse checkout）。
	Prepare(ctx context.Context, dirs []string) error
	ReadFile(ctx context.Context, path string) ([]byte, error)
	Close() error
}

// ParsedDoc 是解析文档得到的结构化信息。
type ParsedDoc struct {
	// Content 是非 markdown 源文件（如 .proto）转换得到的 markdown；为空表示原文即 markdown。
	// 文档树保存、渲染与对外提供的都是这份 markdown。
	Content  []byte
	Title    string
	Weight   int
	Draft    bool
	Meta     map[string]any
	Headings []Heading
	Sections []Section
	Links    []string
}

// Parser 从文档中提取标题、元数据、纯文本与链接；非 markdown 源文件同时给出转换后的 markdown。
type Parser interface {
	Parse(path string, content []byte) (ParsedDoc, error)
}

// Blob 是待持久化的内容寻址数据（文档原文或资源文件）。
type Blob struct {
	Hash string
	Data []byte
}

// SnapshotStore 持久化快照。文档与资源内容按哈希去重保存，快照清单按版本保存。
type SnapshotStore interface {
	// Save 保存快照及其内容，并把它设为该文档树的当前版本。
	Save(ctx context.Context, snap *Snapshot, blobs []Blob) error
	// Current 加载当前版本快照（含文档原文），不存在时返回 ErrNotFound。
	Current(ctx context.Context, key TreeKey) (*Snapshot, error)
	// CurrentVersion 只读取当前版本号，不加载内容，不存在时返回 ErrNotFound。
	CurrentVersion(ctx context.Context, key TreeKey) (string, error)
	Load(ctx context.Context, key TreeKey, version string) (*Snapshot, error)
	// Versions 按从新到旧返回已保存的版本号。
	Versions(ctx context.Context, key TreeKey) ([]string, error)
	ReadBlob(ctx context.Context, hash string) ([]byte, error)
	// Prune 只保留最近 keep 个版本，并清理不再被引用的内容。
	Prune(ctx context.Context, key TreeKey, keep int) error
}

// TreeState 记录文档树的同步状态，独立于快照：
// 仅代码变更的提交会更新 LastRevision，但不会产生新快照。
type TreeState struct {
	Key             TreeKey   `json:"key"`
	LastRevision    Revision  `json:"last_revision"`
	LastFingerprint string    `json:"last_fingerprint"`
	LastSyncAt      time.Time `json:"last_sync_at,omitzero"`
	LastSuccessAt   time.Time `json:"last_success_at,omitzero"`
	LastRun         *SyncRun  `json:"last_run,omitempty"`
}

// StateStore 持久化同步状态与同步历史。
type StateStore interface {
	State(ctx context.Context, key TreeKey) (TreeState, error) // 不存在时返回零值
	SaveState(ctx context.Context, st TreeState) error
	AppendRun(ctx context.Context, run SyncRun) error
	Runs(ctx context.Context, key TreeKey, limit int) ([]SyncRun, error) // 从新到旧
}

// SyncHook 在文档树产生新快照后被调用，是 RAG 索引、向量化、外部通知等能力的扩展点。
// 实现方应只依赖 SyncEvent 中的快照与变更集，自行决定全量或增量处理。
type SyncHook interface {
	Name() string
	OnSync(ctx context.Context, ev SyncEvent) error
}

// SearchQuery 是一次文档检索请求。
type SearchQuery struct {
	Text      string
	Namespace string
	Tree      string
	Limit     int
}

// SearchHit 是一条检索结果，粒度为文档中的某个章节。
type SearchHit struct {
	Key     TreeKey `json:"key"`
	Path    string  `json:"path"`
	Slug    string  `json:"slug"`
	Title   string  `json:"title"`
	Heading string  `json:"heading,omitempty"`
	Anchor  string  `json:"anchor,omitempty"`
	Snippet string  `json:"snippet"`
	Score   float64 `json:"score"`
}

// Searcher 是检索能力的端口。内置实现为内存倒排索引，可替换为向量或混合检索。
type Searcher interface {
	Search(ctx context.Context, q SearchQuery) ([]SearchHit, error)
}

// RegisteredTree 是通过页面或 API 登记的文档树，与配置文件中的文档树合并提供服务。
// 凭据只按名称引用配置文件中的 credentials，不在登记数据中保存令牌或密钥。
type RegisteredTree struct {
	Namespace   string    `json:"namespace"`
	Name        string    `json:"name"`
	Title       string    `json:"title,omitempty"`
	Description string    `json:"description,omitempty"`
	Repo        string    `json:"repo"`
	Branch      string    `json:"branch,omitempty"`
	Provider    string    `json:"provider,omitempty"`
	Root        string    `json:"root,omitempty"`
	Include     []string  `json:"include,omitempty"`
	Exclude     []string  `json:"exclude,omitempty"`
	Credential  string    `json:"credential,omitempty"`
	CreatedAt   time.Time `json:"created_at,omitzero"`
}

// Key 返回登记项对应的文档树标识。
func (t RegisteredTree) Key() TreeKey { return TreeKey{Namespace: t.Namespace, Tree: t.Name} }

// TreeRegistry 持久化登记的文档树。内置实现为数据目录下的 JSON 文件，
// 多副本部署时可替换为数据库等共享存储。
type TreeRegistry interface {
	Load(ctx context.Context) ([]RegisteredTree, error)
	Save(ctx context.Context, trees []RegisteredTree) error
}
