package web

import (
	"cmp"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"getdocs/internal/domain/docs"
)

func (s *Server) apiRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/namespaces", s.apiNamespaces)
	mux.HandleFunc("GET /api/v1/trees/{ns}/{tree}", s.apiTree)
	mux.HandleFunc("GET /api/v1/trees/{ns}/{tree}/nav", s.apiNav)
	mux.HandleFunc("GET /api/v1/trees/{ns}/{tree}/docs", s.apiDocs)
	mux.HandleFunc("GET /api/v1/trees/{ns}/{tree}/docs/{path...}", s.apiDoc)
	mux.HandleFunc("GET /api/v1/trees/{ns}/{tree}/chunks", s.apiChunks)
	mux.HandleFunc("GET /api/v1/trees/{ns}/{tree}/versions", s.apiVersions)
	mux.HandleFunc("GET /api/v1/trees/{ns}/{tree}/runs", s.apiRuns)
	mux.HandleFunc("POST /api/v1/trees/{ns}/{tree}/sync", s.apiSync)
	mux.HandleFunc("POST /api/v1/sync", s.apiSyncAll)
	mux.HandleFunc("POST /api/v1/trees", s.apiAddTree)
	mux.HandleFunc("DELETE /api/v1/trees/{ns}/{tree}", s.apiRemoveTree)
	mux.HandleFunc("GET /api/v1/search", s.apiSearch)
	mux.HandleFunc("POST /api/v1/webhooks/{ns}/{tree}", s.webhook)
	mux.HandleFunc("POST /api/v1/webhooks", s.webhook)
}

func (s *Server) apiNamespaces(w http.ResponseWriter, r *http.Request) {
	nss, err := s.namespaceViews(r, "")
	if err != nil {
		s.apiError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"namespaces": nss})
}

func (s *Server) apiTree(w http.ResponseWriter, r *http.Request) {
	st, err := s.svc.Status(r.Context(), treeKey(r))
	if err != nil {
		s.apiError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newTreeView(st))
}

func (s *Server) apiNav(w http.ResponseWriter, r *http.Request) {
	snap, err := s.snapshotFor(r, treeKey(r))
	if err != nil {
		s.apiError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"version": snap.Version, "nav": snap.Nav()})
}

type docMeta struct {
	Path     string         `json:"path"`
	Slug     string         `json:"slug"`
	Title    string         `json:"title"`
	Hash     string         `json:"hash"`
	Size     int64          `json:"size"`
	URL      string         `json:"url"`
	Meta     map[string]any `json:"meta,omitempty"`
	Headings []docs.Heading `json:"headings,omitempty"`
}

func (s *Server) docMeta(r *http.Request, snap *docs.Snapshot, d *docs.Document, headings bool) docMeta {
	m := docMeta{Path: d.Path, Slug: d.Slug, Title: d.Title, Hash: d.Hash, Size: d.Size, Meta: d.Meta,
		URL: s.baseURL(r) + docURL(snap.Key, d.Slug)}
	if headings {
		m.Headings = d.Headings
	}
	return m
}

func (s *Server) apiDocs(w http.ResponseWriter, r *http.Request) {
	snap, err := s.snapshotFor(r, treeKey(r))
	if err != nil {
		s.apiError(w, r, err)
		return
	}
	list := make([]docMeta, 0, len(snap.Docs))
	for _, d := range snap.Docs {
		list = append(list, s.docMeta(r, snap, d, false))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"version": snap.Version, "revision": snap.Revision, "root": snap.Root, "docs": list,
	})
}

// apiDoc 返回单篇文档。format=raw 返回 markdown 原文；format=html 附带渲染结果；默认返回 JSON。
func (s *Server) apiDoc(w http.ResponseWriter, r *http.Request) {
	key := treeKey(r)
	spec, err := s.svc.Spec(key)
	if err != nil {
		s.apiError(w, r, err)
		return
	}
	snap, err := s.snapshotFor(r, key)
	if err != nil {
		s.apiError(w, r, err)
		return
	}
	d := snap.Lookup(r.PathValue("path"))
	if d == nil {
		s.apiError(w, r, fmt.Errorf("document %q: %w", r.PathValue("path"), docs.ErrNotFound))
		return
	}
	w.Header().Set("ETag", `"`+d.Hash+`"`)
	switch r.URL.Query().Get("format") {
	case "raw", "markdown":
		writeMarkdown(w, d)
		return
	case "html":
		rendered, err := s.render(snap, spec, d, r.URL.Query().Get("v") != "")
		if err != nil {
			s.apiError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"version": snap.Version, "doc": s.docMeta(r, snap, d, true), "html": rendered.HTML, "toc": rendered.TOC,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"version": snap.Version, "revision": snap.Revision, "doc": s.docMeta(r, snap, d, true),
		"source_url": spec.Source.FileURL(snap.Revision, snap.RepoPath(d.Path)),
		"markdown":   string(d.Content), "sections": d.Sections,
	})
}

// apiChunks 返回文档树（或单篇文档）的 RAG 切片，供外部索引任务全量拉取。
// 带 since=<version> 时返回相对该版本的增量。
func (s *Server) apiChunks(w http.ResponseWriter, r *http.Request) {
	key := treeKey(r)
	snap, err := s.snapshotFor(r, key)
	if err != nil {
		s.apiError(w, r, err)
		return
	}
	size, _ := strconv.Atoi(r.URL.Query().Get("size"))
	size = cmp.Or(min(max(size, 0), 20000), docs.DefaultChunkSize)

	if since := r.URL.Query().Get("since"); since != "" {
		if !versionRe.MatchString(since) {
			s.apiError(w, r, fmt.Errorf("invalid version %q: %w", since, errBadRequest))
			return
		}
		prev, err := s.svc.SnapshotAt(r.Context(), key, since)
		if err != nil {
			s.apiError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, docs.DiffChunks(prev, snap, size))
		return
	}
	var chunks []docs.Chunk
	if p := r.URL.Query().Get("path"); p != "" {
		d := snap.Lookup(p)
		if d == nil {
			s.apiError(w, r, fmt.Errorf("document %q: %w", p, docs.ErrNotFound))
			return
		}
		chunks = docs.ChunkDocument(snap, d, size)
	} else {
		for _, d := range snap.Docs {
			chunks = append(chunks, docs.ChunkDocument(snap, d, size)...)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"version": snap.Version, "revision": snap.Revision, "chunks": chunks})
}

func (s *Server) apiVersions(w http.ResponseWriter, r *http.Request) {
	versions, err := s.svc.Versions(r.Context(), treeKey(r))
	if err != nil && !errors.Is(err, docs.ErrNotFound) {
		s.apiError(w, r, err)
		return
	}
	if _, serr := s.svc.Spec(treeKey(r)); serr != nil {
		s.apiError(w, r, serr)
		return
	}
	if versions == nil {
		versions = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"versions": versions})
}

func (s *Server) apiRuns(w http.ResponseWriter, r *http.Request) {
	key := treeKey(r)
	if _, err := s.svc.Spec(key); err != nil {
		s.apiError(w, r, err)
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	runs, err := s.svc.Runs(r.Context(), key, cmp.Or(min(max(limit, 0), 200), 20))
	if err != nil {
		s.apiError(w, r, err)
		return
	}
	if runs == nil {
		runs = []docs.SyncRun{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"running": s.svc.Running(key), "runs": runs})
}

func boolParam(r *http.Request, name string) bool {
	v, _ := strconv.ParseBool(r.URL.Query().Get(name))
	return v
}

// apiSync 触发同步。默认异步执行并返回 202；wait=true 时等待同步完成并返回结果。
func (s *Server) apiSync(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "admin token required"})
		return
	}
	key := treeKey(r)
	force := boolParam(r, "force")
	if boolParam(r, "wait") {
		// 绑定生命周期 ctx：客户端断开不会中断同步，停机时同步会被取消。
		run, err := s.svc.Sync(s.opts.Lifecycle, key, docs.TriggerManual, force)
		if err != nil && run.ID == "" {
			s.apiError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, run)
		return
	}
	queued, err := s.svc.SyncAsync(s.opts.Lifecycle, key, docs.TriggerManual, force)
	if err != nil {
		s.apiError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"tree": key, "accepted": true, "queued": queued})
}

func (s *Server) apiSyncAll(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "admin token required"})
		return
	}
	force := boolParam(r, "force")
	var trees []docs.TreeKey
	for _, spec := range s.svc.Specs() {
		if ns := r.URL.Query().Get("namespace"); ns == "" || spec.Key.Namespace == ns {
			trees = append(trees, spec.Key)
		}
	}
	if boolParam(r, "wait") {
		writeJSON(w, http.StatusOK, map[string]any{"results": s.svc.SyncAll(s.opts.Lifecycle, trees, docs.TriggerManual, force)})
		return
	}
	for _, k := range trees {
		if _, err := s.svc.SyncAsync(s.opts.Lifecycle, k, docs.TriggerManual, force); err != nil {
			s.apiError(w, r, err)
			return
		}
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"trees": trees, "accepted": true})
}

func (s *Server) apiSearch(w http.ResponseWriter, r *http.Request) {
	if s.searcher == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "search is disabled"})
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	hits, err := s.searcher.Search(r.Context(), docs.SearchQuery{
		Text: q.Get("q"), Namespace: q.Get("namespace"), Tree: q.Get("tree"), Limit: limit,
	})
	if err != nil {
		s.apiError(w, r, err)
		return
	}
	type hit struct {
		docs.SearchHit
		URL string `json:"url"`
	}
	out := make([]hit, 0, len(hits))
	for _, h := range hits {
		out = append(out, hit{h, s.baseURL(r) + withFragment(docURL(h.Key, h.Slug), h.Anchor)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"query": q.Get("q"), "hits": out})
}

// ---- git 推送 webhook ----

const maxWebhookBody = 25 << 20 // GitHub 推送事件上限为 25MB

// pushEvent 是 GitHub / GitLab / Gitee / Gitea 推送事件中用到的字段。
type pushEvent struct {
	Ref        string `json:"ref"`
	Repository struct {
		CloneURL      string `json:"clone_url"`
		SSHURL        string `json:"ssh_url"`
		HTMLURL       string `json:"html_url"`
		DefaultBranch string `json:"default_branch"`
	} `json:"repository"`
	Project struct { // GitLab
		HTTPURL       string `json:"git_http_url"`
		SSHURL        string `json:"git_ssh_url"`
		WebURL        string `json:"web_url"`
		DefaultBranch string `json:"default_branch"`
	} `json:"project"`
}

func (e pushEvent) repoURLs() []string {
	return []string{e.Repository.CloneURL, e.Repository.SSHURL, e.Repository.HTMLURL,
		e.Project.HTTPURL, e.Project.SSHURL, e.Project.WebURL}
}

func (e pushEvent) defaultBranch() string {
	return cmp.Or(e.Repository.DefaultBranch, e.Project.DefaultBranch)
}

// webhook 接收 git 平台推送事件并触发同步。
// 路径带文档树时同步该树；不带时按事件中的仓库地址匹配所有使用该仓库的文档树。
func (s *Server) webhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxWebhookBody))
	if err != nil {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "body too large"})
		return
	}
	if !s.verifyWebhook(r, body) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid signature or token"})
		return
	}
	if isPing(r) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "pong"})
		return
	}
	var ev pushEvent
	if len(body) > 0 && strings.Contains(r.Header.Get("Content-Type"), "json") {
		_ = json.Unmarshal(body, &ev) // 解析失败时视为通用触发，不做分支过滤
	}

	var targets []*docs.TreeSpec
	if r.PathValue("ns") != "" {
		spec, err := s.svc.Spec(treeKey(r))
		if err != nil {
			s.apiError(w, r, err)
			return
		}
		targets = append(targets, spec)
	} else {
		for _, spec := range s.svc.Specs() {
			if spec.Source.Kind == docs.SourceGit && repoMatches(spec.Source.URL, ev.repoURLs()) {
				targets = append(targets, spec)
			}
		}
	}

	type result struct {
		Tree    docs.TreeKey `json:"tree"`
		Synced  bool         `json:"synced"`
		Queued  bool         `json:"queued,omitempty"`
		Skipped string       `json:"skipped,omitempty"`
	}
	results := []result{}
	for _, spec := range targets {
		res := result{Tree: spec.Key}
		if reason := branchMismatch(spec, ev); reason != "" {
			res.Skipped = reason
		} else if queued, err := s.svc.SyncAsync(s.opts.Lifecycle, spec.Key, docs.TriggerWebhook, false); err != nil {
			res.Skipped = err.Error()
		} else {
			res.Synced, res.Queued = true, queued
		}
		results = append(results, res)
	}
	s.log.InfoContext(r.Context(), "webhook received", "ref", ev.Ref, "trees", len(targets))
	writeJSON(w, http.StatusAccepted, map[string]any{"results": results})
}

// branchMismatch 判断推送的分支是否与文档树跟踪的分支一致，不一致时返回跳过原因。
func branchMismatch(spec *docs.TreeSpec, ev pushEvent) string {
	branch, ok := strings.CutPrefix(ev.Ref, "refs/heads/")
	if !ok {
		if ev.Ref != "" {
			return "not a branch push: " + ev.Ref
		}
		return "" // 无法识别分支（如手动调用），直接同步
	}
	want := cmp.Or(spec.Source.Branch, ev.defaultBranch())
	if want != "" && want != branch {
		return "branch " + branch + " is not tracked (" + want + ")"
	}
	return ""
}

// isPing 识别各平台保存 webhook 时发送的测试事件，这类事件只需应答，不触发同步。
func isPing(r *http.Request) bool {
	return r.Header.Get("X-GitHub-Event") == "ping" || r.Header.Get("X-Gitee-Ping") == "true"
}

// verifyWebhook 支持 GitHub / Gitea 的 HMAC 签名、GitLab 的令牌头、Gitee 的密码与签名，
// 以及管理令牌（请求头或 ?token=）。未配置任何密钥时放行。
func (s *Server) verifyWebhook(r *http.Request, body []byte) bool {
	secret := cmp.Or(s.opts.WebhookSecret, s.opts.AdminToken)
	if secret == "" {
		return true
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := mac.Sum(nil)
	for _, h := range []string{"X-Hub-Signature-256", "X-Gitea-Signature", "X-Gogs-Signature"} {
		if v := r.Header.Get(h); v != "" {
			got, err := hex.DecodeString(strings.TrimPrefix(v, "sha256="))
			return err == nil && hmac.Equal(got, expected)
		}
	}
	if v := r.Header.Get("X-Gitlab-Token"); v != "" {
		return hmac.Equal([]byte(v), []byte(secret))
	}
	if v := r.Header.Get("X-Gitee-Token"); v != "" {
		return verifyGitee(v, r.Header.Get("X-Gitee-Timestamp"), secret, time.Now())
	}
	if v := r.URL.Query().Get("token"); v != "" {
		return hmac.Equal([]byte(v), []byte(secret))
	}
	return tokenMatches(r, secret)
}

// giteeMaxSkew 是 Gitee 签名时间戳允许的最大偏差，与 Gitee 文档建议一致。
const giteeMaxSkew = time.Hour

// verifyGitee 校验 Gitee webhook：密码模式下令牌即密钥；签名模式下令牌为
// base64(HMAC-SHA256(secret, timestamp + "\n" + secret))，timestamp 为毫秒时间戳。
func verifyGitee(token, timestamp, secret string, now time.Time) bool {
	if hmac.Equal([]byte(token), []byte(secret)) {
		return true
	}
	ms, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || now.Sub(time.UnixMilli(ms)).Abs() > giteeMaxSkew {
		return false
	}
	if t, err := url.QueryUnescape(token); err == nil {
		token = t
	}
	got, err := base64.StdEncoding.DecodeString(token)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp + "\n" + secret))
	return hmac.Equal(got, mac.Sum(nil))
}

// repoMatches 判断配置的仓库地址是否与事件中的任一地址指向同一仓库（忽略协议、凭据与 .git 后缀）。
func repoMatches(configured string, candidates []string) bool {
	want := normalizeRepo(configured)
	if want == "" {
		return false
	}
	for _, c := range candidates {
		if c != "" && normalizeRepo(c) == want {
			return true
		}
	}
	return false
}

func normalizeRepo(u string) string {
	u = strings.TrimSpace(u)
	if m := strings.Index(u, "://"); m < 0 {
		// scp 风格：git@host:org/repo.git
		if at := strings.Index(u, "@"); at >= 0 {
			u = u[at+1:]
		}
		u = strings.Replace(u, ":", "/", 1)
	} else if p, err := url.Parse(u); err == nil {
		u = p.Hostname() + p.Path
	}
	u = strings.TrimSuffix(strings.TrimSuffix(u, "/"), ".git")
	return strings.ToLower(u)
}
