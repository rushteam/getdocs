package git

import (
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"getdocs/internal/domain/docs"
)

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Tester", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=Tester", "GIT_COMMITTER_EMAIL=t@example.com",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for p, c := range files {
		p = filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// newRepo 创建一个模拟代码仓库：文档只在 docs/ 下，图片放在仓库根的 assets/。
func newRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run(t, dir, "init", "-q", "-b", "main")
	run(t, dir, "config", "uploadpack.allowFilter", "true")
	run(t, dir, "config", "uploadpack.allowAnySHA1InWant", "true")
	writeFiles(t, dir, map[string]string{
		"main.go":          "package main\n",
		"README.md":        "# Project\n",
		"docs/index.md":    "# Docs\n",
		"docs/guide/a.md":  "# A\n",
		"assets/logo.png":  "PNG",
		"internal/x/x.go":  "package x\n",
		"internal/x/x.txt": "notes",
	})
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "-q", "-m", "initial")
	return dir
}

func newFetcher(t *testing.T) *Fetcher {
	t.Helper()
	f, err := NewFetcher(filepath.Join(t.TempDir(), "repos"), time.Minute, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestFetchGitRepo(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	ctx := t.Context()
	origin := newRepo(t)
	f := newFetcher(t)
	src := docs.Source{Kind: docs.SourceGit, URL: "file://" + origin}

	co, err := f.Fetch(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	rev := co.Revision()
	if rev.Commit != run(t, origin, "rev-parse", "HEAD") || rev.Branch != "main" || rev.Message != "initial" || rev.Author != "Tester" {
		t.Errorf("revision = %+v", rev)
	}
	files, err := co.Files(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(files, "docs/guide/a.md") || !slices.Contains(files, "main.go") || len(files) != 7 {
		t.Errorf("files = %v", files)
	}
	if err := co.Prepare(ctx, []string{"docs"}); err != nil {
		t.Fatal(err)
	}
	if b, err := co.ReadFile(ctx, "docs/guide/a.md"); err != nil || string(b) != "# A\n" {
		t.Errorf("ReadFile(docs) = %q, %v", b, err)
	}
	// 文档根之外的资源按需从对象库读取。
	if b, err := co.ReadFile(ctx, "assets/logo.png"); err != nil || string(b) != "PNG" {
		t.Errorf("ReadFile(outside sparse set) = %q, %v", b, err)
	}
	if _, err := co.ReadFile(ctx, "../secret"); err == nil {
		t.Errorf("ReadFile escaped the repository")
	}
	work := co.(*checkout).dir
	if _, err := os.Stat(filepath.Join(work, "main.go")); !os.IsNotExist(err) {
		t.Errorf("code outside the docs root was checked out: %v", err)
	}
	if err := co.Close(); err != nil {
		t.Fatal(err)
	}

	// 新提交后再次拉取，复用已有工作副本。
	writeFiles(t, origin, map[string]string{"docs/guide/a.md": "# A v2\n", "docs/b.md": "# B\n"})
	run(t, origin, "add", "-A")
	run(t, origin, "commit", "-q", "-m", "update docs")

	co, err = f.Fetch(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	defer co.Close()
	if co.Revision().Commit == rev.Commit || co.Revision().Message != "update docs" || co.Revision().Branch != "main" {
		t.Errorf("revision after update = %+v", co.Revision())
	}
	if co.(*checkout).dir != work {
		t.Errorf("working copy not reused")
	}
	if err := co.Prepare(ctx, []string{"docs"}); err != nil {
		t.Fatal(err)
	}
	if b, _ := co.ReadFile(ctx, "docs/guide/a.md"); string(b) != "# A v2\n" {
		t.Errorf("updated content = %q", b)
	}
	if files, _ := co.Files(ctx); !slices.Contains(files, "docs/b.md") {
		t.Errorf("new file missing: %v", files)
	}
}

func TestFetchRootSparseFilter(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	ctx := t.Context()
	origin := newRepo(t)
	co, err := newFetcher(t).Fetch(ctx, docs.Source{Kind: docs.SourceGit, URL: origin, Branch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	defer co.Close()
	if err := co.Prepare(ctx, []string{"."}); err != nil {
		t.Fatal(err)
	}
	work := co.(*checkout).dir
	for p, want := range map[string]bool{"README.md": true, "docs/index.md": true, "assets/logo.png": true, "main.go": false, "internal/x/x.go": false} {
		_, err := os.Stat(filepath.Join(work, p))
		if (err == nil) != want {
			t.Errorf("%s checked out = %v, want %v", p, err == nil, want)
		}
	}
	if b, err := co.ReadFile(ctx, "main.go"); err != nil || string(b) != "package main\n" {
		t.Errorf("ReadFile(main.go) = %q, %v", b, err)
	}
}

func TestFetchErrors(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	ctx := t.Context()
	f := newFetcher(t)
	if _, err := f.Fetch(ctx, docs.Source{Kind: docs.SourceGit, URL: filepath.Join(t.TempDir(), "missing")}); err == nil {
		t.Errorf("fetching a missing repository succeeded")
	}
	origin := newRepo(t)
	if _, err := f.Fetch(ctx, docs.Source{Kind: docs.SourceGit, URL: origin, Branch: "nope"}); err == nil {
		t.Errorf("fetching a missing branch succeeded")
	}
	// 凭据不能出现在错误信息里。
	_, err := f.Fetch(ctx, docs.Source{Kind: docs.SourceGit, URL: "http://127.0.0.1:1/repo.git", Auth: docs.Auth{Password: "hunter2"}})
	if err == nil || strings.Contains(err.Error(), "hunter2") {
		t.Errorf("auth error = %v", err)
	}
}

func TestLocalCheckout(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"a.md": "# A", ".git/config": "x", "node_modules/m/readme.md": "x", "sub/b.md": "# B"})
	co, err := newFetcher(t).Fetch(ctx, docs.Source{Kind: docs.SourceLocal, URL: dir})
	if err != nil {
		t.Fatal(err)
	}
	files, _ := co.Files(ctx)
	if !slices.Equal(files, []string{"a.md", "sub/b.md"}) {
		t.Errorf("files = %v", files)
	}
	if _, err := co.ReadFile(ctx, "../x"); err == nil {
		t.Errorf("ReadFile escaped the root")
	}
	if _, err := newFetcher(t).Fetch(ctx, docs.Source{Kind: docs.SourceLocal, URL: filepath.Join(dir, "a.md")}); err == nil {
		t.Errorf("file accepted as local root")
	}
}

func TestAuthEnv(t *testing.T) {
	env := strings.Join(authEnv(docs.Auth{Password: "tok", SSHKey: "/keys/it's"}), "\n")
	if !strings.Contains(env, "GIT_CONFIG_VALUE_0=Authorization: Basic b2F1dGgyOnRvaw==") {
		t.Errorf("missing basic auth header: %s", env)
	}
	if !strings.Contains(env, `-i '/keys/it'\''s'`) || !strings.Contains(env, "GIT_TERMINAL_PROMPT=0") {
		t.Errorf("ssh env = %s", env)
	}
	if strings.Contains(strings.Join(authEnv(docs.Auth{}), "\n"), "extraHeader") {
		t.Errorf("auth header set without password")
	}
}
