package mcp

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"getdocs/internal/application/catalog"
	"getdocs/internal/application/search"
	"getdocs/internal/domain/docs"
	"getdocs/internal/infra/git"
	"getdocs/internal/infra/markdown"
	"getdocs/internal/infra/storage"
)

func TestExtractSection(t *testing.T) {
	body := "# Title\n\nintro\n\n## Install\n\nstep\n\n```sh\n# not a heading\n```\n\n### Linux\n\napt\n\n## Usage\n\nrun\n\n## Install\n\nagain\n"
	headings := []docs.Heading{
		{Level: 1, Text: "Title", ID: "title"},
		{Level: 2, Text: "Install", ID: "install"},
		{Level: 3, Text: "Linux", ID: "linux"},
		{Level: 2, Text: "Usage", ID: "usage"},
		{Level: 2, Text: "Install", ID: "install-1"},
	}
	cases := map[string]string{
		"install":   "## Install\n\nstep\n\n```sh\n# not a heading\n```\n\n### Linux\n\napt\n",
		"linux":     "### Linux\n\napt\n",
		"usage":     "## Usage\n\nrun\n",
		"install-1": "## Install\n\nagain\n",
	}
	for anchor, want := range cases {
		got, ok := extractSection(body, headings, anchor)
		if !ok || got != want {
			t.Errorf("extractSection(%q) = %q, %v\nwant %q", anchor, got, ok, want)
		}
	}
	if full, ok := extractSection(body, headings, "title"); !ok || !strings.HasSuffix(full, "again\n") {
		t.Errorf("top-level section should span the document, got %q", full)
	}
	if _, ok := extractSection(body, headings, "missing"); ok {
		t.Errorf("missing anchor reported found")
	}
}

func TestHeadingLevel(t *testing.T) {
	for line, want := range map[string]int{"# a": 1, "### a": 3, "#a": 0, "####### a": 0, "######": 6, "text": 0} {
		if got := headingLevel(line); got != want {
			t.Errorf("headingLevel(%q) = %d, want %d", line, got, want)
		}
	}
}

func setup(t *testing.T, allowSync bool) *mcp.ClientSession {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	write(t, src, "README.md", "# Home\n\nWelcome to the **demo**.\n")
	write(t, src, "guide/install.md", "---\ntitle: 安装指南\n---\n\n## 准备\n\n需要 Go 1.26。\n\n## Steps\n\nRun `make install`.\n")

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store, err := storage.NewFS(filepath.Join(dir, "data"))
	if err != nil {
		t.Fatal(err)
	}
	fetcher, err := git.NewFetcher(filepath.Join(dir, "repos"), 0, logger)
	if err != nil {
		t.Fatal(err)
	}
	key := docs.TreeKey{Namespace: "demo", Tree: "local"}
	svc := catalog.New(catalog.Deps{Fetcher: fetcher, Parser: markdown.New(), Snapshots: store, States: store, Logger: logger},
		catalog.Options{}, []*docs.Namespace{{Name: "demo", Title: "Demo", Trees: []*docs.TreeSpec{
			{Key: key, Title: "Local", Source: docs.Source{Kind: docs.SourceLocal, URL: src}},
		}}})
	ix := search.NewIndex()
	svc.AddHook(ix)
	if _, err := svc.Sync(t.Context(), key, docs.TriggerManual, false); err != nil {
		t.Fatal(err)
	}

	s := New(svc, ix, "https://docs.example.com", "test")
	if allowSync {
		s.RegisterSync(t.Context())
	}
	st, ct := mcp.NewInMemoryTransports()
	if _, err := s.MCPServer().Connect(t.Context(), st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(t.Context(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func call(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String(), res.IsError
}

func TestTools(t *testing.T) {
	cs := setup(t, false)

	tools, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
	}
	if strings.Contains(strings.Join(names, ","), "sync_tree") {
		t.Errorf("sync_tree exposed without allowSync: %v", names)
	}

	if out, isErr := call(t, cs, "list_trees", nil); isErr || !strings.Contains(out, "tree: local") || strings.Contains(out, "/src") {
		t.Errorf("list_trees = %q", out)
	}
	if out, isErr := call(t, cs, "get_tree", map[string]any{"namespace": "demo", "tree": "local"}); isErr || !strings.Contains(out, "安装指南") {
		t.Errorf("get_tree = %q", out)
	}
	if out, isErr := call(t, cs, "search_docs", map[string]any{"query": "安装 Go"}); isErr || !strings.Contains(out, "guide/install") {
		t.Errorf("search_docs = %q", out)
	}

	out, isErr := call(t, cs, "read_doc", map[string]any{"namespace": "demo", "tree": "local", "path": "guide/install", "section": "steps"})
	if isErr || !strings.Contains(out, "make install") || strings.Contains(out, "需要 Go") {
		t.Errorf("read_doc section = %q", out)
	}
	if !strings.Contains(out, "url: https://docs.example.com/docs/demo/local/guide/install#steps") {
		t.Errorf("read_doc header missing url: %q", out)
	}
	if out, isErr := call(t, cs, "read_doc", map[string]any{"namespace": "demo", "tree": "local", "path": "nope"}); !isErr || !strings.Contains(out, "get_tree") {
		t.Errorf("missing doc should be a tool error with a hint, got %q", out)
	}

	res, err := cs.ReadResource(t.Context(), &mcp.ReadResourceParams{URI: "docs://demo/local/README.md"})
	if err != nil || len(res.Contents) != 1 || !strings.Contains(res.Contents[0].Text, "Welcome") {
		t.Errorf("read resource = %+v, %v", res, err)
	}
}

func TestSyncTool(t *testing.T) {
	cs := setup(t, true)
	out, isErr := call(t, cs, "sync_tree", map[string]any{"namespace": "demo", "tree": "local", "force": true})
	if isErr || !strings.Contains(out, "success") {
		t.Errorf("sync_tree = %q", out)
	}
}
