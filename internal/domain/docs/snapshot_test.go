package docs

import (
	"slices"
	"strings"
	"testing"
)

func newSnap(root string, docs ...*Document) *Snapshot {
	s := &Snapshot{Key: TreeKey{"ns", "tree"}, Version: "v1", Root: root, Docs: docs}
	s.Seal()
	return s
}

func doc(p, title string, weight int) *Document {
	return &Document{Path: p, Title: title, Weight: weight, Hash: "h-" + p}
}

func slugs(list []*Document) []string {
	var out []string
	for _, d := range list {
		out = append(out, d.Path)
	}
	return out
}

func TestSealOrderAndSlugs(t *testing.T) {
	s := newSnap("docs",
		doc("guide/install.md", "Install", 0),
		doc("README.md", "Home", 0),
		doc("guide/README.md", "Guide", 0),
		doc("guide.md", "Guide page", 0), // 与目录首页 slug 冲突
		doc("faq.md", "FAQ", 0),
		doc("changelog.md", "Changelog", 10),
		doc("api/index.md", "API", 1),
	)
	want := []string{"README.md", "api/index.md", "changelog.md", "faq.md", "guide.md", "guide/README.md", "guide/install.md"}
	if got := slugs(s.Docs); !slices.Equal(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
	if d := s.DocBySlug("guide"); d == nil || d.Path != "guide/README.md" {
		t.Errorf("directory index should own slug 'guide', got %+v", d)
	}
	if d := s.DocByPath("guide.md"); d.Slug != "guide.md" {
		t.Errorf("conflicting doc slug = %q, want fallback to path", d.Slug)
	}
	if d := s.DocBySlug(""); d == nil || d.Path != "README.md" {
		t.Errorf("root index slug missing")
	}

	nav := s.Nav()
	if nav.Doc() == nil || nav.Doc().Path != "README.md" {
		t.Errorf("root index not attached to root nav node")
	}
	var guide *NavNode
	for _, c := range nav.Children {
		if c.IsDir && c.Name == "guide" {
			guide = c
		}
	}
	if guide == nil || guide.Title != "Guide" || guide.Slug != "guide" || len(guide.Children) != 1 {
		t.Fatalf("guide dir node = %+v", guide)
	}
	if !nav.Contains("guide/install") || nav.Contains("missing") {
		t.Errorf("Contains misreported")
	}

	prev, next := s.Neighbors(s.DocByPath("faq.md"))
	if prev.Path != "changelog.md" || next.Path != "guide.md" {
		t.Errorf("Neighbors = %v, %v", prev.Path, next.Path)
	}
}

func TestLookup(t *testing.T) {
	s := newSnap(".", doc("README.md", "", 0), doc("guide/install.md", "", 0), doc("guide/README.md", "", 0), doc("api/user.proto", "", 0))
	for ref, want := range map[string]string{
		"":                 "README.md",
		".":                "README.md",
		"/guide/":          "guide/README.md",
		"guide/install":    "guide/install.md",
		"guide/install.md": "guide/install.md",
		"guide/README.md":  "guide/README.md",
		"api/user":         "api/user.proto",
		"api/user.proto":   "api/user.proto",
	} {
		if d := s.Lookup(ref); d == nil || d.Path != want {
			t.Errorf("Lookup(%q) = %v, want %s", ref, d, want)
		}
	}
	if s.Lookup("nope") != nil {
		t.Errorf("Lookup of missing doc should be nil")
	}
}

func TestResolveLink(t *testing.T) {
	s := newSnap("docs", doc("README.md", "", 0), doc("guide/install.md", "", 0))
	s.Assets = []Asset{{Path: "docs/logo.svg"}, {Path: "images/shot.png"}}
	s.Seal()
	from := s.DocByPath("guide/install.md")

	cases := []struct {
		dest string
		kind LinkKind
		repo string
		frag string
	}{
		{"#usage", LinkAnchor, "", "usage"},
		{"https://example.com/x.md", LinkExternal, "", ""},
		{"//cdn.example.com/a.png", LinkExternal, "", ""},
		{"mailto:a@b.c", LinkExternal, "", ""},
		{"../../../outside.md", LinkExternal, "", ""},
		{"../README.md#top", LinkDoc, "", "top"},
		{"/docs/guide/install.md", LinkDoc, "", ""},
		{"../logo.svg", LinkAsset, "docs/logo.svg", ""},
		{"../../images/shot.png", LinkAsset, "images/shot.png", ""},
		{"../../src/main.go#L10", LinkRepoFile, "src/main.go", "L10"},
	}
	for _, c := range cases {
		got := s.ResolveLink(from, c.dest)
		if got.Kind != c.kind || got.RepoPath != c.repo || got.Fragment != c.frag {
			t.Errorf("ResolveLink(%q) = %+v, want kind=%d repo=%q frag=%q", c.dest, got, c.kind, c.repo, c.frag)
		}
	}
	if got := s.ResolveLink(from, "../README.md"); got.Doc == nil || got.Doc.Path != "README.md" {
		t.Errorf("doc link target = %+v", got.Doc)
	}
}

func TestDiff(t *testing.T) {
	prev := newSnap(".", doc("a.md", "", 0), doc("b.md", "", 0), doc("c.md", "", 0))
	b := doc("b.md", "", 0)
	b.Hash = "changed"
	cur := newSnap(".", doc("a.md", "", 0), b, doc("d.md", "", 0))

	cs := Diff(prev, cur)
	if !slices.Equal(cs.Added, []string{"d.md"}) || !slices.Equal(cs.Modified, []string{"b.md"}) ||
		!slices.Equal(cs.Deleted, []string{"c.md"}) || cs.Unchanged != 1 || cs.AssetsChanged {
		t.Errorf("Diff = %+v", cs)
	}
	if cs.Empty() {
		t.Errorf("non-empty change set reported empty")
	}
	if !Diff(cur, cur).Empty() {
		t.Errorf("self diff should be empty")
	}

	first := Diff(nil, cur)
	if len(first.Added) != 3 || first.Unchanged != 0 {
		t.Errorf("initial diff = %+v", first)
	}

	cur.Assets = []Asset{{Path: "x.png", Hash: "1"}}
	cur.Seal()
	if !Diff(prev, cur).AssetsChanged {
		t.Errorf("asset change not detected")
	}
}

func sectionDoc(p string, texts ...string) *Document {
	d := doc(p, "Title", 0)
	for i, tx := range texts {
		d.Sections = append(d.Sections, Section{Heading: "H", Anchor: string(rune('a' + i)), Path: []string{"H"}, Text: tx})
	}
	return d
}

func TestChunkDocument(t *testing.T) {
	s := newSnap(".", sectionDoc("a.md", "short", "  ", strings.Repeat("段落。", 40)+"\n\n"+strings.Repeat("x", 30)))
	chunks := ChunkDocument(s, s.DocByPath("a.md"), 50)
	if len(chunks) < 3 {
		t.Fatalf("expected long section to be split, got %d chunks", len(chunks))
	}
	if chunks[0].Text != "Title > H\n\nshort" || chunks[0].Anchor != "a" {
		t.Errorf("first chunk = %+v", chunks[0])
	}
	seen := map[string]bool{}
	for _, c := range chunks {
		if seen[c.ID] {
			t.Errorf("duplicate chunk id %s", c.ID)
		}
		seen[c.ID] = true
		body := strings.TrimPrefix(c.Text, "Title > H\n\n")
		if n := len([]rune(body)); n > 50 {
			t.Errorf("chunk body has %d runes, exceeds limit", n)
		}
	}
	again := ChunkDocument(s, s.DocByPath("a.md"), 50)
	if again[1].ID != chunks[1].ID || again[1].Hash != chunks[1].Hash {
		t.Errorf("chunking is not deterministic")
	}
}

func TestDiffChunks(t *testing.T) {
	prev := newSnap(".", sectionDoc("a.md", "one", "two"), sectionDoc("gone.md", "bye"), sectionDoc("same.md", "keep"))
	prev.Version = "v1"
	a := sectionDoc("a.md", "one", "TWO")
	a.Hash = "changed"
	cur := newSnap(".", a, sectionDoc("new.md", "hello"), sectionDoc("same.md", "keep"))
	cur.Version = "v2"

	delta := DiffChunks(prev, cur, 0)
	if delta.FromVersion != "v1" || delta.ToVersion != "v2" {
		t.Errorf("versions = %s → %s", delta.FromVersion, delta.ToVersion)
	}
	var upserted []string
	for _, c := range delta.Upsert {
		upserted = append(upserted, c.Path+"#"+c.Anchor)
	}
	slices.Sort(upserted)
	if want := []string{"a.md#b", "new.md#a"}; !slices.Equal(upserted, want) {
		t.Errorf("upsert = %v, want %v", upserted, want)
	}
	goneIDs := ChunkDocument(prev, prev.DocByPath("gone.md"), 0)
	if len(delta.Delete) != 1 || delta.Delete[0] != goneIDs[0].ID {
		t.Errorf("delete = %v, want gone.md chunk %s", delta.Delete, goneIDs[0].ID)
	}

	full := DiffChunks(nil, cur, 0)
	if len(full.Upsert) != 4 || len(full.Delete) != 0 {
		t.Errorf("initial delta upsert=%d delete=%d", len(full.Upsert), len(full.Delete))
	}
}
