package markdown

import (
	"bytes"
	"strings"

	"github.com/yuin/goldmark/ast"
	east "github.com/yuin/goldmark/extension/ast"
	"golang.org/x/net/html"

	"getdocs/internal/domain/docs"
)

// extractSections 按标题把文档切分为章节，并返回标题列表。
// 第一个标题之前的内容归入无标题的引言章节。
func extractSections(doc ast.Node, src []byte) ([]docs.Heading, []docs.Section) {
	var headings []docs.Heading
	var sections []docs.Section
	cur := docs.Section{}
	var body strings.Builder
	var stack []docs.Heading

	flush := func() {
		cur.Text = strings.TrimSpace(body.String())
		if cur.Text != "" || cur.Heading != "" {
			sections = append(sections, cur)
		}
		body.Reset()
	}

	for n := doc.FirstChild(); n != nil; n = n.NextSibling() {
		h, ok := n.(*ast.Heading)
		if !ok {
			writeBlockText(&body, n, src)
			continue
		}
		flush()
		id, _ := h.AttributeString("id")
		idStr, _ := id.([]byte)
		heading := docs.Heading{Level: h.Level, Text: strings.TrimSpace(inlineText(h, src)), ID: string(idStr)}
		headings = append(headings, heading)

		for len(stack) > 0 && stack[len(stack)-1].Level >= h.Level {
			stack = stack[:len(stack)-1]
		}
		stack = append(stack, heading)
		titles := make([]string, len(stack))
		for i, s := range stack {
			titles[i] = s.Text
		}
		cur = docs.Section{Heading: heading.Text, Level: heading.Level, Anchor: heading.ID, Path: titles}
	}
	flush()
	return headings, sections
}

// writeBlockText 把块级节点转换为纯文本，块之间以空行分隔。
func writeBlockText(b *strings.Builder, n ast.Node, src []byte) {
	switch n := n.(type) {
	case *ast.FencedCodeBlock, *ast.CodeBlock:
		var code bytes.Buffer
		lines := n.Lines()
		for i := 0; i < lines.Len(); i++ {
			seg := lines.At(i)
			code.Write(seg.Value(src))
		}
		appendBlock(b, strings.TrimRight(code.String(), "\n"))
	case *ast.HTMLBlock:
		var raw bytes.Buffer
		lines := n.Lines()
		for i := 0; i < lines.Len(); i++ {
			seg := lines.At(i)
			raw.Write(seg.Value(src))
		}
		appendBlock(b, htmlText(raw.Bytes()))
	case *ast.ThematicBreak:
	case *ast.List, *ast.Blockquote, *ast.Document, *east.FootnoteList, *east.DefinitionList:
		for c := n.FirstChild(); c != nil; c = c.NextSibling() {
			writeBlockText(b, c, src)
		}
	case *ast.ListItem, *east.Footnote:
		var item strings.Builder
		for c := n.FirstChild(); c != nil; c = c.NextSibling() {
			writeBlockText(&item, c, src)
		}
		appendBlock(b, "- "+strings.TrimSpace(item.String()))
	case *east.Table:
		var rows []string
		for r := n.FirstChild(); r != nil; r = r.NextSibling() {
			var cells []string
			for c := r.FirstChild(); c != nil; c = c.NextSibling() {
				cells = append(cells, strings.TrimSpace(inlineText(c, src)))
			}
			rows = append(rows, strings.Join(cells, " | "))
		}
		appendBlock(b, strings.Join(rows, "\n"))
	default:
		if n.Type() == ast.TypeBlock && n.HasChildren() && n.FirstChild().Type() == ast.TypeBlock {
			for c := n.FirstChild(); c != nil; c = c.NextSibling() {
				writeBlockText(b, c, src)
			}
			return
		}
		appendBlock(b, inlineText(n, src))
	}
}

func appendBlock(b *strings.Builder, s string) {
	s = strings.TrimSpace(s)
	if s == "" {
		return
	}
	if b.Len() > 0 {
		b.WriteString("\n\n")
	}
	b.WriteString(s)
}

// inlineText 提取行内节点的纯文本。
func inlineText(n ast.Node, src []byte) string {
	var b strings.Builder
	_ = ast.Walk(n, func(c ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch c := c.(type) {
		case *ast.Text:
			b.Write(c.Value(src))
			if c.SoftLineBreak() || c.HardLineBreak() {
				b.WriteByte(' ')
			}
		case *ast.String:
			b.Write(c.Value)
		case *ast.CodeSpan:
			for t := c.FirstChild(); t != nil; t = t.NextSibling() {
				if s, ok := t.(*ast.Text); ok {
					b.Write(s.Value(src))
				}
			}
			return ast.WalkSkipChildren, nil
		case *ast.AutoLink:
			b.Write(c.URL(src))
		case *ast.RawHTML:
			return ast.WalkSkipChildren, nil
		case *ast.Image:
			b.Write(c.Title)
			return ast.WalkSkipChildren, nil
		}
		return ast.WalkContinue, nil
	})
	return b.String()
}

// htmlText 提取原始 HTML 片段中的可见文本。
func htmlText(raw []byte) string {
	z := html.NewTokenizer(bytes.NewReader(raw))
	var b strings.Builder
	skip := 0
	for {
		switch z.Next() {
		case html.ErrorToken:
			return strings.Join(strings.Fields(b.String()), " ")
		case html.TextToken:
			if skip == 0 {
				b.Write(z.Text())
				b.WriteByte(' ')
			}
		case html.StartTagToken:
			if name, _ := z.TagName(); string(name) == "script" || string(name) == "style" {
				skip++
			}
		case html.EndTagToken:
			if name, _ := z.TagName(); (string(name) == "script" || string(name) == "style") && skip > 0 {
				skip--
			}
		}
	}
}

// extractLinks 收集文档中的链接与图片地址，包括原始 HTML 中的 href / src。
func extractLinks(doc ast.Node, src []byte) []string {
	var out []string
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n := n.(type) {
		case *ast.Link:
			out = append(out, string(n.Destination))
		case *ast.Image:
			out = append(out, string(n.Destination))
		case *ast.HTMLBlock:
			var raw bytes.Buffer
			for i := 0; i < n.Lines().Len(); i++ {
				seg := n.Lines().At(i)
				raw.Write(seg.Value(src))
			}
			out = append(out, htmlLinks(raw.Bytes())...)
		case *ast.RawHTML:
			var raw bytes.Buffer
			for i := 0; i < n.Segments.Len(); i++ {
				seg := n.Segments.At(i)
				raw.Write(seg.Value(src))
			}
			out = append(out, htmlLinks(raw.Bytes())...)
		}
		return ast.WalkContinue, nil
	})
	return out
}

func htmlLinks(raw []byte) []string {
	var out []string
	z := html.NewTokenizer(bytes.NewReader(raw))
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			return out
		}
		if tt != html.StartTagToken && tt != html.SelfClosingTagToken {
			continue
		}
		for _, a := range z.Token().Attr {
			if a.Key == "src" || a.Key == "href" || a.Key == "poster" {
				out = append(out, a.Val)
			}
		}
	}
}
