// Package mcp 把文档服务暴露为 MCP 服务器，供 AI agent 浏览、检索与阅读文档。
// 同时支持 Streamable HTTP（挂载到 Web 服务）与 stdio（本地 agent 直接拉起进程）。
package mcp

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/rushteam/getdocs/internal/application/catalog"
	"github.com/rushteam/getdocs/internal/domain/docs"
)

const instructions = `This server hosts documentation synced from git repositories, organized as namespace → tree → document.
Typical flow: call list_trees to see what is available, search_docs to find relevant sections,
then read_doc to fetch the full markdown of a document (optionally a single section via its anchor).
Use get_tree to browse a tree's table of contents when you need an overview.`

// Server 封装 MCP 服务器。
type Server struct {
	svc      *catalog.Service
	searcher docs.Searcher
	baseURL  string
	srv      *mcp.Server
}

// New 创建 MCP 服务器并注册工具与资源。searcher 为 nil 时不提供 search_docs。
func New(svc *catalog.Service, searcher docs.Searcher, baseURL, version string) *Server {
	s := &Server{svc: svc, searcher: searcher, baseURL: baseURL}
	s.srv = mcp.NewServer(&mcp.Implementation{Name: "getdocs", Title: "GetDocs", Version: version},
		&mcp.ServerOptions{Instructions: instructions})
	s.register()
	return s
}

// MCPServer 返回底层 SDK 服务器，用于挂载 HTTP 或 stdio 传输。
func (s *Server) MCPServer() *mcp.Server { return s.srv }

type listTreesIn struct {
	Namespace string `json:"namespace,omitempty" jsonschema:"only list trees in this namespace"`
}

type treeRef struct {
	Namespace string `json:"namespace" jsonschema:"namespace name"`
	Tree      string `json:"tree" jsonschema:"tree name"`
}

type getTreeIn struct {
	treeRef
	Version string `json:"version,omitempty" jsonschema:"snapshot version; defaults to the latest"`
}

type searchIn struct {
	Query     string `json:"query" jsonschema:"keywords or a natural language question"`
	Namespace string `json:"namespace,omitempty" jsonschema:"restrict to a namespace"`
	Tree      string `json:"tree,omitempty" jsonschema:"restrict to a tree (requires namespace)"`
	Limit     int    `json:"limit,omitempty" jsonschema:"max results, default 8, at most 50"`
}

type readDocIn struct {
	treeRef
	Path    string `json:"path" jsonschema:"document path or slug as returned by get_tree or search_docs"`
	Section string `json:"section,omitempty" jsonschema:"heading anchor; when set only that section (with its subsections) is returned"`
	Version string `json:"version,omitempty" jsonschema:"snapshot version; defaults to the latest"`
}

type syncIn struct {
	treeRef
	Force bool `json:"force,omitempty" jsonschema:"rebuild even if the commit has not changed"`
}

func (s *Server) register() {
	ro := &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: ptr(false)}
	mcp.AddTool(s.srv, &mcp.Tool{
		Name: "list_trees", Title: "List documentation trees", Annotations: ro,
		Description: "List namespaces and their documentation trees, with source repository, last sync commit and document count.",
	}, s.listTrees)
	mcp.AddTool(s.srv, &mcp.Tool{
		Name: "get_tree", Title: "Get table of contents", Annotations: ro,
		Description: "Return the table of contents of a documentation tree: every document path with its title and headings.",
	}, s.getTree)
	if s.searcher != nil {
		mcp.AddTool(s.srv, &mcp.Tool{
			Name: "search_docs", Title: "Search documentation", Annotations: ro,
			Description: "Full-text search across documentation. Returns matching sections with path, heading anchor and snippet; follow up with read_doc.",
		}, s.search)
	}
	mcp.AddTool(s.srv, &mcp.Tool{
		Name: "read_doc", Title: "Read a document", Annotations: ro,
		Description: "Return the markdown source of a document, or of a single section when an anchor is given.",
	}, s.readDoc)

	s.srv.AddResourceTemplate(&mcp.ResourceTemplate{
		Name: "document", Title: "Documentation page", MIMEType: "text/markdown",
		URITemplate: "docs://{namespace}/{tree}/{+path}",
		Description: "Markdown source of a document in a documentation tree.",
	}, s.readResource)
}

// RegisterSync 注册触发同步的工具。它有副作用，默认不注册，由组合根按配置决定。
func (s *Server) RegisterSync(lifecycle context.Context) {
	mcp.AddTool(s.srv, &mcp.Tool{
		Name: "sync_tree", Title: "Sync a documentation tree",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: ptr(false), IdempotentHint: true},
		Description: "Pull the latest commit of a tree's repository and rebuild the documentation snapshot.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in syncIn) (*mcp.CallToolResult, any, error) {
		key := docs.TreeKey{Namespace: in.Namespace, Tree: in.Tree}
		// 同步与 MCP 请求解耦：客户端断开不应中断进行中的 git 拉取，但仍受服务停机控制。
		run, err := s.svc.Sync(lifecycle, key, docs.TriggerManual, in.Force)
		if err != nil {
			return nil, nil, err
		}
		return text(fmt.Sprintf("status: %s\ncommit: %s\nversion: %s\ndocs: %d\nchanges: %s",
			run.Status, run.Revision.ShortCommit(), run.Version, run.Docs, run.Changes)), nil, nil
	})
}

func (s *Server) listTrees(ctx context.Context, _ *mcp.CallToolRequest, in listTreesIn) (*mcp.CallToolResult, any, error) {
	var b strings.Builder
	for _, ns := range s.svc.Namespaces() {
		if in.Namespace != "" && ns.Name != in.Namespace {
			continue
		}
		fmt.Fprintf(&b, "# namespace: %s", ns.Name)
		if ns.Title != ns.Name {
			fmt.Fprintf(&b, " (%s)", ns.Title)
		}
		b.WriteString("\n")
		if ns.Description != "" {
			b.WriteString(ns.Description + "\n")
		}
		for _, t := range ns.Trees {
			fmt.Fprintf(&b, "- tree: %s — %s\n", t.Key.Tree, t.Title)
			if t.Description != "" {
				fmt.Fprintf(&b, "  description: %s\n", t.Description)
			}
			fmt.Fprintf(&b, "  source: %s\n", t.Source.Display())
			if snap, err := s.svc.Snapshot(t.Key); err == nil {
				fmt.Fprintf(&b, "  docs: %d, root: %s, commit: %s, version: %s\n",
					len(snap.Docs), snap.Root, snap.Revision.ShortCommit(), snap.Version)
			} else {
				b.WriteString("  status: not synced yet\n")
			}
		}
		b.WriteString("\n")
	}
	if b.Len() == 0 {
		return nil, nil, fmt.Errorf("namespace %q not found", in.Namespace)
	}
	return text(b.String()), nil, nil
}

func (s *Server) getTree(ctx context.Context, _ *mcp.CallToolRequest, in getTreeIn) (*mcp.CallToolResult, any, error) {
	snap, err := s.svc.SnapshotAt(ctx, docs.TreeKey{Namespace: in.Namespace, Tree: in.Tree}, in.Version)
	if err != nil {
		return nil, nil, err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "tree: %s  version: %s  commit: %s  docs: %d\n\n", snap.Key, snap.Version, snap.Revision.ShortCommit(), len(snap.Docs))
	var walk func(n *docs.NavNode, depth int)
	walk = func(n *docs.NavNode, depth int) {
		indent := strings.Repeat("  ", depth)
		if n.Path != "." {
			if n.IsDir {
				fmt.Fprintf(&b, "%s- %s/", indent, n.Title)
				if d := n.Doc(); d != nil {
					fmt.Fprintf(&b, "  (index: %s)", d.Path)
				}
				b.WriteString("\n")
			} else {
				fmt.Fprintf(&b, "%s- %s  [%s]\n", indent, n.Title, n.Path)
			}
			if d := n.Doc(); d != nil && !n.IsDir {
				for _, h := range d.Headings {
					if h.Level >= 2 && h.Level <= 3 {
						fmt.Fprintf(&b, "%s    %s %s  #%s\n", indent, strings.Repeat("#", h.Level), h.Text, h.ID)
					}
				}
			}
		} else if d := n.Doc(); d != nil {
			fmt.Fprintf(&b, "- %s  [%s]\n", d.Title, d.Path)
		}
		for _, c := range n.Children {
			walk(c, depth+boolInt(n.Path != "."))
		}
	}
	walk(snap.Nav(), 0)
	return text(b.String()), nil, nil
}

func (s *Server) search(ctx context.Context, _ *mcp.CallToolRequest, in searchIn) (*mcp.CallToolResult, any, error) {
	limit := in.Limit
	if limit <= 0 {
		limit = 8
	}
	hits, err := s.searcher.Search(ctx, docs.SearchQuery{Text: in.Query, Namespace: in.Namespace, Tree: in.Tree, Limit: min(limit, 50)})
	if err != nil {
		return nil, nil, err
	}
	if len(hits) == 0 {
		return text("No results. Try different keywords, or call get_tree to browse."), nil, nil
	}
	var b strings.Builder
	for i, h := range hits {
		fmt.Fprintf(&b, "%d. %s", i+1, h.Title)
		if h.Heading != "" && h.Heading != h.Title {
			fmt.Fprintf(&b, " › %s", h.Heading)
		}
		fmt.Fprintf(&b, "\n   namespace: %s  tree: %s  path: %s", h.Key.Namespace, h.Key.Tree, h.Path)
		if h.Anchor != "" {
			fmt.Fprintf(&b, "  section: %s", h.Anchor)
		}
		if s.baseURL != "" {
			fmt.Fprintf(&b, "\n   url: %s", s.docURL(h.Key, h.Slug, h.Anchor))
		}
		fmt.Fprintf(&b, "\n   %s\n\n", h.Snippet)
	}
	return text(b.String()), nil, nil
}

func (s *Server) readDoc(ctx context.Context, _ *mcp.CallToolRequest, in readDocIn) (*mcp.CallToolResult, any, error) {
	snap, err := s.svc.SnapshotAt(ctx, docs.TreeKey{Namespace: in.Namespace, Tree: in.Tree}, in.Version)
	if err != nil {
		return nil, nil, err
	}
	d := snap.Lookup(in.Path)
	if d == nil {
		return nil, nil, fmt.Errorf("document %q not found in %s; call get_tree to list documents", in.Path, snap.Key)
	}
	body := string(d.Content)
	if in.Section != "" {
		sec, ok := extractSection(body, d.Headings, in.Section)
		if !ok {
			return nil, nil, fmt.Errorf("section %q not found in %s", in.Section, d.Path)
		}
		body = sec
	}
	var b strings.Builder
	fmt.Fprintf(&b, "<!-- tree: %s  path: %s  commit: %s", snap.Key, d.Path, snap.Revision.ShortCommit())
	if s.baseURL != "" {
		fmt.Fprintf(&b, "  url: %s", s.docURL(snap.Key, d.Slug, in.Section))
	}
	b.WriteString(" -->\n\n")
	b.WriteString(body)
	return text(b.String()), nil, nil
}

func (s *Server) readResource(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	u, err := url.Parse(req.Params.URI)
	if err != nil || u.Scheme != "docs" {
		return nil, mcp.ResourceNotFoundError(req.Params.URI)
	}
	tree, p, _ := strings.Cut(strings.TrimPrefix(u.Path, "/"), "/")
	snap, err := s.svc.Snapshot(docs.TreeKey{Namespace: u.Host, Tree: tree})
	if err != nil {
		return nil, mcp.ResourceNotFoundError(req.Params.URI)
	}
	d := snap.Lookup(p)
	if d == nil {
		return nil, mcp.ResourceNotFoundError(req.Params.URI)
	}
	return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{
		URI: req.Params.URI, MIMEType: "text/markdown", Text: string(d.Content),
	}}}, nil
}

func (s *Server) docURL(k docs.TreeKey, slug, anchor string) string {
	u := s.baseURL + "/docs/" + url.PathEscape(k.Namespace) + "/" + url.PathEscape(k.Tree) + "/"
	if slug != "" {
		u += escapePath(slug)
	}
	if anchor != "" {
		u += "#" + url.PathEscape(anchor)
	}
	return u
}

func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}

// extractSection 截取锚点对应标题到下一个同级或更高级标题之间的 markdown 原文。
func extractSection(body string, headings []docs.Heading, anchor string) (string, bool) {
	idx := -1
	for i, h := range headings {
		if h.ID == anchor {
			idx = i
			break
		}
	}
	if idx < 0 {
		return "", false
	}
	// 按出现顺序在原文中定位标题行；同名标题通过已匹配位置向后推进区分。
	lines := strings.Split(body, "\n")
	inFence := false
	hi := 0
	start, end := -1, len(lines)
	for li, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence || hi >= len(headings) {
			continue
		}
		level := headingLevel(trimmed)
		if level == 0 || level != headings[hi].Level {
			continue
		}
		if hi == idx {
			start = li
		} else if hi > idx && headings[hi].Level <= headings[idx].Level {
			end = li
			break
		}
		hi++
	}
	if start < 0 {
		return "", false
	}
	return strings.TrimSpace(strings.Join(lines[start:end], "\n")) + "\n", true
}

func headingLevel(line string) int {
	n := 0
	for n < len(line) && n < 7 && line[n] == '#' {
		n++
	}
	if n == 0 || n > 6 || (len(line) > n && line[n] != ' ' && line[n] != '\t') {
		return 0
	}
	return n
}

func text(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func ptr[T any](v T) *T { return &v }

// IsNotFound 判断错误是否为资源不存在。
func IsNotFound(err error) bool { return errors.Is(err, docs.ErrNotFound) }
