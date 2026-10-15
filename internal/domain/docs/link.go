package docs

import (
	"net/url"
	"strings"
)

// LinkKind 是文档内链接解析后的目标类型。
type LinkKind int

const (
	LinkExternal LinkKind = iota // 外部地址、无法识别的协议或越出仓库的路径，原样保留
	LinkAnchor                   // 当前页面内锚点
	LinkDoc                      // 同一文档树中的文档
	LinkAsset                    // 已收录的资源文件
	LinkRepoFile                 // 仓库中未收录的文件（如源码），可指向代码浏览地址
)

// LinkTarget 是链接解析结果。
type LinkTarget struct {
	Kind     LinkKind
	Doc      *Document
	RepoPath string // LinkAsset / LinkRepoFile 时为相对仓库根的路径
	Fragment string
}

// RepoRef 把文档中的站内引用解析为相对仓库根的路径。按 GitHub 的约定，
// 相对路径以当前文档所在目录为基准，以 "/" 开头的路径以仓库根为基准。
// 外部地址、纯锚点或越出仓库的路径返回 ok=false。
func (s *Snapshot) RepoRef(from *Document, dest string) (repoPath, fragment string, ok bool) {
	if dest == "" || strings.HasPrefix(dest, "#") || strings.HasPrefix(dest, "//") {
		return "", "", false
	}
	u, err := url.Parse(dest)
	if err != nil || u.Scheme != "" || u.Host != "" || u.Path == "" {
		return "", "", false
	}
	repoPath = ResolveRel(s.RepoPath(from.Path), u.Path)
	if Escapes(repoPath) || repoPath == "." {
		return "", "", false
	}
	return repoPath, u.Fragment, true
}

// ResolveLink 解析文档中的链接目标。
func (s *Snapshot) ResolveLink(from *Document, dest string) LinkTarget {
	if strings.HasPrefix(dest, "#") {
		return LinkTarget{Kind: LinkAnchor, Fragment: dest[1:]}
	}
	repoPath, frag, ok := s.RepoRef(from, dest)
	if !ok {
		return LinkTarget{Kind: LinkExternal}
	}
	if rel, ok := s.relToRoot(repoPath); ok {
		if d := s.Lookup(rel); d != nil {
			return LinkTarget{Kind: LinkDoc, Doc: d, Fragment: frag}
		}
	}
	if s.HasAsset(repoPath) {
		return LinkTarget{Kind: LinkAsset, RepoPath: repoPath, Fragment: frag}
	}
	return LinkTarget{Kind: LinkRepoFile, RepoPath: repoPath, Fragment: frag}
}

// relToRoot 把相对仓库根的路径转换为相对文档根的路径。
func (s *Snapshot) relToRoot(repoPath string) (string, bool) {
	if s.Root == "." || s.Root == "" {
		return repoPath, true
	}
	if repoPath == s.Root {
		return ".", true
	}
	if rel, ok := strings.CutPrefix(repoPath, s.Root+"/"); ok {
		return rel, true
	}
	return "", false
}
