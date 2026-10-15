package git

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/rushteam/getdocs/internal/domain/docs"
)

// localCheckout 直接读取本地目录，适用于本地开发预览或挂载进容器的文档。
// 若目录位于 git 仓库内，会读取当前提交作为版本信息。
type localCheckout struct {
	root string
	rev  docs.Revision
}

func openLocal(dir string) (docs.Checkout, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", abs)
	}
	return &localCheckout{root: abs, rev: localRevision(abs)}, nil
}

func localRevision(dir string) docs.Revision {
	cmd := exec.Command("git", "log", "-1", "--format=%H%x00%an%x00%ct%x00%s")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return docs.Revision{}
	}
	parts := strings.SplitN(strings.TrimSpace(string(out)), "\x00", 4)
	if len(parts) != 4 {
		return docs.Revision{}
	}
	ts, _ := strconv.ParseInt(parts[2], 10, 64)
	rev := docs.Revision{Commit: parts[0], Author: parts[1], Time: time.Unix(ts, 0).UTC(), Message: parts[3]}
	cmd = exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = dir
	if b, err := cmd.Output(); err == nil {
		rev.Branch = strings.TrimSpace(string(b))
	}
	return rev
}

func (l *localCheckout) Revision() docs.Revision { return l.rev }

func (l *localCheckout) Files(ctx context.Context) ([]string, error) {
	var files []string
	err := filepath.WalkDir(l.root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, _ := filepath.Rel(l.root, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel != "." && (strings.HasPrefix(d.Name(), ".") || d.Name() == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type().IsRegular() {
			files = append(files, rel)
		}
		return nil
	})
	return files, err
}

func (l *localCheckout) Prepare(context.Context, []string) error { return nil }

func (l *localCheckout) ReadFile(_ context.Context, p string) ([]byte, error) {
	p = path.Clean(p)
	if docs.Escapes(p) || strings.HasPrefix(p, "/") {
		return nil, fmt.Errorf("invalid path %q", p)
	}
	return os.ReadFile(filepath.Join(l.root, filepath.FromSlash(p)))
}

func (l *localCheckout) Close() error { return nil }
