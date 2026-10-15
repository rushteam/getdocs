package catalog

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"maps"
	"path"
	"regexp"
	"slices"
	"strings"
	"sync"

	"getdocs/internal/domain/docs"
)

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

// memRepo 是可变的内存仓库，模拟代码提交。
type memRepo struct {
	mu     sync.Mutex
	commit string
	files  map[string]string
	err    error
	gate   chan struct{} // 非 nil 时 Fetch 阻塞到关闭，用于测试并发
	fetchN int
}

func (r *memRepo) set(commit string, files map[string]string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.commit, r.files = commit, maps.Clone(files)
}

func (r *memRepo) Fetch(ctx context.Context, _ docs.Source) (docs.Checkout, error) {
	r.mu.Lock()
	gate := r.gate
	r.fetchN++
	r.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return nil, r.err
	}
	return &memCheckout{rev: docs.Revision{Commit: r.commit, Branch: "main"}, files: maps.Clone(r.files)}, nil
}

type memCheckout struct {
	rev   docs.Revision
	files map[string]string
}

func (c *memCheckout) Revision() docs.Revision { return c.rev }
func (c *memCheckout) Files(context.Context) ([]string, error) {
	return slices.Sorted(maps.Keys(c.files)), nil
}
func (c *memCheckout) Prepare(context.Context, []string) error { return nil }
func (c *memCheckout) ReadFile(_ context.Context, p string) ([]byte, error) {
	s, ok := c.files[p]
	if !ok {
		return nil, docs.ErrNotFound
	}
	return []byte(s), nil
}
func (c *memCheckout) Close() error { return nil }

// fakeParser 只识别一级标题、"draft: true" 与 markdown 链接，足以驱动构建流程；.proto 文件转换为固定的 markdown。
type fakeParser struct{}

var linkRe = regexp.MustCompile(`\]\(([^)\s]+)\)`)

func (fakeParser) Parse(name string, content []byte) (docs.ParsedDoc, error) {
	if docs.IsProto(name) {
		md := "# converted " + path.Base(name) + "\n"
		return docs.ParsedDoc{Content: []byte(md), Title: "converted " + path.Base(name)}, nil
	}
	s := string(content)
	if strings.HasPrefix(s, "!invalid") {
		return docs.ParsedDoc{}, errors.New("parse error")
	}
	var p docs.ParsedDoc
	for line := range strings.Lines(s) {
		line = strings.TrimSpace(line)
		if t, ok := strings.CutPrefix(line, "# "); ok && p.Title == "" {
			p.Title = t
		}
		if line == "draft: true" {
			p.Draft = true
		}
	}
	for _, m := range linkRe.FindAllStringSubmatch(s, -1) {
		p.Links = append(p.Links, m[1])
	}
	p.Sections = []docs.Section{{Text: s}}
	return p, nil
}

// memStore 同时实现 SnapshotStore 与 StateStore。
type memStore struct {
	mu      sync.Mutex
	snaps   map[docs.TreeKey][]*docs.Snapshot // 从旧到新
	blobs   map[string][]byte
	states  map[docs.TreeKey]docs.TreeState
	runs    []docs.SyncRun
	saveErr error
	loadsN  int
}

func newMemStore() *memStore {
	return &memStore{snaps: map[docs.TreeKey][]*docs.Snapshot{}, blobs: map[string][]byte{}, states: map[docs.TreeKey]docs.TreeState{}}
}

func (m *memStore) Save(_ context.Context, snap *docs.Snapshot, blobs []docs.Blob) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.saveErr != nil {
		return m.saveErr
	}
	for _, b := range blobs {
		m.blobs[b.Hash] = b.Data
	}
	m.snaps[snap.Key] = append(m.snaps[snap.Key], snap)
	return nil
}

func (m *memStore) Current(ctx context.Context, key docs.TreeKey) (*docs.Snapshot, error) {
	v, err := m.CurrentVersion(ctx, key)
	if err != nil {
		return nil, err
	}
	return m.Load(ctx, key, v)
}

func (m *memStore) CurrentVersion(_ context.Context, key docs.TreeKey) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	list := m.snaps[key]
	if len(list) == 0 {
		return "", docs.ErrNotFound
	}
	return list[len(list)-1].Version, nil
}

func (m *memStore) Load(_ context.Context, key docs.TreeKey, version string) (*docs.Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.loadsN++
	for _, s := range m.snaps[key] {
		if s.Version == version {
			// 模拟反序列化：返回独立副本
			cp := *s
			cp.Docs = nil
			for _, d := range s.Docs {
				dc := *d
				cp.Docs = append(cp.Docs, &dc)
			}
			cp.Seal()
			return &cp, nil
		}
	}
	return nil, docs.ErrNotFound
}

func (m *memStore) Versions(_ context.Context, key docs.TreeKey) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for _, s := range slices.Backward(m.snaps[key]) {
		out = append(out, s.Version)
	}
	return out, nil
}

func (m *memStore) ReadBlob(_ context.Context, hash string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.blobs[hash]
	if !ok {
		return nil, docs.ErrNotFound
	}
	return b, nil
}

func (m *memStore) Prune(_ context.Context, key docs.TreeKey, keep int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if list := m.snaps[key]; len(list) > keep {
		m.snaps[key] = list[len(list)-keep:]
	}
	return nil
}

func (m *memStore) State(_ context.Context, key docs.TreeKey) (docs.TreeState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.states[key], nil
}

func (m *memStore) SaveState(_ context.Context, st docs.TreeState) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.states[st.Key] = st
	return nil
}

func (m *memStore) AppendRun(_ context.Context, run docs.SyncRun) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.runs = append(m.runs, run)
	return nil
}

func (m *memStore) Runs(_ context.Context, key docs.TreeKey, limit int) ([]docs.SyncRun, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []docs.SyncRun
	for _, r := range slices.Backward(m.runs) {
		if r.Key == key && len(out) < limit {
			out = append(out, r)
		}
	}
	return out, nil
}

// recordHook 记录收到的同步事件。
type recordHook struct {
	mu     sync.Mutex
	events []docs.SyncEvent
	panic  bool
}

func (h *recordHook) Name() string { return "record" }
func (h *recordHook) OnSync(_ context.Context, ev docs.SyncEvent) error {
	if h.panic {
		panic("boom")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.events = append(h.events, ev)
	return nil
}

func (h *recordHook) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.events)
}

func (h *recordHook) last() docs.SyncEvent {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.events[len(h.events)-1]
}
