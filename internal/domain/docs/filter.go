package docs

import (
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// Filter 决定文档根下哪些文件进入文档树。模式为 doublestar glob，匹配相对文档根的路径。
type Filter struct {
	Include     []string `json:"include,omitempty"` // 为空表示全部 markdown 文件
	Exclude     []string `json:"exclude,omitempty"`
	MaxFileSize int64    `json:"max_file_size,omitempty"` // 字节，0 表示使用默认值
	Proto       bool     `json:"proto,omitempty"`         // 同时收录 .proto 文件，生成接口文档
}

// DefaultExcludes 总是生效的排除规则：依赖目录、隐藏目录与构建产物。
var DefaultExcludes = []string{
	"**/node_modules/**",
	"**/vendor/**",
	"**/.*/**",
	"**/_site/**",
	"**/dist/**",
	"**/build/**",
	"**/target/**",
}

// DefaultMaxFileSize 是单个文档的默认大小上限。
const DefaultMaxFileSize = 4 << 20

// Validate 检查 glob 模式是否合法。
func (f Filter) Validate() error {
	for _, p := range slices.Concat(f.Include, f.Exclude) {
		if !doublestar.ValidatePattern(p) {
			return fmt.Errorf("invalid glob pattern %q", p)
		}
	}
	return nil
}

// MaxSize 返回生效的文件大小上限。
func (f Filter) MaxSize() int64 {
	if f.MaxFileSize > 0 {
		return f.MaxFileSize
	}
	return DefaultMaxFileSize
}

// Excluded 判断相对文档根的路径（文件或目录）是否被排除。
func (f Filter) Excluded(rel string) bool {
	for _, p := range DefaultExcludes {
		if matchPrefix(p, rel) {
			return true
		}
	}
	for _, p := range f.Exclude {
		if matchPrefix(p, rel) {
			return true
		}
	}
	return false
}

// IsDoc 判断文件类型是否可作为文档：markdown，或开启 Proto 时的 .proto 文件。
func (f Filter) IsDoc(p string) bool { return IsMarkdown(p) || (f.Proto && IsProto(p)) }

// MatchDoc 判断相对文档根的文件是否作为文档收录。
func (f Filter) MatchDoc(rel string) bool {
	if !f.IsDoc(rel) || f.Excluded(rel) {
		return false
	}
	if len(f.Include) == 0 {
		return true
	}
	for _, p := range f.Include {
		if ok, _ := doublestar.Match(p, rel); ok {
			return true
		}
	}
	return false
}

// matchPrefix 判断路径本身或其任一上级目录是否匹配模式，
// 使 "drafts/**" 与 "drafts" 都能排除整个目录。
func matchPrefix(pattern, rel string) bool {
	for p := rel; p != "." && p != "/" && p != ""; p = path.Dir(p) {
		if ok, _ := doublestar.Match(pattern, p); ok {
			return true
		}
		if ok, _ := doublestar.Match(pattern, p+"/"); ok {
			return true
		}
	}
	return false
}

// RootCandidates 是自动探测文档根时依次尝试的目录。
var RootCandidates = []string{
	"docs", "doc", "documentation", "wiki",
	"website/docs", "site/docs", "docs/content", "content/docs",
}

// DetectRoot 在仓库文件列表中自动选择文档根：
// 优先选择候选目录中第一个包含文档的目录；否则回退到仓库根（扫描整个仓库，依赖过滤规则收敛范围）。
// files 为相对仓库根的文件路径。
func DetectRoot(files []string, filter Filter) string {
	for _, dir := range RootCandidates {
		prefix := dir + "/"
		for _, f := range files {
			if lower := strings.ToLower(f); strings.HasPrefix(lower, prefix) {
				if filter.MatchDoc(f[len(prefix):]) {
					return f[:len(dir)]
				}
			}
		}
	}
	return "."
}
