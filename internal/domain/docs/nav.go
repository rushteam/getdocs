package docs

import (
	"cmp"
	"path"
	"slices"
	"strings"
)

// NavNode 是文档树导航中的一个节点：目录或文档。
type NavNode struct {
	Name     string     `json:"name"`
	Title    string     `json:"title"`
	Path     string     `json:"path"`           // 目录路径或文档路径
	Slug     string     `json:"slug,omitempty"` // 文档节点，或拥有首页的目录节点
	IsDir    bool       `json:"is_dir,omitempty"`
	Weight   int        `json:"-"`
	Children []*NavNode `json:"children,omitempty"`

	doc *Document
}

// Doc 返回节点对应的文档；目录节点返回其首页（可能为 nil）。
func (n *NavNode) Doc() *Document { return n.doc }

// BuildNav 根据文档路径构建导航树。目录首页挂在目录节点上，不再作为独立子节点；
// 同级按 weight（front matter）、目录优先、标题排序。
func BuildNav(list []*Document) *NavNode {
	root := &NavNode{Name: "", Path: ".", IsDir: true}
	dirs := map[string]*NavNode{".": root}
	var ensureDir func(dir string) *NavNode
	ensureDir = func(dir string) *NavNode {
		if n, ok := dirs[dir]; ok {
			return n
		}
		parent := ensureDir(path.Dir(dir))
		n := &NavNode{Name: path.Base(dir), Title: HumanizeName(path.Base(dir)), Path: dir, IsDir: true}
		parent.Children = append(parent.Children, n)
		dirs[dir] = n
		return n
	}

	for _, d := range list {
		dir := path.Dir(d.Path)
		if IsIndexDoc(d.Path) && d.Slug == SlugOf(d.Path) {
			n := ensureDir(dir)
			if n.doc == nil {
				n.doc, n.Slug, n.Weight = d, d.Slug, d.Weight
				if d.Title != "" && dir != "." {
					n.Title = d.Title
				}
				continue
			}
		}
		parent := ensureDir(dir)
		parent.Children = append(parent.Children, &NavNode{
			Name: path.Base(d.Path), Title: d.Title, Path: d.Path, Slug: d.Slug, Weight: d.Weight, doc: d,
		})
	}
	root.sort()
	return root
}

func (n *NavNode) sort() {
	slices.SortStableFunc(n.Children, func(a, b *NavNode) int {
		if c := cmp.Compare(weightKey(a.Weight), weightKey(b.Weight)); c != 0 {
			return c
		}
		if a.IsDir != b.IsDir {
			if a.IsDir {
				return 1 // 同权重下文件在前，目录在后，阅读顺序更自然
			}
			return -1
		}
		return cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
	for _, c := range n.Children {
		c.sort()
	}
}

// weightKey 让未设置权重（0）的节点排在显式设置权重的节点之后。
func weightKey(w int) int {
	if w == 0 {
		return 1 << 30
	}
	return w
}

// Flatten 以深度优先的阅读顺序返回全部文档。
func (n *NavNode) Flatten() []*Document {
	var out []*Document
	var walk func(*NavNode)
	walk = func(x *NavNode) {
		if x.doc != nil {
			out = append(out, x.doc)
		}
		for _, c := range x.Children {
			walk(c)
		}
	}
	walk(n)
	return out
}

// Contains 判断节点子树中是否包含指定 slug，用于导航展开当前路径。
func (n *NavNode) Contains(slug string) bool {
	if n.Slug == slug && n.doc != nil {
		return true
	}
	for _, c := range n.Children {
		if c.Contains(slug) {
			return true
		}
	}
	return false
}
