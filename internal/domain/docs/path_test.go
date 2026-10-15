package docs

import (
	"testing"
	"time"
)

func TestSlugOf(t *testing.T) {
	cases := map[string]string{
		"README.md":         "",
		"index.md":          "",
		"guide/README.md":   "guide",
		"guide/_index.md":   "guide",
		"guide/install.md":  "guide/install",
		"guide/readme-x.md": "guide/readme-x",
		"api/v1.markdown":   "api/v1",
		"Guide/Readme.MD":   "Guide",
		"notes.mdx":         "notes",
	}
	for p, want := range cases {
		if got := SlugOf(p); got != want {
			t.Errorf("SlugOf(%q) = %q, want %q", p, got, want)
		}
	}
}

func TestResolveRel(t *testing.T) {
	cases := []struct{ base, ref, want string }{
		{"docs/guide/a.md", "b.md", "docs/guide/b.md"},
		{"docs/guide/a.md", "../img/x.png", "docs/img/x.png"},
		{"docs/guide/a.md", "/src/main.go", "src/main.go"},
		{"a.md", "../../etc/passwd", "../../etc/passwd"},
		{"a.md", "./b/../c.md", "c.md"},
	}
	for _, c := range cases {
		if got := ResolveRel(c.base, c.ref); got != c.want {
			t.Errorf("ResolveRel(%q, %q) = %q, want %q", c.base, c.ref, got, c.want)
		}
	}
	if !Escapes("..") || !Escapes("../x") || Escapes("..x/y") || Escapes("a/../b") {
		t.Errorf("Escapes misclassified a path")
	}
}

func TestHumanizeName(t *testing.T) {
	cases := map[string]string{
		"getting-started.md": "Getting started",
		"01-intro.md":        "Intro",
		"02_quick_start":     "Quick start",
		"2024.md":            "2024",
		"v2-api":             "V2 api",
		"安装指南.md":            "安装指南",
		"":                   "",
	}
	for in, want := range cases {
		if got := HumanizeName(in); got != want {
			t.Errorf("HumanizeName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseTreeKey(t *testing.T) {
	k, err := ParseTreeKey("/team/api/")
	if err != nil || k != (TreeKey{"team", "api"}) {
		t.Fatalf("ParseTreeKey = %v, %v", k, err)
	}
	for _, bad := range []string{"", "team", "team/", "/api", "a/b/c"} {
		if _, err := ParseTreeKey(bad); err == nil {
			t.Errorf("ParseTreeKey(%q) accepted", bad)
		}
	}
}

func TestSourceFileURL(t *testing.T) {
	src := Source{Branch: "main", BrowseURL: "https://example.com/blob/{branch}/{path}?c={commit}"}
	rev := Revision{Commit: "abc123"}
	if got, want := src.FileURL(rev, "/docs/a.md"), "https://example.com/blob/main/docs/a.md?c=abc123"; got != want {
		t.Errorf("FileURL = %q, want %q", got, want)
	}
	rev.Branch = "dev"
	if got := src.FileURL(rev, "a.md"); got != "https://example.com/blob/dev/a.md?c=abc123" {
		t.Errorf("revision branch not preferred: %q", got)
	}
	if (Source{}).FileURL(rev, "a.md") != "" {
		t.Errorf("FileURL without template should be empty")
	}
}

func TestNewVersionSortable(t *testing.T) {
	t0 := time.Date(2026, 1, 2, 3, 4, 5, 6, time.UTC)
	a := NewVersion(t0, Revision{Commit: "ffffffffffff"})
	b := NewVersion(t0.Add(time.Millisecond), Revision{Commit: "000000000000"})
	if !(a < b) {
		t.Errorf("versions not ordered: %q >= %q", a, b)
	}
	if want := "20260102T030405000000006-ffffffff"; a != want {
		t.Errorf("NewVersion = %q, want %q", a, want)
	}
}

func TestFingerprintIgnoresCredentials(t *testing.T) {
	a := &TreeSpec{Source: Source{Kind: SourceGit, URL: "u", Auth: Auth{Password: "x"}}, Root: "docs"}
	b := &TreeSpec{Source: Source{Kind: SourceGit, URL: "u", Auth: Auth{Password: "y"}}, Root: "docs"}
	if a.Fingerprint() != b.Fingerprint() {
		t.Errorf("credentials should not affect fingerprint")
	}
	b.Filter.Exclude = []string{"drafts"}
	if a.Fingerprint() == b.Fingerprint() {
		t.Errorf("filter change should affect fingerprint")
	}
}
