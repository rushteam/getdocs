package markdown

import (
	"strings"
	"testing"

	"github.com/rushteam/getdocs/internal/domain/docs"
)

const sample = `---
title: 快速开始
weight: 2
---

引言段落。

## 安装 Install

运行 ` + "`go install`" + `。

> [!WARNING]
> 小心操作。

` + "```go\nfunc main() {}\n```" + `

` + "```mermaid\ngraph TD; A-->B\n```" + `

[下一篇](./next.md#用法) ![图](../img/a.png) [外链](https://example.com)

<img src="logo.png"><script>alert(1)</script>

## 安装 Install

| a | b |
|---|---|
| 1 | 2 |
`

func TestParse(t *testing.T) {
	e := New()
	p, err := e.Parse("guide/start.md", []byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	if p.Title != "快速开始" || p.Weight != 2 {
		t.Fatalf("title/weight = %q/%d", p.Title, p.Weight)
	}
	if len(p.Headings) != 2 || p.Headings[0].ID != "安装-install" || p.Headings[1].ID != "安装-install-1" {
		t.Fatalf("headings = %+v", p.Headings)
	}
	if len(p.Sections) != 3 || p.Sections[0].Text != "引言段落。" {
		t.Fatalf("sections = %+v", p.Sections)
	}
	if !strings.Contains(p.Sections[1].Text, "func main()") || !strings.Contains(p.Sections[1].Text, "小心操作") {
		t.Fatalf("section text = %q", p.Sections[1].Text)
	}
	want := []string{"./next.md#用法", "../img/a.png", "https://example.com", "logo.png"}
	if strings.Join(p.Links, ",") != strings.Join(want, ",") {
		t.Fatalf("links = %v", p.Links)
	}
}

func TestRender(t *testing.T) {
	e := New()
	d := &docs.Document{Path: "guide/start.md", Content: []byte(sample)}
	r, err := e.Render(d, RenderOptions{Rewrite: func(s string) string { return "/x/" + s }})
	if err != nil {
		t.Fatal(err)
	}
	h := r.HTML
	for _, want := range []string{
		`id="安装-install"`,
		`markdown-alert-warning`,
		`<pre class="mermaid">graph TD; A--&gt;B`,
		`class="chroma"`,
		`href="/x/./next.md#%E7%94%A8%E6%B3%95"`,
		`src="/x/../img/a.png"`,
		`src="/x/logo.png"`,
		`<table>`,
	} {
		if !strings.Contains(h, want) {
			t.Errorf("missing %q in\n%s", want, h)
		}
	}
	if strings.Contains(h, "<script") || strings.Contains(h, "[!WARNING]") || strings.Contains(h, "title: 快速开始") {
		t.Errorf("unexpected content in\n%s", h)
	}
}
