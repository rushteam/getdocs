// Package git 通过系统 git 命令拉取文档仓库。
//
// 代码仓库往往很大而文档只占一小部分，因此采用浅克隆（depth=1）+ 部分克隆（blob:none）+
// 稀疏检出：首次同步只下载提交与目录树，实际文件内容只按文档目录批量下载。
package git

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rushteam/getdocs/internal/domain/docs"
)

// Fetcher 实现 docs.Fetcher，为每个“仓库地址 + 分支”维护一份缓存工作副本。
type Fetcher struct {
	dir     string
	timeout time.Duration
	logger  *slog.Logger

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

var _ docs.Fetcher = (*Fetcher)(nil)

// NewFetcher 创建 git 拉取器，dir 为工作副本缓存目录。
func NewFetcher(dir string, timeout time.Duration, logger *slog.Logger) (*Fetcher, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return nil, fmt.Errorf("git executable not found: %w", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	return &Fetcher{dir: dir, timeout: timeout, logger: logger, locks: map[string]*sync.Mutex{}}, nil
}

func (f *Fetcher) lock(key string) *sync.Mutex {
	f.mu.Lock()
	defer f.mu.Unlock()
	l, ok := f.locks[key]
	if !ok {
		l = &sync.Mutex{}
		f.locks[key] = l
	}
	return l
}

// Fetch 更新工作副本到远端最新提交。返回的 Checkout 持有该工作副本的锁，直到 Close。
func (f *Fetcher) Fetch(ctx context.Context, src docs.Source) (docs.Checkout, error) {
	if src.Kind == docs.SourceLocal {
		return openLocal(src.URL)
	}
	sum := sha256.Sum256([]byte(src.URL + "\x00" + src.Branch))
	key := hex.EncodeToString(sum[:10])
	l := f.lock(key)
	l.Lock()
	ok := false
	defer func() {
		if !ok {
			l.Unlock()
		}
	}()

	r := &repo{dir: filepath.Join(f.dir, key), env: authEnv(src.Auth), timeout: f.timeout, secret: src.Auth.Password}
	if err := r.ensure(ctx, src); err != nil {
		return nil, err
	}
	rev, err := r.update(ctx, src.Branch)
	if err != nil {
		return nil, err
	}
	ok = true
	return &checkout{repo: r, rev: rev, unlock: l.Unlock}, nil
}

type repo struct {
	dir     string
	env     []string
	timeout time.Duration
	secret  string
}

func (r *repo) git(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	if _, err := os.Stat(r.dir); err == nil {
		cmd.Dir = r.dir
	}
	cmd.Env = append(os.Environ(), r.env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if r.secret != "" {
			msg = strings.ReplaceAll(msg, r.secret, "***")
		}
		if ctx.Err() != nil {
			return nil, fmt.Errorf("git %s: %w: %s", args[0], ctx.Err(), msg)
		}
		return nil, fmt.Errorf("git %s: %w: %s", args[0], err, msg)
	}
	return stdout.Bytes(), nil
}

// ensure 在工作副本不存在或损坏时重新克隆。
func (r *repo) ensure(ctx context.Context, src docs.Source) error {
	if _, err := os.Stat(filepath.Join(r.dir, ".git")); err == nil {
		if out, err := r.git(ctx, "remote", "get-url", "origin"); err == nil && strings.TrimSpace(string(out)) == src.URL {
			return nil
		}
		if err := os.RemoveAll(r.dir); err != nil {
			return err
		}
	}
	tmp := r.dir + ".tmp"
	_ = os.RemoveAll(tmp)
	args := []string{"clone", "--filter=blob:none", "--depth=1", "--single-branch", "--no-checkout"}
	if src.Branch != "" {
		args = append(args, "--branch", src.Branch)
	}
	args = append(args, "--", src.URL, tmp)
	if _, err := r.git(ctx, args...); err != nil {
		_ = os.RemoveAll(tmp)
		return err
	}
	if err := os.Rename(tmp, r.dir); err != nil {
		return err
	}
	// 记录克隆时远端的默认分支，后续以分离 HEAD 方式检出时仍能报告分支名。
	if src.Branch == "" {
		if out, err := r.git(ctx, "symbolic-ref", "--short", "HEAD"); err == nil {
			_, _ = r.git(ctx, "config", "getdocs.branch", strings.TrimSpace(string(out)))
		}
	}
	_, err := r.git(ctx, "sparse-checkout", "set", "--no-cone", "--", "/*.md")
	return err
}

// update 拉取远端最新提交并检出（受稀疏规则约束）。
func (r *repo) update(ctx context.Context, branch string) (docs.Revision, error) {
	ref := branch
	if ref == "" {
		ref = "HEAD"
	}
	if _, err := r.git(ctx, "fetch", "--depth=1", "--filter=blob:none", "--no-tags", "origin", ref); err != nil {
		return docs.Revision{}, err
	}
	out, err := r.git(ctx, "log", "-1", "--format=%H%x00%an%x00%ct%x00%s", "FETCH_HEAD")
	if err != nil {
		return docs.Revision{}, err
	}
	parts := strings.SplitN(strings.TrimSpace(string(out)), "\x00", 4)
	if len(parts) != 4 {
		return docs.Revision{}, fmt.Errorf("unexpected git log output %q", out)
	}
	ts, _ := strconv.ParseInt(parts[2], 10, 64)
	rev := docs.Revision{Commit: parts[0], Author: parts[1], Time: time.Unix(ts, 0).UTC(), Message: parts[3], Branch: branch}
	if rev.Branch == "" {
		if out, err := r.git(ctx, "config", "--get", "getdocs.branch"); err == nil {
			rev.Branch = strings.TrimSpace(string(out))
		}
	}
	if _, err := r.git(ctx, "-c", "advice.detachedHead=false", "checkout", "--force", "--detach", rev.Commit); err != nil {
		return docs.Revision{}, err
	}
	return rev, nil
}

type checkout struct {
	*repo
	rev    docs.Revision
	unlock func()
	once   sync.Once
	sparse map[string]bool
}

func (c *checkout) Revision() docs.Revision { return c.rev }

func (c *checkout) Files(ctx context.Context) ([]string, error) {
	out, err := c.git(ctx, "ls-tree", "-r", "-z", "--name-only", c.rev.Commit)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, p := range bytes.Split(out, []byte{0}) {
		if len(p) > 0 {
			files = append(files, string(p))
		}
	}
	return files, nil
}

// Prepare 把稀疏检出规则设置为给定目录，git 会一次性批量下载这些目录下缺失的文件内容。
// 文档根为仓库根时只检出文档与资源类型的文件，避免下载整个代码仓库。
func (c *checkout) Prepare(ctx context.Context, dirs []string) error {
	var patterns []string
	c.sparse = map[string]bool{}
	for _, d := range dirs {
		c.sparse[d] = true
		if d == "." || d == "" {
			for _, ext := range slices.Concat(docs.MarkdownExts, docs.ProtoExts) {
				patterns = append(patterns, "*"+ext)
			}
			for ext := range docs.AssetExts {
				patterns = append(patterns, "*"+ext)
			}
			continue
		}
		patterns = append(patterns, "/"+strings.Trim(d, "/")+"/")
	}
	_, err := c.git(ctx, append([]string{"sparse-checkout", "set", "--no-cone", "--"}, patterns...)...)
	return err
}

func (c *checkout) prepared(p string) bool {
	for d := range c.sparse {
		if d == "." || d == "" {
			return docs.IsMarkdown(p) || docs.IsProto(p) || docs.IsAsset(p)
		}
		if strings.HasPrefix(p, strings.Trim(d, "/")+"/") {
			return true
		}
	}
	return false
}

// ReadFile 优先读取已检出的文件；不在稀疏规则内的文件（如文档根之外的图片）按需从对象库读取。
func (c *checkout) ReadFile(ctx context.Context, p string) ([]byte, error) {
	p = path.Clean(p)
	if docs.Escapes(p) || strings.HasPrefix(p, "/") {
		return nil, fmt.Errorf("invalid path %q", p)
	}
	if c.prepared(p) {
		b, err := os.ReadFile(filepath.Join(c.dir, filepath.FromSlash(p)))
		if err == nil || !errors.Is(err, os.ErrNotExist) {
			return b, err
		}
	}
	return c.git(ctx, "cat-file", "blob", c.rev.Commit+":"+p)
}

func (c *checkout) Close() error {
	c.once.Do(c.unlock)
	return nil
}

// authEnv 通过环境变量注入凭据：HTTP 认证头不会写入 .git/config，也不会出现在进程参数中。
func authEnv(a docs.Auth) []string {
	// 只允许常规传输协议，禁用 ext:: 等可执行任意命令的协议。
	env := []string{"GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=echo", "GCM_INTERACTIVE=never", "GIT_ALLOW_PROTOCOL=https:http:ssh:git:file"}
	if a.Password != "" {
		user := a.Username
		if user == "" {
			user = "oauth2"
		}
		cred := base64.StdEncoding.EncodeToString([]byte(user + ":" + a.Password))
		env = append(env,
			"GIT_CONFIG_COUNT=1",
			"GIT_CONFIG_KEY_0=http.extraHeader",
			"GIT_CONFIG_VALUE_0=Authorization: Basic "+cred,
		)
	}
	ssh := "ssh -o BatchMode=yes -o StrictHostKeyChecking=accept-new"
	if a.SSHKey != "" {
		ssh += " -o IdentitiesOnly=yes -i " + shellQuote(a.SSHKey)
	}
	return append(env, "GIT_SSH_COMMAND="+ssh)
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
