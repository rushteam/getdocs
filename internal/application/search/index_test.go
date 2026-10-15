package search

import (
	"slices"
	"strings"
	"testing"

	"getdocs/internal/domain/docs"
)

func TestTokenize(t *testing.T) {
	got := Tokenize("Install getdocs_v2 安装指南!")
	want := []string{"install", "getdocs_v2", "安", "装", "安装", "指", "装指", "南", "指南"}
	if !slices.Equal(got, want) {
		t.Errorf("Tokenize = %v, want %v", got, want)
	}
}

func snapshot(key docs.TreeKey, list ...*docs.Document) *docs.Snapshot {
	s := &docs.Snapshot{Key: key, Version: "v1", Root: ".", Docs: list}
	s.Seal()
	return s
}

func TestSearch(t *testing.T) {
	a := docs.TreeKey{Namespace: "team", Tree: "api"}
	b := docs.TreeKey{Namespace: "other", Tree: "wiki"}
	ix := NewIndex()
	_ = ix.OnSync(t.Context(), docs.SyncEvent{Key: a, Current: snapshot(a,
		&docs.Document{Path: "install.md", Title: "安装指南", Sections: []docs.Section{
			{Heading: "准备", Anchor: "prep", Path: []string{"准备"}, Text: "需要先安装 Go 与 git。"},
			{Heading: "Deploy", Anchor: "deploy", Path: []string{"Deploy"}, Text: "Use kubectl apply to deploy the service."},
		}},
		&docs.Document{Path: "faq.md", Title: "FAQ", Sections: []docs.Section{
			{Text: "Unrelated text about billing and invoices."},
		}},
	)})
	_ = ix.OnSync(t.Context(), docs.SyncEvent{Key: b, Current: snapshot(b,
		&docs.Document{Path: "deploy.md", Title: "Deploy", Sections: []docs.Section{{Text: "deploy deploy deploy"}}},
	)})

	hits, err := ix.Search(t.Context(), docs.SearchQuery{Text: "安装"})
	if err != nil || len(hits) == 0 || hits[0].Path != "install.md" || hits[0].Anchor != "prep" {
		t.Fatalf("CJK search = %+v, %v", hits, err)
	}
	if !strings.Contains(hits[0].Snippet, "安装") {
		t.Errorf("snippet = %q", hits[0].Snippet)
	}

	hits, _ = ix.Search(t.Context(), docs.SearchQuery{Text: "deploy", Namespace: "team"})
	if len(hits) != 1 || hits[0].Key != a || hits[0].Heading != "Deploy" {
		t.Errorf("namespace-filtered search = %+v", hits)
	}
	hits, _ = ix.Search(t.Context(), docs.SearchQuery{Text: "DEPLOY"})
	if len(hits) != 2 {
		t.Errorf("cross-tree search = %+v", hits)
	}
	if hits, _ := ix.Search(t.Context(), docs.SearchQuery{Text: "  ?? "}); hits != nil {
		t.Errorf("empty query returned %v", hits)
	}

	// 新快照替换旧索引。
	_ = ix.OnSync(t.Context(), docs.SyncEvent{Key: a, Current: snapshot(a, &docs.Document{Path: "x.md", Title: "X", Sections: []docs.Section{{Text: "nothing"}}})})
	if hits, _ := ix.Search(t.Context(), docs.SearchQuery{Text: "安装"}); len(hits) != 0 {
		t.Errorf("stale index entries: %+v", hits)
	}
}

func TestSearchLimitsPerDoc(t *testing.T) {
	k := docs.TreeKey{Namespace: "n", Tree: "t"}
	var secs []docs.Section
	for i := range 6 {
		secs = append(secs, docs.Section{Heading: "go", Anchor: string(rune('a' + i)), Text: "go go"})
	}
	ix := NewIndex()
	_ = ix.OnSync(t.Context(), docs.SyncEvent{Key: k, Current: snapshot(k,
		&docs.Document{Path: "long.md", Title: "Long", Sections: secs},
		&docs.Document{Path: "short.md", Title: "Short", Sections: []docs.Section{{Text: "go"}}},
	)})
	hits, _ := ix.Search(t.Context(), docs.SearchQuery{Text: "go", Limit: 10})
	if len(hits) != 4 {
		t.Errorf("hits = %d, want 3 from long.md + 1 from short.md", len(hits))
	}
}

func TestSnippet(t *testing.T) {
	text := strings.Repeat("前文内容。", 30) + "这里讲解如何配置 webhook 推送。" + strings.Repeat("后文。", 30)
	s := Snippet(text, []string{"webhook"}, 40)
	if !strings.Contains(s, "webhook") || !strings.HasPrefix(s, "…") || !strings.HasSuffix(s, "…") {
		t.Errorf("Snippet = %q", s)
	}
	if got := Snippet("short", []string{"x"}, 40); got != "short" {
		t.Errorf("Snippet short = %q", got)
	}
}
