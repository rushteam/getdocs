package storage

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"getdocs/internal/domain/docs"
)

var key = docs.TreeKey{Namespace: "ns", Tree: "t"}

func snapshotOf(version string, files map[string]string) (*docs.Snapshot, []docs.Blob) {
	snap := &docs.Snapshot{Key: key, Version: version, Root: "."}
	var blobs []docs.Blob
	for _, p := range slices.Sorted(maps.Keys(files)) {
		data := []byte(files[p])
		h := fmt.Sprintf("h%x", data)
		blobs = append(blobs, docs.Blob{Hash: h, Data: data})
		if docs.IsMarkdown(p) {
			snap.Docs = append(snap.Docs, &docs.Document{Path: p, Title: p, Hash: h, Size: int64(len(data))})
		} else {
			snap.Assets = append(snap.Assets, docs.Asset{Path: p, Hash: h, Size: int64(len(data))})
		}
	}
	snap.Seal()
	return snap, blobs
}

func TestSnapshotRoundTrip(t *testing.T) {
	ctx := t.Context()
	s, err := NewFS(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Current(ctx, key); !errors.Is(err, docs.ErrNotFound) {
		t.Fatalf("Current on empty store = %v", err)
	}
	if vs, err := s.Versions(ctx, key); err != nil || vs != nil {
		t.Fatalf("Versions on empty store = %v, %v", vs, err)
	}

	snap, blobs := snapshotOf("v1", map[string]string{"a.md": "# A", "guide/b.md": "# B", "img.png": "PNG"})
	if err := s.Save(ctx, snap, blobs); err != nil {
		t.Fatal(err)
	}
	got, err := s.Current(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != "v1" || len(got.Docs) != 2 || string(got.Lookup("guide/b").Content) != "# B" {
		t.Errorf("loaded snapshot = %+v", got)
	}
	if b, err := s.ReadBlob(ctx, got.Assets[0].Hash); err != nil || string(b) != "PNG" {
		t.Errorf("ReadBlob = %q, %v", b, err)
	}
	for _, bad := range []string{"", "../x", "a/b", "."} {
		if _, err := s.ReadBlob(ctx, bad); !errors.Is(err, docs.ErrNotFound) {
			t.Errorf("ReadBlob(%q) = %v", bad, err)
		}
	}
	for _, bad := range []string{"../v1", "a/b", ".hidden"} {
		if _, err := s.Load(ctx, key, bad); !errors.Is(err, docs.ErrNotFound) {
			t.Errorf("Load(%q) = %v", bad, err)
		}
	}
}

func TestPruneCollectsUnreferencedBlobs(t *testing.T) {
	ctx := t.Context()
	root := t.TempDir()
	s, _ := NewFS(root)

	// 另一棵文档树引用的内容不能被清理。
	other := docs.TreeKey{Namespace: "ns", Tree: "other"}
	shared, sharedBlobs := snapshotOf("v1", map[string]string{"shared.md": "old"})
	shared.Key = other
	if err := s.Save(ctx, shared, sharedBlobs); err != nil {
		t.Fatal(err)
	}
	for i, content := range []string{"old", "mid", "new"} {
		snap, blobs := snapshotOf(fmt.Sprintf("v%d", i+1), map[string]string{"a.md": content, "keep.md": "same"})
		if err := s.Save(ctx, snap, blobs); err != nil {
			t.Fatal(err)
		}
	}
	if vs, _ := s.Versions(ctx, key); !slices.Equal(vs, []string{"v3", "v2", "v1"}) {
		t.Fatalf("Versions = %v", vs)
	}
	if v, _ := s.CurrentVersion(ctx, key); v != "v3" {
		t.Fatalf("CurrentVersion = %q", v)
	}

	if err := s.Prune(ctx, key, 2); err != nil {
		t.Fatal(err)
	}
	if vs, _ := s.Versions(ctx, key); !slices.Equal(vs, []string{"v3", "v2"}) {
		t.Errorf("Versions after prune = %v", vs)
	}
	exists := func(content string) bool {
		_, err := s.ReadBlob(ctx, fmt.Sprintf("h%x", content))
		return err == nil
	}
	for content, want := range map[string]bool{"old": true, "mid": true, "new": true, "same": true} {
		if exists(content) != want {
			t.Errorf("blob %q exists = %v, want %v", content, !want, want)
		}
	}

	if err := s.Prune(ctx, key, 1); err != nil {
		t.Fatal(err)
	}
	if exists("mid") {
		t.Errorf("unreferenced blob survived prune")
	}
	if !exists("old") || !exists("new") || !exists("same") {
		t.Errorf("referenced blob was removed")
	}
	if _, err := s.Load(ctx, key, "v3"); err != nil {
		t.Errorf("current version unreadable after prune: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "trees", "ns", "t", "versions", "v1.json")); !os.IsNotExist(err) {
		t.Errorf("old manifest not removed: %v", err)
	}
}

func TestStateAndRuns(t *testing.T) {
	ctx := t.Context()
	s, _ := NewFS(t.TempDir())

	st, err := s.State(ctx, key)
	if err != nil || st.Key != key || st.LastFingerprint != "" {
		t.Fatalf("empty state = %+v, %v", st, err)
	}
	st.LastFingerprint = "fp"
	st.LastRevision = docs.Revision{Commit: "abc"}
	if err := s.SaveState(ctx, st); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.State(ctx, key); got.LastFingerprint != "fp" || got.LastRevision.Commit != "abc" {
		t.Errorf("state = %+v", got)
	}

	for i := range maxRuns + 5 {
		if err := s.AppendRun(ctx, docs.SyncRun{ID: fmt.Sprint(i), Key: key, Status: docs.RunSuccess}); err != nil {
			t.Fatal(err)
		}
	}
	runs, err := s.Runs(ctx, key, 3)
	if err != nil || len(runs) != 3 || runs[0].ID != fmt.Sprint(maxRuns+4) || runs[2].ID != fmt.Sprint(maxRuns+2) {
		t.Errorf("latest runs = %+v, %v", runs, err)
	}
	all, _ := s.Runs(ctx, key, 0)
	if len(all) != maxRuns || all[len(all)-1].ID != "5" {
		t.Errorf("history not capped: %d runs, oldest %q", len(all), all[len(all)-1].ID)
	}
}

func TestSafeName(t *testing.T) {
	for in, want := range map[string]string{"a/b": "a_b", "..x": "x", `a\b`: "a_b", "中文": "中文"} {
		if got := safeName(in); got != want {
			t.Errorf("safeName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFileRegistry(t *testing.T) {
	r := NewFileRegistry(filepath.Join(t.TempDir(), "registry.json"))
	if trees, err := r.Load(t.Context()); err != nil || len(trees) != 0 {
		t.Fatalf("empty load = %v, %v", trees, err)
	}
	want := []docs.RegisteredTree{{Namespace: "ns", Name: "t", Repo: "https://github.com/a/b.git", Credential: "gh"}}
	if err := r.Save(t.Context(), want); err != nil {
		t.Fatal(err)
	}
	got, err := r.Load(t.Context())
	if err != nil || len(got) != 1 || got[0].Key() != want[0].Key() || got[0].Credential != "gh" {
		t.Fatalf("load = %+v, %v", got, err)
	}
}
