package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rushteam/getdocs/internal/domain/docs"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "getdocs.yaml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoad(t *testing.T) {
	t.Setenv("GD_TEST_TOKEN", "tok")
	p := writeConfig(t, `
server:
  base_url: https://docs.example.com/
storage:
  dir: ./state
sync:
  interval: 5m
namespaces:
  - name: platform
    trees:
      - name: api
        repo: https://github.com/org/api.git
        branch: main
        exclude: ["drafts/**"]
        auth:
          token: ${GD_TEST_TOKEN}
          username: ${GD_TEST_USER:-oauth2}
      - name: handbook
        path: ./handbook
        root: README.md
        interval: 1h
        auto_sync: false
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(p)
	if cfg.Server.Addr != ":8080" || cfg.Server.BaseURL != "https://docs.example.com" {
		t.Errorf("server defaults = %+v", cfg.Server)
	}
	if cfg.Storage.Dir != filepath.Join(dir, "state") || cfg.Storage.KeepVersions != 5 {
		t.Errorf("storage = %+v", cfg.Storage)
	}
	if !*cfg.Sync.OnStart || !*cfg.Search.Enabled || !*cfg.MCP.Enabled || cfg.MCP.AllowSync {
		t.Errorf("feature defaults wrong")
	}

	nss := cfg.DomainNamespaces()
	if len(nss) != 1 || nss[0].Title != "platform" || len(nss[0].Trees) != 2 {
		t.Fatalf("namespaces = %+v", nss)
	}
	api, hb := nss[0].Trees[0], nss[0].Trees[1]
	if api.Source.Kind != docs.SourceGit || api.Source.Auth.Password != "tok" || api.Source.Auth.Username != "oauth2" {
		t.Errorf("api source = %+v", api.Source)
	}
	if api.Source.BrowseURL != "https://github.com/org/api/blob/{branch}/{path}" {
		t.Errorf("browse url = %q", api.Source.BrowseURL)
	}
	if api.Interval != 5*time.Minute || !api.AutoSync || api.Title != "api" {
		t.Errorf("api defaults = %+v", api)
	}
	if hb.Source.Kind != docs.SourceLocal || hb.Source.URL != filepath.Join(dir, "handbook") || hb.Root != "README.md" {
		t.Errorf("handbook source = %+v", hb.Source)
	}
	if hb.Interval != time.Hour || hb.AutoSync {
		t.Errorf("handbook overrides = %+v", hb)
	}
}

func TestLoadInvalid(t *testing.T) {
	cases := map[string]string{
		"unknown field":  "namespaces:\n  - name: a\n    tress: []\n",
		"repo and path":  "namespaces:\n  - name: a\n    trees:\n      - {name: t, repo: x, path: y}\n",
		"neither":        "namespaces:\n  - name: a\n    trees:\n      - {name: t}\n",
		"bad name":       "namespaces:\n  - name: a/b\n    trees:\n      - {name: t, path: x}\n",
		"duplicate":      "namespaces:\n  - name: a\n    trees:\n      - {name: t, path: x}\n      - {name: t, path: y}\n",
		"bad glob":       "namespaces:\n  - name: a\n    trees:\n      - {name: t, path: x, exclude: ['[']}\n",
		"short interval": "namespaces:\n  - name: a\n    trees:\n      - {name: t, path: x, interval: 1s}\n",
		"bad duration":   "sync: {interval: soon}\nnamespaces:\n  - name: a\n    trees:\n      - {name: t, path: x}\n",
		"bad provider":   "namespaces:\n  - name: a\n    trees:\n      - {name: t, repo: https://git.example.com/a/b, provider: svn}\n",
		"local provider": "namespaces:\n  - name: a\n    trees:\n      - {name: t, path: x, provider: github}\n",
		"gitee no user":  "namespaces:\n  - name: a\n    trees:\n      - {name: t, repo: https://gitee.com/a/b.git, auth: {token: x}}\n",
		"bad webhook":    "hooks:\n  webhooks: [{url: 'ftp://x'}]\nnamespaces:\n  - name: a\n    trees:\n      - {name: t, path: x}\n",
	}
	for name, content := range cases {
		if _, err := Load(writeConfig(t, content)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestInferBrowseURL(t *testing.T) {
	cases := []struct {
		repo     string
		provider docs.Provider
		want     string
	}{
		{"https://github.com/org/repo.git", "", "https://github.com/org/repo/blob/{branch}/{path}"},
		{"https://user:tok@github.com/org/repo", "", "https://github.com/org/repo/blob/{branch}/{path}"},
		{"git@github.com:org/repo.git", "", "https://github.com/org/repo/blob/{branch}/{path}"},
		{"ssh://git@gitlab.example.com/grp/sub/repo", "", "https://gitlab.example.com/grp/sub/repo/-/blob/{branch}/{path}"},
		{"ssh://git@gitlab.example.com:2222/grp/repo.git", "", "https://gitlab.example.com/grp/repo/-/blob/{branch}/{path}"},
		{"https://gitee.com/org/repo.git", "", "https://gitee.com/org/repo/blob/{branch}/{path}"},
		{"git@gitee.com:org/repo.git", "", "https://gitee.com/org/repo/blob/{branch}/{path}"},
		{"https://gitea.example.com/org/repo", "", "https://gitea.example.com/org/repo/src/branch/{branch}/{path}"},
		{"https://codeberg.org/org/repo.git", "", "https://codeberg.org/org/repo/src/branch/{branch}/{path}"},
		{"https://bitbucket.org/org/repo.git", "", "https://bitbucket.org/org/repo/src/{branch}/{path}"},
		// 自建平台域名中没有平台名，靠 provider 指定
		{"https://code.example.com/org/repo.git", docs.ProviderGitLab, "https://code.example.com/org/repo/-/blob/{branch}/{path}"},
		{"git@git.example.com:org/repo.git", docs.ProviderGitea, "https://git.example.com/org/repo/src/branch/{branch}/{path}"},
		{"file:///tmp/repo", "", ""},
		{"/srv/repo", "", ""},
	}
	for _, c := range cases {
		if got := docs.InferBrowseURL(c.repo, c.provider); got != c.want {
			t.Errorf("InferBrowseURL(%q, %q) = %q, want %q", c.repo, c.provider, got, c.want)
		}
	}
}

func TestProviders(t *testing.T) {
	cfg, err := Load(writeConfig(t, `
namespaces:
  - name: a
    trees:
      - {name: gh, repo: https://github.com/o/r.git, auth: {token: t}}
      - {name: gl, repo: https://gitlab.com/o/r.git, auth: {token: t}}
      - {name: ge, repo: https://gitee.com/o/r.git, auth: {username: me, token: t}}
      - {name: gt, repo: https://git.example.com/o/r.git, provider: Gitea, auth: {token: t}}
      - {name: bb, repo: https://bitbucket.org/o/r.git, auth: {token: t}}
      - {name: ssh, repo: git@git.example.com:o/r.git}
`))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]struct {
		provider docs.Provider
		user     string
	}{
		"gh":  {docs.ProviderGitHub, "x-access-token"},
		"gl":  {docs.ProviderGitLab, "oauth2"},
		"ge":  {docs.ProviderGitee, "me"},
		"gt":  {docs.ProviderGitea, "oauth2"},
		"bb":  {docs.ProviderBitbucket, "x-token-auth"},
		"ssh": {"", ""},
	}
	for _, tr := range cfg.DomainNamespaces()[0].Trees {
		w := want[tr.Key.Tree]
		if tr.Source.Provider != w.provider || tr.Source.Auth.Username != w.user {
			t.Errorf("%s: provider %q user %q, want %q %q", tr.Key.Tree, tr.Source.Provider, tr.Source.Auth.Username, w.provider, w.user)
		}
	}
}

func TestExampleConfigLoads(t *testing.T) {
	raw, err := os.ReadFile("../../../config.example.yaml")
	if err != nil {
		t.Skip("config.example.yaml not present")
	}
	p := writeConfig(t, string(raw))
	if _, err := Load(p); err != nil && !strings.Contains(err.Error(), "no such file") {
		t.Fatalf("example config invalid: %v", err)
	}
}
