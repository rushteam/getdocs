// Package web 提供文档站点（HTML 页面）、REST API 与 git 推送 webhook。
package web

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	texttemplate "text/template"

	"getdocs/internal/application/catalog"
	"getdocs/internal/domain/docs"
	"getdocs/internal/infra/markdown"
)

//go:embed templates/*.html templates/*.md static/*
var assets embed.FS

// Options 是 Web 层配置。
type Options struct {
	Title         string
	BaseURL       string // 对外访问地址，为空时按请求推导
	AdminToken    string // 保护同步等写操作，为空表示不鉴权
	WebhookSecret string // 校验 git 平台推送，为空时退回 AdminToken
	MermaidURL    string // mermaid ESM 地址，为空使用默认 CDN
	// MCP 是挂载在 /mcp 的 MCP 处理器，为 nil 时不提供；MCPToken 非空时要求 Bearer 鉴权。
	MCP      http.Handler
	MCPToken string
	// Lifecycle 是服务生命周期 ctx：后台同步绑定到它，而不是某次请求。
	Lifecycle context.Context
	Logger    *slog.Logger
	// AllowedHosts 限制页面登记的仓库主机；为空时允许公网主机，内网地址需显式列出。
	AllowedHosts []string
}

const defaultMermaidURL = "https://cdn.jsdelivr.net/npm/mermaid@11/dist/mermaid.esm.min.mjs"

// Server 处理全部 HTTP 请求。
type Server struct {
	svc      *catalog.Service
	searcher docs.Searcher // 可为 nil
	md       *markdown.Engine
	opts     Options
	log      *slog.Logger

	pages      map[string]*template.Template
	guide      *texttemplate.Template // /ai 接入说明（markdown）
	cache      *renderCache
	staticVer  string
	chromaCSS  []byte
	cspHeader  string
	staticRoot fs.FS
}

// New 创建 Web 服务。
func New(svc *catalog.Service, searcher docs.Searcher, md *markdown.Engine, opts Options) (*Server, error) {
	if opts.Lifecycle == nil {
		return nil, errors.New("web: lifecycle context is required")
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.MermaidURL == "" {
		opts.MermaidURL = defaultMermaidURL
	}
	s := &Server{svc: svc, searcher: searcher, md: md, opts: opts, log: opts.Logger, cache: newRenderCache(2000)}

	css, err := markdown.HighlightCSS("github", "github-dark")
	if err != nil {
		return nil, fmt.Errorf("highlight css: %w", err)
	}
	s.chromaCSS = []byte(css)
	if s.staticRoot, err = fs.Sub(assets, "static"); err != nil {
		return nil, err
	}
	s.staticVer = assetsVersion(s.chromaCSS)
	if err := s.parseTemplates(); err != nil {
		return nil, err
	}
	if err := s.parseGuide(); err != nil {
		return nil, err
	}
	scriptSrc := "'self'"
	if origin := originOf(opts.MermaidURL); origin != "" {
		scriptSrc += " " + origin
	}
	// 文档常引用外部图片（徽章等），img-src 放开；mermaid 生成的 SVG 需要内联样式。
	s.cspHeader = "default-src 'self'; script-src " + scriptSrc + "; style-src 'self' 'unsafe-inline'; " +
		"img-src * data:; media-src *; object-src 'none'; base-uri 'self'; frame-ancestors 'self'"
	return s, nil
}

func assetsVersion(extra []byte) string {
	h := sha256.New()
	_ = fs.WalkDir(assets, "static", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			b, _ := assets.ReadFile(p)
			h.Write(b)
		}
		return nil
	})
	h.Write(extra)
	return hex.EncodeToString(h.Sum(nil))[:10]
}

func originOf(u string) string {
	if i := strings.Index(u, "://"); i > 0 {
		if j := strings.Index(u[i+3:], "/"); j > 0 {
			return u[:i+3+j]
		}
		return u
	}
	return ""
}

// Handler 返回挂载了全部路由的处理器。panic 兜底、访问日志、请求 ID、健康检查与压缩
// 是进程级的横切能力，由组合根在 HTTP 服务上以中间件统一提供，不在这里重复。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /{$}", s.home)
	mux.HandleFunc("GET /docs/{ns}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, r.URL.Path+"/", http.StatusMovedPermanently)
	})
	mux.HandleFunc("GET /docs/{ns}/{$}", s.namespacePage)
	mux.HandleFunc("GET /docs/{ns}/{tree}/{path...}", s.docPage)
	mux.HandleFunc("GET /assets/{ns}/{tree}/{path...}", s.asset)
	mux.HandleFunc("GET /search", s.searchPage)
	mux.HandleFunc("GET /status", s.statusPage)
	mux.HandleFunc("GET /status/{ns}/{tree}", s.treeStatusPage)
	mux.HandleFunc("GET /llms.txt", s.llmsTxt)
	mux.HandleFunc("GET /llms-full.txt", s.llmsFullTxt)
	mux.HandleFunc("GET /ai", s.aiPage)
	mux.HandleFunc("GET /ai.md", s.aiMarkdown)
	mux.HandleFunc("GET /static/chroma.css", s.chroma)
	mux.Handle("GET /static/", http.StripPrefix("/static/", staticCache(http.FileServerFS(s.staticRoot))))

	s.apiRoutes(mux)

	if s.opts.MCP != nil {
		mux.Handle("/mcp", s.bearer(s.opts.MCPToken, s.opts.MCP))
	}

	// 浏览器跨站发起的写请求一律拒绝；webhook 与 MCP 客户端不携带跨站标头，不受影响。
	cop := http.NewCrossOriginProtection()
	return cop.Handler(mux)
}

func staticCache(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("v") != "" {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		h.ServeHTTP(w, r)
	})
}

func (s *Server) chroma(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	_, _ = w.Write(s.chromaCSS)
}

// ---- 鉴权 ----

// authorized 校验管理令牌：未配置令牌时放行。
func (s *Server) authorized(r *http.Request) bool {
	return s.opts.AdminToken == "" || tokenMatches(r, s.opts.AdminToken)
}

func tokenMatches(r *http.Request, want string) bool {
	got := r.Header.Get("X-Admin-Token")
	if v, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		got = v
	}
	return got != "" && subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func (s *Server) bearer(token string, h http.Handler) http.Handler {
	if token == "" {
		return h
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !tokenMatches(r, token) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="getdocs"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// ---- 工具函数 ----

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func errStatus(err error) int {
	switch {
	case errors.Is(err, docs.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, docs.ErrSyncInProgress):
		return http.StatusConflict
	case errors.Is(err, docs.ErrConflict):
		return http.StatusConflict
	case errors.Is(err, errBadRequest), errors.Is(err, docs.ErrInvalid):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

var errBadRequest = errors.New("bad request")

func (s *Server) apiError(w http.ResponseWriter, r *http.Request, err error) {
	status := errStatus(err)
	msg := err.Error()
	if status == http.StatusInternalServerError {
		s.log.ErrorContext(r.Context(), "api error", "path", r.URL.Path, "err", err)
		msg = "internal server error"
	}
	writeJSON(w, status, map[string]string{"error": msg})
}

func treeKey(r *http.Request) docs.TreeKey {
	return docs.TreeKey{Namespace: r.PathValue("ns"), Tree: r.PathValue("tree")}
}

var versionRe = regexp.MustCompile(`^[0-9A-Za-z_-]{1,64}$`)

// snapshotFor 按 ?v= 参数返回快照，未指定时为当前版本。
func (s *Server) snapshotFor(r *http.Request, key docs.TreeKey) (*docs.Snapshot, error) {
	v := r.URL.Query().Get("v")
	if v != "" && !versionRe.MatchString(v) {
		return nil, fmt.Errorf("invalid version %q: %w", v, errBadRequest)
	}
	return s.svc.SnapshotAt(r.Context(), key, v)
}

func (s *Server) baseURL(r *http.Request) string {
	if s.opts.BaseURL != "" {
		return s.opts.BaseURL
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}
