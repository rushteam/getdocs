package catalog

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/rushteam/getdocs/internal/domain/docs"
)

type memRegistry struct {
	mu    sync.Mutex
	trees []docs.RegisteredTree
}

func (m *memRegistry) Load(context.Context) ([]docs.RegisteredTree, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]docs.RegisteredTree(nil), m.trees...), nil
}

func (m *memRegistry) Save(_ context.Context, trees []docs.RegisteredTree) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.trees = append([]docs.RegisteredTree(nil), trees...)
	return nil
}

func TestAddRemoveTree(t *testing.T) {
	reg := &memRegistry{}
	newSvc := func() *Service {
		return New(Deps{Fetcher: &memRepo{}, Parser: fakeParser{}, Snapshots: newMemStore(), States: newMemStore(), Logger: discard, Registry: reg},
			Options{Credentials: map[string]docs.Auth{"gh": {Password: "tok"}}},
			[]*docs.Namespace{{Name: key.Namespace, Trees: []*docs.TreeSpec{{Key: key, Source: docs.Source{Kind: docs.SourceGit}}}}})
	}
	svc := newSvc()
	hook := &recordHook{}
	svc.AddHook(hook)
	ctx := t.Context()

	for name, tr := range map[string]docs.RegisteredTree{
		"config conflict":    {Namespace: key.Namespace, Name: key.Tree, Repo: "https://github.com/a/b.git"},
		"local path":         {Namespace: "x", Name: "y", Repo: "/etc"},
		"unknown credential": {Namespace: "x", Name: "y", Repo: "https://github.com/a/b.git", Credential: "none"},
		"bad branch":         {Namespace: "x", Name: "y", Repo: "https://github.com/a/b.git", Branch: "--upload-pack=x"},
	} {
		if _, err := svc.AddTree(ctx, tr); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}

	spec, err := svc.AddTree(ctx, docs.RegisteredTree{Namespace: "new", Name: "docs", Repo: " https://github.com/a/b.git ", Credential: "gh"})
	if err != nil {
		t.Fatal(err)
	}
	if spec.Source.Auth.Username != "x-access-token" || spec.Source.Provider != docs.ProviderGitHub || !spec.AutoSync {
		t.Fatalf("spec = %+v", spec.Source)
	}
	if _, err := svc.AddTree(ctx, docs.RegisteredTree{Namespace: "new", Name: "docs", Repo: "https://github.com/a/c.git"}); !errors.Is(err, docs.ErrConflict) {
		t.Fatalf("duplicate add err = %v", err)
	}
	if ns, err := svc.Namespace("new"); err != nil || len(ns.Trees) != 1 {
		t.Fatalf("auto-created namespace = %+v, %v", ns, err)
	}

	// 新进程从存储恢复登记项。
	other := newSvc()
	if err := other.LoadRegistry(ctx); err != nil {
		t.Fatal(err)
	}
	if !other.Registered(spec.Key) || other.Registered(key) {
		t.Fatal("registry not restored")
	}

	if err := svc.RemoveTree(ctx, key); !errors.Is(err, docs.ErrInvalid) {
		t.Fatalf("remove config tree err = %v", err)
	}
	if err := svc.RemoveTree(ctx, spec.Key); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Namespace("new"); !errors.Is(err, docs.ErrNotFound) {
		t.Fatalf("empty registered namespace kept: %v", err)
	}
	if len(reg.trees) != 0 {
		t.Fatalf("registry = %+v", reg.trees)
	}
	if ev := hook.last(); hook.count() != 1 || !ev.Removed || ev.Key != spec.Key {
		t.Fatalf("hook events = %d, last %+v", hook.count(), ev)
	}
}
