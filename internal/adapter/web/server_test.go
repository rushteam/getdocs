package web

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rushteam/getdocs/internal/application/catalog"
	"github.com/rushteam/getdocs/internal/application/search"
	"github.com/rushteam/getdocs/internal/domain/docs"
	"github.com/rushteam/getdocs/internal/infra/git"
	"github.com/rushteam/getdocs/internal/infra/markdown"
	"github.com/rushteam/getdocs/internal/infra/protodoc"
	"github.com/rushteam/getdocs/internal/infra/storage"
)

func TestNormalizeRepo(t *testing.T) {
	want := "github.com/org/repo"
	for _, u := range []string{
		"https://github.com/org/repo.git",
		"https://user:token@github.com/org/repo",
		"http://GitHub.com/Org/Repo/",
		"git@github.com:org/repo.git",
		"ssh://git@github.com/org/repo.git",
		"ssh://git@github.com:22/org/repo.git",
	} {
		if got := normalizeRepo(u); got != want {
			t.Errorf("normalizeRepo(%q) = %q, want %q", u, got, want)
		}
	}
	if !repoMatches("git@github.com:org/repo.git", []string{"", "https://github.com/org/repo"}) {
		t.Errorf("repoMatches should match equivalent URLs")
	}
	if repoMatches("https://github.com/org/repo", []string{"https://github.com/org/repo2"}) || repoMatches("", []string{""}) {
		t.Errorf("repoMatches matched different repositories")
	}
}

func TestBranchMismatch(t *testing.T) {
	ev := func(ref, def string) pushEvent {
		var e pushEvent
		e.Ref = ref
		e.Repository.DefaultBranch = def
		return e
	}
	spec := &docs.TreeSpec{Source: docs.Source{Branch: "main"}}
	if r := branchMismatch(spec, ev("refs/heads/main", "")); r != "" {
		t.Errorf("tracked branch skipped: %s", r)
	}
	if r := branchMismatch(spec, ev("refs/heads/dev", "")); r == "" {
		t.Errorf("untracked branch not skipped")
	}
	if r := branchMismatch(spec, ev("refs/tags/v1", "")); r == "" {
		t.Errorf("tag push not skipped")
	}
	if r := branchMismatch(spec, ev("", "")); r != "" {
		t.Errorf("generic trigger skipped: %s", r)
	}
	noBranch := &docs.TreeSpec{}
	if r := branchMismatch(noBranch, ev("refs/heads/dev", "main")); r == "" {
		t.Errorf("non-default branch should be skipped when tree follows default branch")
	}
	if r := branchMismatch(noBranch, ev("refs/heads/dev", "")); r != "" {
		t.Errorf("unknown default branch should not skip: %s", r)
	}
}

func TestVerifyWebhook(t *testing.T) {
	s := &Server{opts: Options{WebhookSecret: "s3cret", AdminToken: "admin"}}
	body := []byte(`{"ref":"refs/heads/main"}`)
	mac := hmac.New(sha256.New, []byte("s3cret"))
	mac.Write(body)
	sig := hex.EncodeToString(mac.Sum(nil))

	giteeSign := func(ts string) string {
		m := hmac.New(sha256.New, []byte("s3cret"))
		m.Write([]byte(ts + "\n" + "s3cret"))
		return base64.StdEncoding.EncodeToString(m.Sum(nil))
	}
	now := strconv.FormatInt(time.Now().UnixMilli(), 10)
	stale := strconv.FormatInt(time.Now().Add(-2*time.Hour).UnixMilli(), 10)

	req := func(h map[string]string, query string) *http.Request {
		r := httptest.NewRequest("POST", "/api/v1/webhooks"+query, nil)
		for k, v := range h {
			r.Header.Set(k, v)
		}
		return r
	}
	cases := []struct {
		name string
		r    *http.Request
		want bool
	}{
		{"github", req(map[string]string{"X-Hub-Signature-256": "sha256=" + sig}, ""), true},
		{"gitea", req(map[string]string{"X-Gitea-Signature": sig}, ""), true},
		{"bad signature", req(map[string]string{"X-Hub-Signature-256": "sha256=00"}, ""), false},
		{"bad signature ignores token", req(map[string]string{"X-Hub-Signature-256": "sha256=00"}, "?token=s3cret"), false},
		{"gitlab", req(map[string]string{"X-Gitlab-Token": "s3cret"}, ""), true},
		{"gitlab wrong", req(map[string]string{"X-Gitlab-Token": "nope"}, ""), false},
		{"query token", req(nil, "?token=s3cret"), true},
		{"bearer", req(map[string]string{"Authorization": "Bearer s3cret"}, ""), true},
		{"admin token is not the webhook secret", req(map[string]string{"Authorization": "Bearer admin"}, ""), false},
		{"gitee password", req(map[string]string{"X-Gitee-Token": "s3cret"}, ""), true},
		{"gitee signature", req(map[string]string{"X-Gitee-Token": url.QueryEscape(giteeSign(now)), "X-Gitee-Timestamp": now}, ""), true},
		{"gitee stale signature", req(map[string]string{"X-Gitee-Token": giteeSign(stale), "X-Gitee-Timestamp": stale}, ""), false},
		{"gitee wrong", req(map[string]string{"X-Gitee-Token": "nope", "X-Gitee-Timestamp": now}, ""), false},
		{"none", req(nil, ""), false},
	}
	for _, c := range cases {
		if got := s.verifyWebhook(c.r, body); got != c.want {
			t.Errorf("%s: verifyWebhook = %v, want %v", c.name, got, c.want)
		}
	}

	open := &Server{}
	if !open.verifyWebhook(req(nil, ""), body) {
		t.Errorf("webhook without any secret should be accepted")
	}
	fallback := &Server{opts: Options{AdminToken: "admin"}}
	if !fallback.verifyWebhook(req(map[string]string{"X-Gitlab-Token": "admin"}, ""), body) {
		t.Errorf("webhook secret should fall back to admin token")
	}
}

func newTestServer(t *testing.T) (http.Handler, *catalog.Service) {
	t.Helper()
	return newServerWith(t, map[string]string{
		"README.md":        "# Home\n\n![logo](logo.svg) [install](guide/install.md) <script>alert(1)</script>\n",
		"guide/install.md": "# Install\n\n| a | b |\n|:--|--:|\n| 1 | 2 |\n",
		"logo.svg":         `<svg xmlns="http://www.w3.org/2000/svg"/>`,
	}, docs.Filter{})
}

// newServerWith 以本地目录 files 作为 demo/local 文档树启动服务并完成首次同步。
func newServerWith(t *testing.T, files map[string]string, filter docs.Filter) (http.Handler, *catalog.Service) {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	for p, c := range files {
		p = filepath.Join(src, p)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store, err := storage.NewFS(filepath.Join(dir, "data"))
	if err != nil {
		t.Fatal(err)
	}
	fetcher, err := git.NewFetcher(filepath.Join(dir, "repos"), 0, logger)
	if err != nil {
		t.Fatal(err)
	}
	svc := catalog.New(catalog.Deps{Fetcher: fetcher, Parser: protodoc.NewParser(markdown.New()), Snapshots: store, States: store, Logger: logger,
		Registry: storage.NewFileRegistry(filepath.Join(dir, "registry.json"))},
		catalog.Options{}, []*docs.Namespace{{Name: "demo", Title: "Demo", Trees: []*docs.TreeSpec{
			{Key: docs.TreeKey{Namespace: "demo", Tree: "local"}, Title: "Local", Source: docs.Source{Kind: docs.SourceLocal, URL: src}, Filter: filter},
		}}})
	ix := search.NewIndex()
	svc.AddHook(ix)
	if _, err := svc.Sync(t.Context(), docs.TreeKey{Namespace: "demo", Tree: "local"}, docs.TriggerManual, false); err != nil {
		t.Fatal(err)
	}
	s, err := New(svc, ix, markdown.New(), Options{Title: "Docs", AdminToken: "admin", Lifecycle: t.Context(), Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	return s.Handler(), svc
}

func do(h http.Handler, method, target string, header map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, nil)
	for k, v := range header {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestPages(t *testing.T) {
	h, _ := newTestServer(t)
	cases := []struct {
		target   string
		status   int
		contains string
	}{
		{"/", 200, "Demo"},
		{"/docs/demo", 301, ""},
		{"/docs/demo/", 200, "Local"},
		{"/docs/demo/local/", 200, "Home"},
		{"/docs/demo/local/guide/install", 200, `align="right"`},
		{"/docs/demo/local/guide/install.md", 301, ""},
		{"/docs/demo/local/missing", 404, ""},
		{"/docs/demo/nope/", 404, ""},
		{"/search?q=install", 200, "guide/install"},
		{"/status", 200, "local"},
		{"/status/demo/local", 200, "success"},
		{"/llms.txt", 200, "/api/v1/trees/demo/local/docs/guide/install.md?format=raw"},
		{"/llms.txt", 200, "/llms-full.txt"},
		{"/llms-full.txt", 200, `<document namespace="demo" tree="local" path="guide/install.md" title="Install"`},
		{"/llms-full.txt?namespace=demo&tree=local", 200, "# Install"},
		{"/llms-full.txt?namespace=nope", 404, ""},
		{"/ai", 200, "/api/v1/trees/demo/local/docs/README.md?format=raw"},
		{"/ai.md", 200, "curl 'http://example.com/api/v1/trees/demo/local/docs/README.md?format=raw'"},
		{"/", 200, `href="/llms.txt" target="_blank" rel="noopener"`},
		{"/ai", 200, `href="/ai.md" type="text/markdown" target="_blank" rel="noopener"`},
		{"/docs/demo/local/guide/install", 200, `href="/api/v1/trees/demo/local/docs/guide/install.md?format=raw" type="text/markdown" target="_blank" rel="noopener"`},
		{"/docs/demo/local/guide/install", 200, `<link rel="alternate" type="text/markdown"`},
		{"/static/app.css", 200, ""},
		{"/static/chroma.css", 200, ".chroma"},
	}
	for _, c := range cases {
		w := do(h, "GET", c.target, nil)
		if w.Code != c.status {
			t.Errorf("GET %s = %d, want %d", c.target, w.Code, c.status)
			continue
		}
		if c.contains != "" && !strings.Contains(w.Body.String(), c.contains) {
			t.Errorf("GET %s body missing %q", c.target, c.contains)
		}
	}

	home := do(h, "GET", "/docs/demo/local/", nil).Body.String()
	if strings.Contains(home, "<script>alert(1)") {
		t.Errorf("unsanitized script rendered")
	}
	if !strings.Contains(home, `href="/docs/demo/local/guide/install"`) {
		t.Errorf("relative doc link not rewritten")
	}
	if !strings.Contains(home, "/assets/demo/local/logo.svg?v=") {
		t.Errorf("image not rewritten to asset URL")
	}

	asset := do(h, "GET", "/assets/demo/local/logo.svg", nil)
	if asset.Code != 200 || !strings.Contains(asset.Header().Get("Content-Security-Policy"), "sandbox") {
		t.Errorf("asset = %d, CSP %q", asset.Code, asset.Header().Get("Content-Security-Policy"))
	}
}

// 页面控件全部自绘：不渲染原生 select，脚本不调用浏览器原生对话框。
func TestNoNativeControls(t *testing.T) {
	h, _ := newTestServer(t)
	page := do(h, "GET", "/search?q=install&scope=demo/local", nil).Body.String()
	if strings.Contains(page, "<select") {
		t.Error("search page renders a native <select>")
	}
	for _, want := range []string{`name="scope" value="demo/local"`, `data-value="demo/local" aria-selected="true"`, `data-value="" aria-selected="false"`} {
		if !strings.Contains(page, want) {
			t.Errorf("search page missing %q", want)
		}
	}
	if page := do(h, "GET", "/search?q=install&scope=nope", nil).Body.String(); !strings.Contains(page, `name="scope" value=""`) {
		t.Error("unknown scope should fall back to all documents")
	}
	js := do(h, "GET", "/static/app.js", nil).Body.String()
	for _, banned := range []string{"prompt(", "alert(", "confirm("} {
		if strings.Contains(js, banned) {
			t.Errorf("app.js calls native %s", banned)
		}
	}
	font := do(h, "GET", "/static/fonts/Geist-Variable.woff2", nil)
	if font.Code != 200 || font.Header().Get("Content-Type") != "font/woff2" {
		t.Errorf("font = %d %q", font.Code, font.Header().Get("Content-Type"))
	}
}

func TestMarkdownNegotiation(t *testing.T) {
	h, _ := newTestServer(t)
	cases := []struct {
		target, accept string
		markdown       bool
	}{
		{"/docs/demo/local/guide/install", "text/markdown", true},
		{"/docs/demo/local/guide/install.md", "text/markdown, text/html;q=0.5", true},
		{"/docs/demo/local/guide/install", "text/html,application/xhtml+xml,*/*;q=0.8", false},
		{"/docs/demo/local/guide/install", "text/html, text/markdown;q=0.5", false},
		{"/ai", "text/markdown", true},
	}
	for _, c := range cases {
		w := do(h, "GET", c.target, map[string]string{"Accept": c.accept})
		got := strings.HasPrefix(w.Header().Get("Content-Type"), "text/markdown")
		if w.Code != 200 || got != c.markdown {
			t.Errorf("GET %s Accept %q = %d %s, want markdown %v", c.target, c.accept, w.Code, w.Header().Get("Content-Type"), c.markdown)
		}
		if w.Header().Get("Vary") != "Accept" {
			t.Errorf("GET %s missing Vary: Accept", c.target)
		}
	}
	if w := do(h, "GET", "/docs/demo/local/guide/install", map[string]string{"Accept": "text/markdown"}); !strings.HasPrefix(w.Body.String(), "# Install") {
		t.Errorf("negotiated markdown = %q", w.Body)
	}
}

func TestAPI(t *testing.T) {
	h, _ := newTestServer(t)

	w := do(h, "GET", "/api/v1/namespaces", nil)
	var ns struct {
		Namespaces []nsView `json:"namespaces"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &ns); err != nil || len(ns.Namespaces) != 1 || ns.Namespaces[0].Name != "demo" {
		t.Errorf("namespaces = %s", w.Body)
	}
	if w := do(h, "GET", "/api/v1/trees/demo/local/docs/guide/install.md?format=raw", nil); w.Code != 200 || !strings.HasPrefix(w.Body.String(), "# Install") {
		t.Errorf("raw doc = %d %q", w.Code, w.Body)
	}
	if w := do(h, "GET", "/api/v1/trees/demo/local/chunks?path=guide/install", nil); w.Code != 200 || strings.Count(w.Body.String(), `"id"`) != 1 {
		t.Errorf("chunks = %d %s", w.Code, w.Body)
	}
	var tree struct {
		Version string `json:"version"`
	}
	_ = json.Unmarshal(do(h, "GET", "/api/v1/trees/demo/local", nil).Body.Bytes(), &tree)
	if w := do(h, "GET", "/api/v1/trees/demo/local/chunks?since="+tree.Version, nil); w.Code != 200 || !strings.Contains(w.Body.String(), `"upsert":[]`) {
		t.Errorf("chunk delta against current version = %d %s", w.Code, w.Body)
	}
	if w := do(h, "GET", "/api/v1/trees/demo/local/chunks?since=../x", nil); w.Code != 400 {
		t.Errorf("invalid since = %d", w.Code)
	}
	if w := do(h, "GET", "/api/v1/search?q=install", nil); w.Code != 200 || !strings.Contains(w.Body.String(), "guide/install.md") {
		t.Errorf("search = %d %s", w.Code, w.Body)
	}
	if w := do(h, "GET", "/api/v1/trees/demo/nope", nil); w.Code != 404 {
		t.Errorf("missing tree = %d", w.Code)
	}

	if w := do(h, "POST", "/api/v1/trees/demo/local/sync", nil); w.Code != 401 {
		t.Errorf("unauthenticated sync = %d", w.Code)
	}
	w = do(h, "POST", "/api/v1/trees/demo/local/sync?wait=true", map[string]string{"Authorization": "Bearer admin"})
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"unchanged"`) {
		t.Errorf("sync = %d %s", w.Code, w.Body)
	}
	// 浏览器跨站发起的写请求被拒绝。
	if w := do(h, "POST", "/api/v1/sync", map[string]string{"Authorization": "Bearer admin", "Sec-Fetch-Site": "cross-site"}); w.Code != 403 {
		t.Errorf("cross-site sync = %d", w.Code)
	}
}

func TestProtoDocs(t *testing.T) {
	src, err := os.ReadFile("../../infra/protodoc/testdata/user.proto")
	if err != nil {
		t.Fatal(err)
	}
	h, _ := newServerWith(t, map[string]string{
		"README.md":              "# Home\n\n见 [用户接口](api/user/v1/user.proto#getuser)。\n",
		"api/user/v1/user.proto": string(src),
	}, docs.Filter{Proto: true})

	page := do(h, "GET", "/docs/demo/local/api/user/v1/user", nil)
	if page.Code != 200 {
		t.Fatalf("proto page = %d", page.Code)
	}
	body := page.Body.String()
	for _, want := range []string{
		`<h3 id="userservice">UserService`,
		`<a href="#getuserrequest">`,
		`<code>GET /v1/users/{id}</code>`,
		`<summary>user.proto</summary>`,
		`class="chroma"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("proto page missing %q", want)
		}
	}
	if w := do(h, "GET", "/docs/demo/local/api/user/v1/user.proto", nil); w.Code != 301 {
		t.Errorf("proto path = %d, want redirect to slug", w.Code)
	}
	home := do(h, "GET", "/docs/demo/local/", nil).Body.String()
	if !strings.Contains(home, `href="/docs/demo/local/api/user/v1/user#getuser"`) {
		t.Errorf("link to .proto not rewritten")
	}

	raw := do(h, "GET", "/api/v1/trees/demo/local/docs/api/user/v1/user.proto?format=raw", nil)
	if raw.Code != 200 || !strings.Contains(raw.Body.String(), "| [GetUser](#getuser) |") {
		t.Errorf("raw proto doc = %d %q", raw.Code, raw.Body)
	}
	if w := do(h, "GET", "/api/v1/search?q=CreateUser", nil); !strings.Contains(w.Body.String(), "api/user/v1/user.proto") {
		t.Errorf("proto doc not searchable: %s", w.Body)
	}
	if w := do(h, "GET", "/llms.txt", nil); !strings.Contains(w.Body.String(), "docs/api/user/v1/user.proto?format=raw") {
		t.Errorf("llms.txt missing proto doc")
	}
}
