package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"slices"
	"strings"
	"time"

	"getdocs/internal/domain/docs"
)

// canRegister 判断是否开放页面登记：必须配置管理令牌，否则任何人都能让服务拉取任意仓库。
func (s *Server) canRegister() bool { return s.opts.AdminToken != "" && s.svc.CanRegister() }

// apiAddTree 登记一棵 git 文档树并立即开始首次同步。
func (s *Server) apiAddTree(w http.ResponseWriter, r *http.Request) {
	if !s.canRegister() {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "tree registration is disabled (requires server.admin_token)"})
		return
	}
	if !s.authorized(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "admin token required"})
		return
	}
	var t docs.RegisteredTree
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&t); err != nil {
		s.apiError(w, r, fmt.Errorf("invalid json: %v: %w", err, errBadRequest))
		return
	}
	t.Repo = strings.TrimSpace(t.Repo)
	if err := s.checkRepoHost(r.Context(), t.Repo); err != nil {
		s.apiError(w, r, err)
		return
	}
	spec, err := s.svc.AddTree(r.Context(), t)
	if err != nil {
		s.apiError(w, r, err)
		return
	}
	s.log.InfoContext(r.Context(), "tree added via api", "tree", spec.Key, "repo", spec.Source.Display(), "remote", r.RemoteAddr)
	if _, err := s.svc.SyncAsync(s.opts.Lifecycle, spec.Key, docs.TriggerManual, false); err != nil {
		s.log.WarnContext(r.Context(), "initial sync not started", "tree", spec.Key, "err", err)
	}
	writeJSON(w, http.StatusCreated, map[string]any{"tree": spec.Key, "status_url": "/status/" + escapePath(spec.Key.String())})
}

func (s *Server) apiRemoveTree(w http.ResponseWriter, r *http.Request) {
	if !s.canRegister() {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "tree registration is disabled (requires server.admin_token)"})
		return
	}
	if !s.authorized(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "admin token required"})
		return
	}
	key := treeKey(r)
	if err := s.svc.RemoveTree(r.Context(), key); err != nil {
		s.apiError(w, r, err)
		return
	}
	s.log.InfoContext(r.Context(), "tree removed via api", "tree", key, "remote", r.RemoteAddr)
	writeJSON(w, http.StatusOK, map[string]any{"tree": key, "removed": true})
}

// checkRepoHost 防止借登记功能访问内网服务（SSRF）：主机不在白名单时，解析出的地址不能是内网、
// 回环或链路本地地址。白名单非空时只允许名单内的主机。
func (s *Server) checkRepoHost(ctx context.Context, repo string) error {
	if !docs.IsRemoteRepo(repo) {
		return fmt.Errorf("repo %q must be an https or ssh git url: %w", repo, docs.ErrInvalid)
	}
	host := docs.RepoHost(repo)
	if slices.ContainsFunc(s.opts.AllowedHosts, func(h string) bool { return strings.EqualFold(h, host) }) {
		return nil
	}
	if len(s.opts.AllowedHosts) > 0 {
		return fmt.Errorf("host %s is not in registry.allowed_hosts: %w", host, docs.ErrInvalid)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return fmt.Errorf("resolve %s: %v: %w", host, err, docs.ErrInvalid)
	}
	for _, ip := range ips {
		if a := ip.IP; a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsUnspecified() || a.IsLinkLocalMulticast() {
			return fmt.Errorf("host %s resolves to a private address; add it to registry.allowed_hosts: %w", host, docs.ErrInvalid)
		}
	}
	return nil
}
