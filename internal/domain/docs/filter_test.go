package docs

import "testing"

func TestFilterMatchDoc(t *testing.T) {
	f := Filter{Exclude: []string{"drafts", "**/internal-*.md"}}
	cases := map[string]bool{
		"README.md":                  true,
		"guide/install.markdown":     true,
		"guide/page.mdx":             true,
		"guide/logo.png":             false,
		"drafts/wip.md":              false,
		"drafts/deep/wip.md":         false,
		"guide/internal-notes.md":    false,
		"node_modules/pkg/README.md": false,
		"a/node_modules/x.md":        false,
		".github/PULL_REQUEST.md":    false,
		"dist/out.md":                false,
	}
	for p, want := range cases {
		if got := f.MatchDoc(p); got != want {
			t.Errorf("MatchDoc(%q) = %v, want %v", p, got, want)
		}
	}

	if f.MatchDoc("api/user.proto") {
		t.Errorf(".proto matched without Proto")
	}
	p := Filter{Proto: true, Exclude: []string{"third_party"}}
	if !p.MatchDoc("api/user.proto") || !p.MatchDoc("README.md") || p.MatchDoc("third_party/google/api/http.proto") || p.MatchDoc("main.go") {
		t.Errorf("Proto filter not applied")
	}

	inc := Filter{Include: []string{"guide/**"}}
	if !inc.MatchDoc("guide/a/b.md") || inc.MatchDoc("README.md") {
		t.Errorf("include pattern not applied")
	}
}

func TestFilterValidate(t *testing.T) {
	if err := (Filter{Include: []string{"docs/**/*.md"}}).Validate(); err != nil {
		t.Fatalf("valid pattern rejected: %v", err)
	}
	if err := (Filter{Exclude: []string{"[unclosed"}}).Validate(); err == nil {
		t.Fatalf("invalid pattern accepted")
	}
}

func TestFilterMaxSize(t *testing.T) {
	if (Filter{}).MaxSize() != DefaultMaxFileSize {
		t.Errorf("default max size not applied")
	}
	if (Filter{MaxFileSize: 10}).MaxSize() != 10 {
		t.Errorf("custom max size ignored")
	}
}

func TestDetectRoot(t *testing.T) {
	cases := []struct {
		name  string
		files []string
		want  string
	}{
		{"docs dir", []string{"main.go", "README.md", "docs/index.md"}, "docs"},
		{"case preserved", []string{"Docs/guide.md"}, "Docs"},
		{"candidate order", []string{"wiki/a.md", "doc/b.md"}, "doc"},
		{"nested candidate", []string{"website/docs/intro.md"}, "website/docs"},
		{"no markdown in candidate", []string{"docs/logo.png", "README.md"}, "."},
		{"excluded only", []string{"docs/node_modules/x.md", "README.md"}, "."},
		{"fallback", []string{"README.md", "cmd/main.go"}, "."},
	}
	for _, c := range cases {
		if got := DetectRoot(c.files, Filter{}); got != c.want {
			t.Errorf("%s: DetectRoot = %q, want %q", c.name, got, c.want)
		}
	}
}
