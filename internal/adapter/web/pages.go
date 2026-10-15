package web

import (
	"bytes"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/rushteam/getdocs/internal/domain/docs"
)

// page 是所有页面模板的公共数据。
type page struct {
	Site      string
	Title     string
	StaticVer string
	Search    bool
	Query     string
	Scope     string // 搜索范围 "ns/tree"
	Mermaid   string // 页面包含 mermaid 图表时为脚本地址
	Wide      bool   // 文档页使用三栏布局
	Alternate string // 页面的 markdown 版本地址，供 agent 发现
	Body      any
}

func (s *Server) parseTemplates() error {
	funcs := template.FuncMap{
		"ago":           ago,
		"datetime":      func(t time.Time) string { return t.Local().Format("2006-01-02 15:04:05") },
		"short":         func(c string) string { return docs.Revision{Commit: c}.ShortCommit() },
		"providerTitle": func(p string) string { return docs.Provider(p).Title() },
		"providerName":  func(p docs.Provider) string { return p.Title() },
		"treeURL":       treeURL,
		"docURL":        docURL,
		"pathEsc":       escapePath,
		"duration":      func(r docs.SyncRun) string { return r.Duration().Round(time.Millisecond).String() },
		"changeStr":     func(c docs.ChangeSet) string { return c.String() },
	}
	layout, err := template.New("layout.html").Funcs(funcs).ParseFS(assets, "templates/layout.html")
	if err != nil {
		return err
	}
	names, err := fs.Glob(assets, "templates/*.html")
	if err != nil {
		return err
	}
	s.pages = map[string]*template.Template{}
	for _, n := range names {
		base := path.Base(n)
		if base == "layout.html" {
			continue
		}
		t, err := template.Must(layout.Clone()).ParseFS(assets, n)
		if err != nil {
			return fmt.Errorf("parse %s: %w", n, err)
		}
		s.pages[strings.TrimSuffix(base, ".html")] = t
	}
	return nil
}

func (s *Server) html(w http.ResponseWriter, r *http.Request, status int, name string, p page) {
	p.Site, p.StaticVer, p.Search = s.opts.Title, s.staticVer, s.searcher != nil
	if p.Title == "" {
		p.Title = s.opts.Title
	} else {
		p.Title += " · " + s.opts.Title
	}
	var buf bytes.Buffer
	if err := s.pages[name].ExecuteTemplate(&buf, "layout.html", p); err != nil {
		s.log.ErrorContext(r.Context(), "render template failed", "template", name, "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Security-Policy", s.cspHeader)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

type messageBody struct {
	Heading string
	Text    string
	Tree    *treeView // 未同步的文档树：展示状态与同步按钮
}

func (s *Server) errorPage(w http.ResponseWriter, r *http.Request, err error) {
	status := errStatus(err)
	body := messageBody{Heading: "出错了", Text: err.Error()}
	switch status {
	case http.StatusNotFound:
		body.Heading = "页面不存在"
	case http.StatusInternalServerError:
		s.log.ErrorContext(r.Context(), "page error", "path", r.URL.Path, "err", err)
		body.Text = "服务器内部错误，请稍后重试。"
	}
	s.html(w, r, status, "message", page{Title: body.Heading, Body: body})
}

// ---- 首页与命名空间 ----

type nsView struct {
	Name        string     `json:"name"`
	Title       string     `json:"title"`
	Description string     `json:"description,omitempty"`
	Trees       []treeView `json:"trees"`
}

func (s *Server) namespaceViews(r *http.Request, only string) ([]nsView, error) {
	var out []nsView
	for _, ns := range s.svc.Namespaces() {
		if only != "" && ns.Name != only {
			continue
		}
		v := nsView{Name: ns.Name, Title: ns.Title, Description: ns.Description}
		for _, t := range ns.Trees {
			st, err := s.svc.Status(r.Context(), t.Key)
			if err != nil {
				return nil, err
			}
			tv := newTreeView(st)
			tv.Registered = s.svc.Registered(t.Key)
			v.Trees = append(v.Trees, tv)
		}
		out = append(out, v)
	}
	return out, nil
}

func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	nss, err := s.namespaceViews(r, "")
	if err != nil {
		s.errorPage(w, r, err)
		return
	}
	s.html(w, r, http.StatusOK, "home", page{Body: nss})
}

func (s *Server) namespacePage(w http.ResponseWriter, r *http.Request) {
	ns, err := s.svc.Namespace(r.PathValue("ns"))
	if err != nil {
		s.errorPage(w, r, err)
		return
	}
	nss, err := s.namespaceViews(r, ns.Name)
	if err != nil {
		s.errorPage(w, r, err)
		return
	}
	s.html(w, r, http.StatusOK, "home", page{Title: ns.Title, Body: nss})
}

// ---- 文档页 ----

type docLink struct{ Title, URL string }

type docBody struct {
	NS        nsView
	Tree      treeView
	Trees     []docLink // 同一命名空间下的其他文档树
	Nav       []*navItem
	Doc       *docs.Document
	Crumbs    []docLink
	HTML      template.HTML
	TOC       []docs.Heading
	Prev      *docLink
	Next      *docLink
	SourceURL string
	RawURL    string // markdown 原文
	Snapshot  *docs.Snapshot
	Pinned    bool
	LatestURL string
}

func (s *Server) docPage(w http.ResponseWriter, r *http.Request) {
	key := treeKey(r)
	spec, err := s.svc.Spec(key)
	if err != nil {
		s.errorPage(w, r, err)
		return
	}
	snap, err := s.snapshotFor(r, key)
	if errors.Is(err, docs.ErrNotFound) && r.URL.Query().Get("v") == "" {
		st, _ := s.svc.Status(r.Context(), key)
		tv := newTreeView(st)
		s.html(w, r, http.StatusNotFound, "message", page{Title: spec.Title, Body: messageBody{
			Heading: spec.Title, Text: "该文档树还没有同步成功，同步完成后即可浏览。", Tree: &tv,
		}})
		return
	}
	if err != nil {
		s.errorPage(w, r, err)
		return
	}
	pinned := r.URL.Query().Get("v") != ""
	query := ""
	if pinned {
		query = "?v=" + snap.Version
	}

	ref := r.PathValue("path")
	d := snap.Lookup(ref)
	if d == nil {
		if ref == "" && len(snap.Docs) > 0 {
			// 根目录没有 README / index 时进入阅读顺序中的第一篇。
			http.Redirect(w, r, docURL(key, snap.Docs[0].Slug)+query, http.StatusFound)
			return
		}
		if ref == "" {
			s.html(w, r, http.StatusOK, "message", page{Title: spec.Title, Body: messageBody{
				Heading: spec.Title, Text: "文档根目录 " + snap.Root + " 中没有找到 markdown 文档，请检查 root / include / exclude 配置。",
			}})
			return
		}
		s.errorPage(w, r, fmt.Errorf("document %q: %w", ref, docs.ErrNotFound))
		return
	}
	// 页面地址也可以直接交给 agent：声明 Accept: text/markdown 时返回原文。
	w.Header().Set("Vary", "Accept")
	if acceptsMarkdown(r) {
		writeMarkdown(w, d)
		return
	}
	// 通过路径（如 guide/README.md）访问时跳转到规范地址，保证页面内相对锚点与导航高亮一致。
	if strings.Trim(ref, "/") != d.Slug {
		http.Redirect(w, r, withFragment(docURL(key, d.Slug)+query, ""), http.StatusMovedPermanently)
		return
	}

	rendered, err := s.render(snap, spec, d, pinned)
	if err != nil {
		s.errorPage(w, r, err)
		return
	}
	st, err := s.svc.Status(r.Context(), key)
	if err != nil {
		s.errorPage(w, r, err)
		return
	}
	ns, _ := s.svc.Namespace(key.Namespace)
	body := docBody{
		NS:        nsView{Name: ns.Name, Title: ns.Title},
		Tree:      newTreeView(st),
		Nav:       buildNav(snap, d.Slug, query),
		Doc:       d,
		HTML:      template.HTML(rendered.HTML), // 已经过 bluemonday 清洗
		SourceURL: spec.Source.FileURL(snap.Revision, snap.RepoPath(d.Path)),
		RawURL:    rawURL(key, d.Path) + strings.Replace(query, "?", "&", 1),
		Snapshot:  snap,
		Pinned:    pinned,
		LatestURL: docURL(key, d.Slug),
	}
	for _, t := range ns.Trees {
		if t.Key != key {
			body.Trees = append(body.Trees, docLink{Title: t.Title, URL: treeURL(t.Key)})
		}
	}
	for _, h := range rendered.TOC {
		if h.Level >= 2 && h.Level <= 4 {
			body.TOC = append(body.TOC, h)
		}
	}
	body.Crumbs = crumbs(snap, d, query)
	if prev, next := snap.Neighbors(d); prev != nil || next != nil {
		if prev != nil {
			body.Prev = &docLink{Title: prev.Title, URL: docURL(key, prev.Slug) + query}
		}
		if next != nil {
			body.Next = &docLink{Title: next.Title, URL: docURL(key, next.Slug) + query}
		}
	}
	p := page{Title: d.Title + " - " + spec.Title, Scope: key.String(), Wide: true, Alternate: body.RawURL, Body: body}
	if strings.Contains(rendered.HTML, `class="mermaid"`) {
		p.Mermaid = s.opts.MermaidURL
	}
	s.html(w, r, http.StatusOK, "doc", p)
}

// crumbs 生成文档所在目录的面包屑，目录有首页时可点击。
func crumbs(snap *docs.Snapshot, d *docs.Document, query string) []docLink {
	var out []docLink
	n := snap.Nav()
	for _, part := range strings.Split(path.Dir(d.Path), "/") {
		if part == "." {
			break
		}
		idx := slices.IndexFunc(n.Children, func(c *docs.NavNode) bool { return c.IsDir && c.Name == part })
		if idx < 0 {
			break
		}
		n = n.Children[idx]
		if n.Doc() == d {
			break
		}
		l := docLink{Title: n.Title}
		if n.Doc() != nil {
			l.URL = docURL(snap.Key, n.Slug) + query
		}
		out = append(out, l)
	}
	return out
}

// ---- 资源 ----

func (s *Server) asset(w http.ResponseWriter, r *http.Request) {
	key := treeKey(r)
	snap, err := s.snapshotFor(r, key)
	if err != nil {
		http.Error(w, err.Error(), errStatus(err))
		return
	}
	repoPath := r.PathValue("path")
	a, ok := snap.AssetByPath(repoPath)
	if !ok {
		http.NotFound(w, r)
		return
	}
	data, err := s.svc.Asset(r.Context(), snap, repoPath)
	if err != nil {
		http.Error(w, "asset unavailable", errStatus(err))
		return
	}
	h := w.Header()
	ctype := mime.TypeByExtension(strings.ToLower(path.Ext(repoPath)))
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	h.Set("Content-Type", ctype)
	h.Set("ETag", `"`+a.Hash+`"`)
	h.Set("X-Content-Type-Options", "nosniff")
	// 资源来自仓库，可能是包含脚本的 SVG：禁止其执行脚本或访问本站上下文。
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; img-src data:; sandbox")
	if r.URL.Query().Get("v") != "" {
		h.Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		h.Set("Cache-Control", "public, max-age=300")
	}
	http.ServeContent(w, r, path.Base(repoPath), snap.BuiltAt, bytes.NewReader(data))
}

// ---- 搜索 ----

type searchHit struct {
	docs.SearchHit
	URL       string
	TreeTitle string
}

type searchBody struct {
	Query   string
	Scope   string
	Scopes  []docLink // Title 为展示名，URL 为 "ns/tree" 或 "ns"
	Hits    []searchHit
	Error   string
	Enabled bool
}

func (s *Server) searchPage(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	scope := r.URL.Query().Get("scope")
	body := searchBody{Query: q, Scope: scope, Enabled: s.searcher != nil}
	for _, ns := range s.svc.Namespaces() {
		body.Scopes = append(body.Scopes, docLink{Title: ns.Title, URL: ns.Name})
		for _, t := range ns.Trees {
			body.Scopes = append(body.Scopes, docLink{Title: ns.Title + " / " + t.Title, URL: t.Key.String()})
		}
	}
	if q != "" && s.searcher != nil {
		sq := docs.SearchQuery{Text: q, Limit: 30}
		sq.Namespace, sq.Tree, _ = strings.Cut(scope, "/")
		hits, err := s.searcher.Search(r.Context(), sq)
		if err != nil {
			body.Error = err.Error()
		}
		for _, h := range hits {
			title := h.Key.Tree
			if spec, err := s.svc.Spec(h.Key); err == nil {
				title = spec.Title
			}
			body.Hits = append(body.Hits, searchHit{SearchHit: h, TreeTitle: title,
				URL: withFragment(docURL(h.Key, h.Slug), h.Anchor)})
		}
	}
	title := "搜索"
	if q != "" {
		title = q + " - 搜索"
	}
	s.html(w, r, http.StatusOK, "search", page{Title: title, Query: q, Scope: scope, Body: body})
}

// ---- 同步状态 ----

func (s *Server) statusPage(w http.ResponseWriter, r *http.Request) {
	nss, err := s.namespaceViews(r, "")
	if err != nil {
		s.errorPage(w, r, err)
		return
	}
	s.html(w, r, http.StatusOK, "status", page{Title: "同步状态", Body: statusBody{
		Namespaces: nss, AuthNeeded: s.opts.AdminToken != "", CanRegister: s.canRegister(),
		Credentials: s.svc.Credentials(), Providers: docs.Providers,
	}})
}

type statusBody struct {
	Namespaces  []nsView
	AuthNeeded  bool
	CanRegister bool
	Credentials []string
	Providers   []docs.Provider
}

type treeStatusBody struct {
	Tree       treeView
	Spec       *docs.TreeSpec
	Runs       []docs.SyncRun
	Versions   []string
	Current    string
	AuthNeeded bool
	Webhook    string
	Removable  bool
}

func (s *Server) treeStatusPage(w http.ResponseWriter, r *http.Request) {
	key := treeKey(r)
	st, err := s.svc.Status(r.Context(), key)
	if err != nil {
		s.errorPage(w, r, err)
		return
	}
	runs, err := s.svc.Runs(r.Context(), key, 50)
	if err != nil {
		s.errorPage(w, r, err)
		return
	}
	versions, err := s.svc.Versions(r.Context(), key)
	if err != nil && !errors.Is(err, docs.ErrNotFound) {
		s.errorPage(w, r, err)
		return
	}
	body := treeStatusBody{
		Tree: newTreeView(st), Spec: st.Spec, Runs: runs, Versions: versions, AuthNeeded: s.opts.AdminToken != "",
		Webhook:   s.baseURL(r) + "/api/v1/webhooks/" + escapePath(key.String()),
		Removable: s.canRegister() && s.svc.Registered(key),
	}
	if st.Snapshot != nil {
		body.Current = st.Snapshot.Version
	}
	if st.Running != nil {
		body.Runs = append([]docs.SyncRun{*st.Running}, body.Runs...)
	}
	s.html(w, r, http.StatusOK, "tree_status", page{Title: st.Spec.Title + " - 同步状态", Body: body})
}

func ago(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "刚刚"
	case d < time.Hour:
		return fmt.Sprintf("%d 分钟前", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d 小时前", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%d 天前", int(d.Hours()/24))
	default:
		return t.Local().Format("2006-01-02")
	}
}
