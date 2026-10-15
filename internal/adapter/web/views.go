package web

import (
	"net/url"
	"strings"
	"time"

	"github.com/rushteam/getdocs/internal/application/catalog"
	"github.com/rushteam/getdocs/internal/domain/docs"
)

// treeView 是文档树对外展示的信息，不包含凭据等敏感配置。
type treeView struct {
	Namespace   string    `json:"namespace"`
	Name        string    `json:"name"`
	Title       string    `json:"title"`
	Description string    `json:"description,omitempty"`
	URL         string    `json:"url"`
	Source      string    `json:"source"`
	Provider    string    `json:"provider,omitempty"`
	Branch      string    `json:"branch,omitempty"`
	Root        string    `json:"root,omitempty"`
	AutoSync    bool      `json:"auto_sync"`
	Interval    string    `json:"interval,omitempty"`
	Synced      bool      `json:"synced"`
	Docs        int       `json:"docs"`
	Version     string    `json:"version,omitempty"`
	Commit      string    `json:"commit,omitempty"`
	CommitTime  time.Time `json:"commit_time,omitzero"`
	BuiltAt     time.Time `json:"built_at,omitzero"`
	LastSyncAt  time.Time `json:"last_sync_at,omitzero"`
	LastStatus  string    `json:"last_status,omitempty"`
	LastError   string    `json:"last_error,omitempty"`
	Running     bool      `json:"running"`
	Registered  bool      `json:"registered,omitempty"` // 通过页面登记，可在页面删除
}

func newTreeView(st catalog.TreeStatus) treeView {
	spec := st.Spec
	v := treeView{
		Namespace: spec.Key.Namespace, Name: spec.Key.Tree, Title: spec.Title, Description: spec.Description,
		URL: treeURL(spec.Key), Source: spec.Source.Display(), Provider: string(spec.Source.Provider), Branch: spec.Source.Branch, Root: spec.Root,
		AutoSync: spec.AutoSync, LastSyncAt: st.State.LastSyncAt, Running: st.Running != nil,
	}
	if spec.AutoSync && spec.Interval > 0 {
		v.Interval = spec.Interval.String()
	}
	if snap := st.Snapshot; snap != nil {
		v.Synced, v.Docs, v.Version, v.BuiltAt = true, len(snap.Docs), snap.Version, snap.BuiltAt
		if snap.Root != "." { // 单文件文档树的快照根是 "."，展示配置的文件名更直观
			v.Root = snap.Root
		}
		v.Commit, v.CommitTime = snap.Revision.Commit, snap.Revision.Time
		if snap.Revision.Branch != "" {
			v.Branch = snap.Revision.Branch
		}
	}
	if r := st.State.LastRun; r != nil {
		v.LastStatus, v.LastError = string(r.Status), r.Error
	}
	return v
}

func treeURL(k docs.TreeKey) string {
	return "/docs/" + url.PathEscape(k.Namespace) + "/" + url.PathEscape(k.Tree) + "/"
}

func docURL(k docs.TreeKey, slug string) string { return treeURL(k) + escapePath(slug) }

func assetURL(k docs.TreeKey, repoPath, version string) string {
	return "/assets/" + url.PathEscape(k.Namespace) + "/" + url.PathEscape(k.Tree) + "/" +
		escapePath(repoPath) + "?v=" + url.QueryEscape(version)
}

func escapePath(p string) string {
	if p == "" {
		return ""
	}
	parts := strings.Split(p, "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}

func withFragment(u, frag string) string {
	if frag == "" {
		return u
	}
	return u + (&url.URL{Fragment: frag}).String()
}

// navItem 是侧边栏中的一项。
type navItem struct {
	Title    string
	URL      string // 没有首页的目录为空
	Active   bool
	Open     bool
	IsDir    bool
	Children []*navItem
}

func buildNav(snap *docs.Snapshot, current, query string) []*navItem {
	var conv func(n *docs.NavNode) *navItem
	conv = func(n *docs.NavNode) *navItem {
		it := &navItem{Title: n.Title, IsDir: n.IsDir}
		if n.Doc() != nil {
			it.URL = docURL(snap.Key, n.Slug) + query
			it.Active = n.Slug == current
		}
		it.Open = n.Contains(current)
		for _, c := range n.Children {
			it.Children = append(it.Children, conv(c))
		}
		return it
	}
	root := snap.Nav()
	var out []*navItem
	if root.Doc() != nil {
		out = append(out, &navItem{Title: root.Doc().Title, URL: docURL(snap.Key, "") + query, Active: current == ""})
	}
	for _, c := range root.Children {
		out = append(out, conv(c))
	}
	return out
}
