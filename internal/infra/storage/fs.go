// Package storage 以本地文件系统实现快照与同步状态的持久化。
//
// 目录结构：
//
//	blobs/ab/abcdef...            内容寻址的文档原文与资源（跨版本、跨文档树去重）
//	trees/<ns>/<tree>/versions/<version>.json   快照清单
//	trees/<ns>/<tree>/CURRENT     当前版本号
//	trees/<ns>/<tree>/state.json  同步状态
//	trees/<ns>/<tree>/runs.jsonl  同步历史
package storage

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/rushteam/getdocs/internal/domain/docs"
)

// FS 实现 docs.SnapshotStore 与 docs.StateStore。
type FS struct {
	root string
	mu   sync.Mutex // 串行化清理与写入，避免清理掉正在写入的内容
}

var (
	_ docs.SnapshotStore = (*FS)(nil)
	_ docs.StateStore    = (*FS)(nil)
)

// maxRuns 是每棵文档树保留的同步历史条数。
const maxRuns = 200

// NewFS 创建文件存储。
func NewFS(root string) (*FS, error) {
	if err := os.MkdirAll(filepath.Join(root, "blobs"), 0o755); err != nil {
		return nil, err
	}
	return &FS{root: root}, nil
}

func (s *FS) treeDir(k docs.TreeKey) string {
	return filepath.Join(s.root, "trees", safeName(k.Namespace), safeName(k.Tree))
}

func (s *FS) blobPath(hash string) string {
	if len(hash) < 3 {
		return filepath.Join(s.root, "blobs", "_", hash)
	}
	return filepath.Join(s.root, "blobs", hash[:2], hash)
}

func safeName(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == 0 {
			return '_'
		}
		return r
	}, strings.TrimLeft(s, "."))
}

// writeAtomic 先写临时文件再重命名，避免进程中断留下半个文件。
func writeAtomic(p string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p)
}

func (s *FS) Save(ctx context.Context, snap *docs.Snapshot, blobs []docs.Blob) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, b := range blobs {
		if err := ctx.Err(); err != nil {
			return err
		}
		p := s.blobPath(b.Hash)
		if _, err := os.Stat(p); err == nil {
			continue
		}
		if err := writeAtomic(p, b.Data); err != nil {
			return fmt.Errorf("write blob: %w", err)
		}
	}
	manifest, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	dir := s.treeDir(snap.Key)
	if err := writeAtomic(filepath.Join(dir, "versions", snap.Version+".json"), manifest); err != nil {
		return err
	}
	return writeAtomic(filepath.Join(dir, "CURRENT"), []byte(snap.Version))
}

func (s *FS) Current(ctx context.Context, key docs.TreeKey) (*docs.Snapshot, error) {
	v, err := s.CurrentVersion(ctx, key)
	if err != nil {
		return nil, err
	}
	return s.Load(ctx, key, v)
}

func (s *FS) CurrentVersion(_ context.Context, key docs.TreeKey) (string, error) {
	b, err := os.ReadFile(filepath.Join(s.treeDir(key), "CURRENT"))
	if errors.Is(err, fs.ErrNotExist) {
		return "", docs.ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

func (s *FS) Load(ctx context.Context, key docs.TreeKey, version string) (*docs.Snapshot, error) {
	if strings.ContainsAny(version, `/\`) || strings.HasPrefix(version, ".") {
		return nil, docs.ErrNotFound
	}
	b, err := os.ReadFile(filepath.Join(s.treeDir(key), "versions", version+".json"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, docs.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	snap := &docs.Snapshot{}
	if err := json.Unmarshal(b, snap); err != nil {
		return nil, fmt.Errorf("decode snapshot %s@%s: %w", key, version, err)
	}
	for _, d := range snap.Docs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if d.Content, err = s.ReadBlob(ctx, d.Hash); err != nil {
			return nil, fmt.Errorf("load %s: %w", d.Path, err)
		}
	}
	snap.Seal()
	return snap, nil
}

func (s *FS) Versions(_ context.Context, key docs.TreeKey) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(s.treeDir(key), "versions"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if v, ok := strings.CutSuffix(e.Name(), ".json"); ok && !e.IsDir() {
			out = append(out, v)
		}
	}
	slices.Sort(out)
	slices.Reverse(out)
	return out, nil
}

func (s *FS) ReadBlob(_ context.Context, hash string) ([]byte, error) {
	if strings.ContainsAny(hash, `/\.`) || hash == "" {
		return nil, docs.ErrNotFound
	}
	b, err := os.ReadFile(s.blobPath(hash))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, docs.ErrNotFound
	}
	return b, err
}

// Prune 删除旧版本清单，并对全部文档树做一次引用计数，清理无人引用的内容。
func (s *FS) Prune(ctx context.Context, key docs.TreeKey, keep int) error {
	if keep < 1 {
		keep = 1
	}
	versions, err := s.Versions(ctx, key)
	if err != nil || len(versions) <= keep {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, v := range versions[keep:] {
		if err := os.Remove(filepath.Join(s.treeDir(key), "versions", v+".json")); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return s.gc(ctx)
}

func (s *FS) gc(ctx context.Context) error {
	live := map[string]bool{}
	manifests, _ := filepath.Glob(filepath.Join(s.root, "trees", "*", "*", "versions", "*.json"))
	for _, m := range manifests {
		b, err := os.ReadFile(m)
		if err != nil {
			return err
		}
		var snap docs.Snapshot
		if err := json.Unmarshal(b, &snap); err != nil {
			return fmt.Errorf("decode %s: %w", m, err)
		}
		for _, d := range snap.Docs {
			live[d.Hash] = true
		}
		for _, a := range snap.Assets {
			live[a.Hash] = true
		}
	}
	return filepath.WalkDir(filepath.Join(s.root, "blobs"), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if !live[d.Name()] && !strings.HasPrefix(d.Name(), ".tmp-") {
			return os.Remove(p)
		}
		return nil
	})
}

func (s *FS) State(_ context.Context, key docs.TreeKey) (docs.TreeState, error) {
	b, err := os.ReadFile(filepath.Join(s.treeDir(key), "state.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return docs.TreeState{Key: key}, nil
	}
	if err != nil {
		return docs.TreeState{}, err
	}
	var st docs.TreeState
	if err := json.Unmarshal(b, &st); err != nil {
		return docs.TreeState{}, err
	}
	return st, nil
}

func (s *FS) SaveState(_ context.Context, st docs.TreeState) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(s.treeDir(st.Key), "state.json"), b)
}

func (s *FS) AppendRun(ctx context.Context, run docs.SyncRun) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	runs, err := s.readRuns(run.Key)
	if err != nil {
		return err
	}
	runs = append(runs, run)
	if len(runs) > maxRuns {
		runs = runs[len(runs)-maxRuns:]
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, r := range runs {
		if err := enc.Encode(r); err != nil {
			return err
		}
	}
	return writeAtomic(filepath.Join(s.treeDir(run.Key), "runs.jsonl"), buf.Bytes())
}

func (s *FS) readRuns(key docs.TreeKey) ([]docs.SyncRun, error) {
	f, err := os.Open(filepath.Join(s.treeDir(key), "runs.jsonl"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var runs []docs.SyncRun
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		var r docs.SyncRun
		if json.Unmarshal(sc.Bytes(), &r) == nil {
			runs = append(runs, r)
		}
	}
	return runs, sc.Err()
}

func (s *FS) Runs(_ context.Context, key docs.TreeKey, limit int) ([]docs.SyncRun, error) {
	s.mu.Lock()
	runs, err := s.readRuns(key)
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	slices.Reverse(runs)
	if limit > 0 && len(runs) > limit {
		runs = runs[:limit]
	}
	return runs, nil
}
