package web

import (
	"bytes"
	"fmt"
	"html"
	"html/template"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	texttemplate "text/template"

	"getdocs/internal/domain/docs"
	"getdocs/internal/infra/markdown"
)

// 面向 AI 与开发者的入口：/llms.txt、/llms-full.txt、/ai（接入说明）与 /ai.md（其 markdown 版本）。

func rawURL(k docs.TreeKey, p string) string {
	return "/api/v1/trees/" + escapePath(k.String()) + "/docs/" + escapePath(p) + "?format=raw"
}

func (s *Server) parseGuide() error {
	t, err := texttemplate.New("ai.md").Funcs(texttemplate.FuncMap{
		"cell": func(v string) string { return strings.ReplaceAll(v, "|", `\|`) },
	}).ParseFS(assets, "templates/ai.md")
	if err != nil {
		return fmt.Errorf("parse ai.md: %w", err)
	}
	s.guide = t
	return nil
}

type guideData struct {
	Title   string
	Base    string
	Search  bool
	MCP     bool
	MCPAuth bool
	Example guideExample
	Trees   []guideTree
}

// guideExample 是示例命令中使用的文档，取第一棵已同步文档树的第一篇文档，使示例可以直接运行。
type guideExample struct {
	NS, Tree, Key, Path, Slug string
	Raw, API, Page            string
}

type guideTree struct {
	Key, Title string
	Docs       int
}

// guideMarkdown 生成接入说明，其中的地址都是当前服务的实际地址。
func (s *Server) guideMarkdown(r *http.Request) ([]byte, error) {
	base := s.baseURL(r)
	data := guideData{
		Title: s.opts.Title, Base: base, Search: s.searcher != nil,
		MCP: s.opts.MCP != nil, MCPAuth: s.opts.MCPToken != "",
	}
	for _, ns := range s.svc.Namespaces() {
		for _, spec := range ns.Trees {
			snap, err := s.svc.Snapshot(spec.Key)
			if err != nil {
				continue
			}
			data.Trees = append(data.Trees, guideTree{Key: escapePath(spec.Key.String()), Title: spec.Title, Docs: len(snap.Docs)})
			if data.Example.Raw == "" && len(snap.Docs) > 0 {
				d := snap.Docs[0]
				key := escapePath(spec.Key.String())
				data.Example = guideExample{
					NS: spec.Key.Namespace, Tree: spec.Key.Tree, Key: key, Path: escapePath(d.Path), Slug: d.Slug,
					Raw:  base + rawURL(spec.Key, d.Path),
					API:  base + "/api/v1/trees/" + key + "/docs/" + escapePath(d.Path),
					Page: base + docURL(spec.Key, d.Slug),
				}
			}
		}
	}
	if data.Example.Raw == "" {
		data.Example = guideExample{
			NS: "{namespace}", Tree: "{tree}", Key: "{namespace}/{tree}", Path: "guide/install.md", Slug: "guide/install",
			Raw:  base + "/api/v1/trees/{namespace}/{tree}/docs/guide/install.md?format=raw",
			API:  base + "/api/v1/trees/{namespace}/{tree}/docs/guide/install.md",
			Page: base + "/docs/{namespace}/{tree}/guide/install",
		}
	}
	var buf bytes.Buffer
	if err := s.guide.Execute(&buf, data); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (s *Server) aiMarkdown(w http.ResponseWriter, r *http.Request) {
	md, err := s.guideMarkdown(r)
	if err != nil {
		s.apiError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	_, _ = w.Write(md)
}

func (s *Server) aiPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Vary", "Accept")
	if acceptsMarkdown(r) {
		s.aiMarkdown(w, r)
		return
	}
	md, err := s.guideMarkdown(r)
	if err != nil {
		s.errorPage(w, r, err)
		return
	}
	rendered, err := s.md.Render(&docs.Document{Path: "ai.md", Content: md}, markdown.RenderOptions{})
	if err != nil {
		s.errorPage(w, r, err)
		return
	}
	s.html(w, r, http.StatusOK, "ai", page{
		Title: "AI 与开发者接入", Alternate: "/ai.md",
		Body: template.HTML(rendered.HTML), // 已经过 bluemonday 清洗
	})
}

// acceptsMarkdown 判断客户端是否更希望得到 markdown：Accept 中 text/markdown 的权重不低于 text/html。
// 浏览器从不声明 text/markdown，因此只影响主动请求 markdown 的 agent 与脚本。
func acceptsMarkdown(r *http.Request) bool {
	var md, htm float64
	for _, part := range strings.Split(r.Header.Get("Accept"), ",") {
		mt, params, err := mime.ParseMediaType(strings.TrimSpace(part))
		if err != nil {
			continue
		}
		q := 1.0
		if v, ok := params["q"]; ok {
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				q = f
			}
		}
		switch mt {
		case "text/markdown", "text/x-markdown":
			md = max(md, q)
		case "text/html":
			htm = max(htm, q)
		}
	}
	return md > 0 && md >= htm
}

func writeMarkdown(w http.ResponseWriter, d *docs.Document) {
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("ETag", `"`+d.Hash+`"`)
	_, _ = w.Write(d.Content)
}

// llmsTxt 按 llms.txt 约定（https://llmstxt.org）列出接口用法与全部文档，便于 AI agent 发现与抓取 markdown 原文。
func (s *Server) llmsTxt(w http.ResponseWriter, r *http.Request) {
	base := s.baseURL(r)
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", s.opts.Title)
	b.WriteString("> Documentation synced from git repositories, organized as namespace → tree → document. " +
		"Every link below returns the raw markdown of one document.\n\n")
	b.WriteString("How to access this documentation programmatically (all read endpoints are public):\n\n")
	fmt.Fprintf(&b, "- All documents in one file: %s/llms-full.txt (narrow with ?namespace=&tree=)\n", base)
	fmt.Fprintf(&b, "- Raw markdown of a document: %s/api/v1/trees/{namespace}/{tree}/docs/{path}?format=raw "+
		"(path may also be the slug used in page URLs; page URLs return markdown with `Accept: text/markdown`)\n", base)
	fmt.Fprintf(&b, "- Documents of a tree (JSON): %s/api/v1/trees/{namespace}/{tree}/docs\n", base)
	if s.searcher != nil {
		fmt.Fprintf(&b, "- Full-text search (JSON): %s/api/v1/search?q={query}&namespace=&tree=&limit= "+
			"(hits carry key.namespace, key.tree, path and a section anchor)\n", base)
	}
	fmt.Fprintf(&b, "- RAG chunks (JSON): %s/api/v1/trees/{namespace}/{tree}/chunks (?path= one document, ?since={version} incremental)\n", base)
	if s.opts.MCP != nil {
		auth := ""
		if s.opts.MCPToken != "" {
			auth = ", requires Authorization: Bearer <token>"
		}
		fmt.Fprintf(&b, "- MCP server (streamable HTTP%s): %s/mcp with tools list_trees, get_tree, search_docs, read_doc\n", auth, base)
	}
	fmt.Fprintf(&b, "- Integration guide: %s/ai (markdown: %s/ai.md)\n", base, base)

	for _, ns := range s.svc.Namespaces() {
		for _, spec := range ns.Trees {
			snap, err := s.svc.Snapshot(spec.Key)
			if err != nil {
				continue
			}
			fmt.Fprintf(&b, "\n## %s / %s\n\n", ns.Title, spec.Title)
			fmt.Fprintf(&b, "namespace=%s tree=%s", spec.Key.Namespace, spec.Key.Tree)
			if spec.Description != "" {
				fmt.Fprintf(&b, " — %s", spec.Description)
			}
			b.WriteString("\n\n")
			for _, d := range snap.Docs {
				fmt.Fprintf(&b, "- [%s](%s)", d.Title, base+rawURL(spec.Key, d.Path))
				if desc, ok := d.Meta["description"].(string); ok && strings.TrimSpace(desc) != "" {
					fmt.Fprintf(&b, ": %s", strings.Join(strings.Fields(desc), " "))
				}
				b.WriteString("\n")
			}
		}
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, b.String())
}

// llmsFullTxt 输出全部（或指定命名空间 / 文档树的）文档原文，每篇用 <document> 标签包裹并标明来源。
func (s *Server) llmsFullTxt(w http.ResponseWriter, r *http.Request) {
	base := s.baseURL(r)
	q := r.URL.Query()
	type entry struct {
		ns   *docs.Namespace
		spec *docs.TreeSpec
		snap *docs.Snapshot
	}
	var entries []entry
	for _, ns := range s.svc.Namespaces() {
		if v := q.Get("namespace"); v != "" && ns.Name != v {
			continue
		}
		for _, spec := range ns.Trees {
			if v := q.Get("tree"); v != "" && spec.Key.Tree != v {
				continue
			}
			if snap, err := s.svc.Snapshot(spec.Key); err == nil {
				entries = append(entries, entry{ns, spec, snap})
			}
		}
	}
	if len(entries) == 0 && (q.Get("namespace") != "" || q.Get("tree") != "") {
		http.Error(w, "no synced documentation tree matches the filter", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, "# %s\n\n> Full text of every document. Each document is wrapped in a <document> tag with its source; "+
		"the document index is at %s/llms.txt.\n", s.opts.Title, base)
	for _, e := range entries {
		fmt.Fprintf(w, "\n## %s / %s\n", e.ns.Title, e.spec.Title)
		for _, d := range e.snap.Docs {
			fmt.Fprintf(w, "\n<document namespace=\"%s\" tree=\"%s\" path=\"%s\" title=\"%s\" url=\"%s\">\n",
				html.EscapeString(e.spec.Key.Namespace), html.EscapeString(e.spec.Key.Tree), html.EscapeString(d.Path),
				html.EscapeString(d.Title), html.EscapeString(base+docURL(e.spec.Key, d.Slug)))
			_, _ = w.Write(bytes.TrimRight(d.Content, "\n"))
			_, _ = io.WriteString(w, "\n</document>\n")
		}
	}
}
