package catalog

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"getdocs/internal/domain/docs"
)

var key = docs.TreeKey{Namespace: "team", Tree: "api"}

var baseFiles = map[string]string{
	"README.md":             "# Project\n",
	"main.go":               "package main\n",
	"api/user.proto":        "syntax = \"proto3\";\n",
	"docs/README.md":        "# Docs\n\n![logo](logo.svg) [install](guide/install.md) [src](../main.go)\n",
	"docs/guide/install.md": "# Install\n\nrun it\n",
	"docs/drafts/wip.md":    "# WIP\n",
	"docs/secret.md":        "# Secret\ndraft: true\n",
	"docs/logo.svg":         "<svg/>",
	"docs/unused.png":       "png",
}

func newTestService(t *testing.T, spec *docs.TreeSpec) (*Service, *memRepo, *memStore, *recordHook) {
	t.Helper()
	repo := &memRepo{}
	repo.set("c1", baseFiles)
	store := newMemStore()
	if spec == nil {
		spec = &docs.TreeSpec{Key: key, Filter: docs.Filter{Exclude: []string{"drafts"}}}
	}
	spec.Key = key
	if spec.Source.Kind == "" {
		spec.Source.Kind = docs.SourceGit
	}
	svc := New(Deps{Fetcher: repo, Parser: fakeParser{}, Snapshots: store, States: store, Logger: discard},
		Options{KeepVersions: 2}, []*docs.Namespace{{Name: key.Namespace, Trees: []*docs.TreeSpec{spec}}})
	hook := &recordHook{}
	svc.AddHook(hook)
	return svc, repo, store, hook
}

func mustSync(t *testing.T, svc *Service, force bool) docs.SyncRun {
	t.Helper()
	run, err := svc.Sync(t.Context(), key, docs.TriggerManual, force)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	return run
}

func docPaths(s *docs.Snapshot) []string {
	var out []string
	for _, d := range s.Docs {
		out = append(out, d.Path)
	}
	slices.Sort(out)
	return out
}

func TestSyncLifecycle(t *testing.T) {
	svc, repo, store, hook := newTestService(t, nil)

	// 首次同步：自动探测 docs 目录，过滤草稿与排除目录，只收录被引用的资源。
	run := mustSync(t, svc, false)
	if run.Status != docs.RunSuccess || run.Docs != 2 {
		t.Fatalf("first run = %+v", run)
	}
	snap, err := svc.Snapshot(key)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Root != "docs" {
		t.Errorf("root = %q, want docs", snap.Root)
	}
	if got := docPaths(snap); !slices.Equal(got, []string{"README.md", "guide/install.md"}) {
		t.Errorf("docs = %v", got)
	}
	if len(snap.Assets) != 1 || snap.Assets[0].Path != "docs/logo.svg" {
		t.Errorf("assets = %+v", snap.Assets)
	}
	if data, err := svc.Asset(t.Context(), snap, "docs/logo.svg"); err != nil || string(data) != "<svg/>" {
		t.Errorf("asset = %q, %v", data, err)
	}
	if hook.count() != 1 || len(hook.last().Changes.Added) != 2 {
		t.Errorf("hook events = %d", hook.count())
	}

	// 提交未变：跳过构建。
	if run := mustSync(t, svc, false); run.Status != docs.RunUnchanged || run.Version != snap.Version {
		t.Errorf("same commit run = %+v", run)
	}
	fetches := repo.fetchN

	// 只改代码：构建后发现文档无变化，不产生新版本。
	code := withFile(baseFiles, "main.go", "package main // v2\n")
	repo.set("c2", code)
	if run := mustSync(t, svc, false); run.Status != docs.RunUnchanged {
		t.Errorf("code-only run = %+v", run)
	}
	if repo.fetchN != fetches+1 {
		t.Errorf("fetch not called")
	}
	if hook.count() != 1 {
		t.Errorf("hook called for unchanged sync")
	}

	// 修改并删除文档：产生新版本，变更集交给钩子。
	changed := withFile(code, "docs/README.md", "# Docs v2\n\n![logo](logo.svg)\n")
	delete(changed, "docs/guide/install.md")
	changed["docs/guide/config.md"] = "# Config\n"
	repo.set("c3", changed)
	run = mustSync(t, svc, false)
	if run.Status != docs.RunSuccess {
		t.Fatalf("doc change run = %+v", run)
	}
	cs := run.Changes
	if !slices.Equal(cs.Added, []string{"guide/config.md"}) || !slices.Equal(cs.Modified, []string{"README.md"}) ||
		!slices.Equal(cs.Deleted, []string{"guide/install.md"}) {
		t.Errorf("changes = %+v", cs)
	}
	ev := hook.last()
	if ev.Previous == nil || ev.Previous.Version != snap.Version || ev.Current.Version != run.Version {
		t.Errorf("hook event versions wrong")
	}
	if ev.Run.ID != run.ID {
		t.Errorf("hook event missing run")
	}
	if !ev.Current.HasAsset("docs/logo.svg") {
		t.Errorf("asset dropped after unrelated change")
	}

	// 强制同步：即使无变化也重建。
	if run := mustSync(t, svc, true); run.Status != docs.RunSuccess || !run.Force {
		t.Errorf("forced run = %+v", run)
	}
	versions, _ := svc.Versions(t.Context(), key)
	if len(versions) != 2 {
		t.Errorf("prune not applied, versions = %v", versions)
	}

	st, _ := svc.State(t.Context(), key)
	if st.LastRevision.Commit != "c3" || st.LastRun == nil || st.LastSuccessAt.IsZero() {
		t.Errorf("state = %+v", st)
	}
	if len(store.runs) != 5 {
		t.Errorf("runs recorded = %d, want 5", len(store.runs))
	}
}

func withFile(m map[string]string, k, v string) map[string]string {
	out := make(map[string]string, len(m))
	for a, b := range m {
		out[a] = b
	}
	out[k] = v
	return out
}

func TestSyncFailureKeepsSnapshot(t *testing.T) {
	svc, repo, _, _ := newTestService(t, nil)
	first := mustSync(t, svc, false)

	repo.mu.Lock()
	repo.err = errors.New("network down")
	repo.mu.Unlock()
	run, err := svc.Sync(t.Context(), key, docs.TriggerSchedule, false)
	if err == nil || run.Status != docs.RunFailed || !strings.Contains(run.Error, "network down") {
		t.Fatalf("run = %+v, err = %v", run, err)
	}
	snap, _ := svc.Snapshot(key)
	if snap.Version != first.Version {
		t.Errorf("snapshot replaced after failed sync")
	}
	st, _ := svc.State(t.Context(), key)
	if st.LastRevision.Commit != "c1" || st.LastRun.Status != docs.RunFailed {
		t.Errorf("state after failure = %+v", st)
	}
}

func TestSyncConfigChangeRebuilds(t *testing.T) {
	spec := &docs.TreeSpec{Filter: docs.Filter{Exclude: []string{"drafts"}}}
	svc, _, _, _ := newTestService(t, spec)
	mustSync(t, svc, false)

	spec.Filter.Exclude = nil // 配置变更（如通过重载）使指纹变化
	run := mustSync(t, svc, false)
	if run.Status != docs.RunSuccess || !slices.Equal(run.Changes.Added, []string{"drafts/wip.md"}) {
		t.Errorf("run after filter change = %+v", run)
	}
}

func TestBuildRootVariants(t *testing.T) {
	cases := []struct {
		root    string
		want    []string
		wantErr string
	}{
		{root: "docs/guide/install.md", want: []string{"install.md"}},
		{root: "/docs/guide/", want: []string{"install.md"}},
		{root: ".", want: []string{"README.md", "docs/README.md", "docs/drafts/wip.md", "docs/guide/install.md"}},
		{root: "missing", wantErr: "not found"},
		{root: "../etc", wantErr: "escapes"},
		{root: "main.go", wantErr: "not a markdown"},
		{root: "api/user.proto", wantErr: "not a markdown"},
	}
	for _, c := range cases {
		svc, _, _, _ := newTestService(t, &docs.TreeSpec{Root: c.root})
		run, err := svc.Sync(t.Context(), key, docs.TriggerManual, false)
		if c.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("root %q: err = %v, want %q", c.root, err, c.wantErr)
			}
			continue
		}
		if err != nil {
			t.Errorf("root %q: %v", c.root, err)
			continue
		}
		snap, _ := svc.Snapshot(key)
		if got := docPaths(snap); !slices.Equal(got, c.want) {
			t.Errorf("root %q: docs = %v, want %v (run %+v)", c.root, got, c.want, run)
		}
	}
}

func TestBuildSkipsBadDocuments(t *testing.T) {
	svc, repo, _, _ := newTestService(t, &docs.TreeSpec{Root: "docs", Filter: docs.Filter{MaxFileSize: 64}})
	files := withFile(baseFiles, "docs/big.md", "# Big\n"+strings.Repeat("x", 100))
	files["docs/broken.md"] = "!invalid"
	repo.set("c1", files)
	mustSync(t, svc, false)
	snap, _ := svc.Snapshot(key)
	if got := docPaths(snap); slices.Contains(got, "big.md") || slices.Contains(got, "broken.md") {
		t.Errorf("bad documents included: %v", got)
	}
}

func TestHookPanicIsIsolated(t *testing.T) {
	svc, _, _, hook := newTestService(t, nil)
	bad := &recordHook{panic: true}
	svc.AddHook(bad)
	run := mustSync(t, svc, false)
	if run.Status != docs.RunSuccess || len(run.HookErrors) != 1 || !strings.Contains(run.HookErrors[0], "panic") {
		t.Errorf("run = %+v", run)
	}
	if hook.count() != 1 {
		t.Errorf("healthy hook not called")
	}
}

func TestRestore(t *testing.T) {
	svc, repo, store, _ := newTestService(t, nil)
	first := mustSync(t, svc, false)

	// 新进程共享同一存储：启动时恢复快照并通知钩子（用于重建内存索引）。
	other := New(Deps{Fetcher: repo, Parser: fakeParser{}, Snapshots: store, States: store, Logger: discard},
		Options{}, svc.Namespaces())
	hook := &recordHook{}
	other.AddHook(hook)
	other.Restore(t.Context())
	snap, err := other.Snapshot(key)
	if err != nil || snap.Version != first.Version {
		t.Fatalf("restored = %v, %v", snap, err)
	}
	if hook.count() != 1 || !hook.last().Restored {
		t.Errorf("restore did not notify hooks")
	}

	// 版本未变时不重复加载。
	loads := store.loadsN
	other.Restore(t.Context())
	if store.loadsN != loads || hook.count() != 1 {
		t.Errorf("unchanged restore reloaded snapshot")
	}

	// 另一个进程同步出新版本后，Restore 跟随更新。
	repo.set("c2", withFile(baseFiles, "docs/new.md", "# New\n"))
	second := mustSync(t, svc, false)
	other.Restore(t.Context())
	if snap, _ := other.Snapshot(key); snap.Version != second.Version {
		t.Errorf("restore did not pick up new version")
	}
}

func TestConcurrentSync(t *testing.T) {
	svc, repo, store, _ := newTestService(t, nil)
	gate := make(chan struct{})
	repo.mu.Lock()
	repo.gate = gate
	repo.mu.Unlock()

	if queued, err := svc.SyncAsync(t.Context(), key, docs.TriggerWebhook, false); err != nil || queued {
		t.Fatalf("SyncAsync = %v, %v", queued, err)
	}
	waitFor(t, func() bool { return svc.Running(key) != nil })

	if _, err := svc.Sync(t.Context(), key, docs.TriggerManual, false); !errors.Is(err, docs.ErrSyncInProgress) {
		t.Errorf("concurrent Sync err = %v", err)
	}
	// 同步期间到达的请求合并为一次后续同步。
	for range 3 {
		if queued, err := svc.SyncAsync(t.Context(), key, docs.TriggerWebhook, true); err != nil || !queued {
			t.Fatalf("expected queued, got %v, %v", queued, err)
		}
	}
	close(gate)
	waitFor(t, func() bool {
		store.mu.Lock()
		defer store.mu.Unlock()
		return len(store.runs) == 2
	})
	if err := svc.Wait(t.Context()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.runs) != 2 || !store.runs[1].Force {
		t.Errorf("runs = %+v", store.runs)
	}
}

func TestSyncUnknownTree(t *testing.T) {
	svc, _, _, _ := newTestService(t, nil)
	if _, err := svc.Sync(t.Context(), docs.TreeKey{Namespace: "x", Tree: "y"}, docs.TriggerManual, false); !errors.Is(err, docs.ErrNotFound) {
		t.Errorf("err = %v", err)
	}
	if _, err := svc.SyncAsync(t.Context(), docs.TreeKey{Namespace: "x", Tree: "y"}, docs.TriggerManual, false); !errors.Is(err, docs.ErrNotFound) {
		t.Errorf("async err = %v", err)
	}
}

func TestSyncAllAndCancel(t *testing.T) {
	svc, repo, _, _ := newTestService(t, nil)
	repo.mu.Lock()
	repo.gate = make(chan struct{}) // 永不放行
	repo.mu.Unlock()
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	results := svc.SyncAll(ctx, nil, docs.TriggerCLI, false)
	if len(results) != 1 || results[0].Err == "" || results[0].Run.Status != docs.RunFailed {
		t.Errorf("results = %+v", results)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// 开启 Proto 后 .proto 文件进入文档树，保存的是解析器转换得到的 markdown。
func TestBuildProto(t *testing.T) {
	for _, c := range []struct {
		spec *docs.TreeSpec
		want []string
	}{
		{&docs.TreeSpec{Root: "api", Filter: docs.Filter{Proto: true}}, []string{"user.proto"}},
		{&docs.TreeSpec{Root: "api/user.proto", Filter: docs.Filter{Proto: true}}, []string{"user.proto"}},
		{&docs.TreeSpec{Root: ".", Filter: docs.Filter{Proto: true, Include: []string{"api/**", "README.md"}}}, []string{"README.md", "api/user.proto"}},
	} {
		svc, _, store, _ := newTestService(t, c.spec)
		mustSync(t, svc, false)
		snap, _ := svc.Snapshot(key)
		if got := docPaths(snap); !slices.Equal(got, c.want) {
			t.Errorf("root %q: docs = %v, want %v", c.spec.Root, got, c.want)
			continue
		}
		d := snap.Lookup(strings.TrimSuffix(c.want[len(c.want)-1], ".proto"))
		if d == nil || string(d.Content) != "# converted user.proto\n" || d.Title != "converted user.proto" {
			t.Errorf("root %q: proto doc = %+v", c.spec.Root, d)
			continue
		}
		if string(store.blobs[d.Hash]) != string(d.Content) {
			t.Errorf("root %q: blob does not hold converted markdown", c.spec.Root)
		}
	}

	// 未开启时目录下没有可收录的文档。
	svc, _, _, _ := newTestService(t, &docs.TreeSpec{Root: "api"})
	if _, err := svc.Sync(t.Context(), key, docs.TriggerManual, false); err == nil || !strings.Contains(err.Error(), "no documents") {
		t.Errorf("err = %v", err)
	}
}
