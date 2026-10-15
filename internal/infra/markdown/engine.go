// Package markdown 基于 goldmark 实现文档解析与渲染：GFM、脚注、代码高亮、mermaid、
// GitHub 风格提示块、中文标题锚点，并对输出做 XSS 清洗。
package markdown

import (
	"bytes"
	"fmt"
	"path"
	"strings"
	"sync"

	chromahtml "github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/yuin/goldmark"
	highlighting "github.com/yuin/goldmark-highlighting/v2"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"

	"github.com/rushteam/getdocs/internal/domain/docs"
)

// Engine 实现 docs.Parser，并提供 HTML 渲染，可并发使用。
type Engine struct {
	md       goldmark.Markdown
	sanitize func([]byte) []byte
}

var _ docs.Parser = (*Engine)(nil)

// RenderOptions 控制渲染时的链接改写。Rewrite 接收文档中的原始地址，返回最终输出的地址，
// 由调用方根据自身的 URL 结构提供。
type RenderOptions struct {
	Rewrite func(dest string) string
}

// Rendered 是渲染后的 HTML 及目录。
type Rendered struct {
	HTML string
	TOC  []docs.Heading
}

// New 创建 markdown 引擎。
func New() *Engine {
	md := goldmark.New(
		goldmark.WithExtensions(
			// 等价于 extension.GFM，但表格对齐输出为 align 属性：style 属性会被净化器移除。
			extension.Linkify,
			extension.Strikethrough,
			extension.TaskList,
			extension.NewTable(extension.WithTableCellAlignMethod(extension.TableCellAlignAttribute)),
			extension.Footnote,
			extension.DefinitionList,
			extension.NewCJK(extension.WithEastAsianLineBreaks()),
			highlighting.NewHighlighting(
				highlighting.WithFormatOptions(chromahtml.WithClasses(true)),
			),
		),
		goldmark.WithParserOptions(
			parser.WithAutoHeadingID(),
			parser.WithASTTransformers(
				util.Prioritized(alertTransformer{}, 500),
				util.Prioritized(mermaidTransformer{}, 500),
			),
		),
		goldmark.WithRendererOptions(
			html.WithUnsafe(), // 允许 README 中常见的原始 HTML，输出统一经过清洗
			renderer.WithNodeRenderers(util.Prioritized(mermaidRenderer{}, 100)),
		),
	)
	return &Engine{md: md, sanitize: newSanitizer()}
}

func (e *Engine) parse(content []byte) (ast.Node, []byte) {
	_, body := splitFrontMatter(content)
	ctx := parser.NewContext(parser.WithIDs(newIDs()))
	return e.md.Parser().Parse(text.NewReader(body), parser.WithContext(ctx)), body
}

// Parse 提取标题、front matter、章节纯文本与链接。
func (e *Engine) Parse(p string, content []byte) (docs.ParsedDoc, error) {
	meta, _ := splitFrontMatter(content)
	doc, body := e.parse(content)
	out := docs.ParsedDoc{Meta: meta}
	out.Headings, out.Sections = extractSections(doc, body)
	out.Links = extractLinks(doc, body)

	if t, ok := meta["title"].(string); ok && strings.TrimSpace(t) != "" {
		out.Title = strings.TrimSpace(t)
	} else if h := firstH1(out.Headings); h != "" {
		out.Title = h
	} else {
		out.Title = docs.HumanizeName(path.Base(p))
		if docs.IsIndexDoc(p) && path.Dir(p) != "." {
			out.Title = docs.HumanizeName(path.Base(path.Dir(p)))
		}
	}
	out.Weight = intMeta(meta, "weight", "sidebar_position", "order", "nav_order")
	out.Draft, _ = meta["draft"].(bool)
	return out, nil
}

func firstH1(hs []docs.Heading) string {
	for _, h := range hs {
		if h.Level == 1 {
			return h.Text
		}
	}
	return ""
}

func intMeta(m map[string]any, keys ...string) int {
	for _, k := range keys {
		switch v := m[k].(type) {
		case int:
			return v
		case float64:
			return int(v)
		}
	}
	return 0
}

var bufPool = sync.Pool{New: func() any { return new(bytes.Buffer) }}

// Render 渲染文档为清洗后的 HTML，并按 opt.Rewrite 改写链接与图片地址。
func (e *Engine) Render(d *docs.Document, opt RenderOptions) (Rendered, error) {
	doc, body := e.parse(d.Content)
	headings, _ := extractSections(doc, body)

	buf := bufPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer bufPool.Put(buf)
	if err := e.md.Renderer().Render(buf, body, doc); err != nil {
		return Rendered{}, fmt.Errorf("render %s: %w", d.Path, err)
	}
	out, err := postProcess(buf.Bytes(), opt.Rewrite)
	if err != nil {
		return Rendered{}, fmt.Errorf("post-process %s: %w", d.Path, err)
	}
	return Rendered{HTML: string(e.sanitize(out)), TOC: headings}, nil
}

// HighlightCSS 返回代码高亮样式表，亮色与暗色主题分别作用于 [data-theme] 选择器之下。
func HighlightCSS(light, dark string) (string, error) {
	f := chromahtml.New(chromahtml.WithClasses(true))
	var out strings.Builder
	// 两套样式都要限定作用域：暗色主题未定义的 token 规则不能回落到亮色主题的颜色。
	for _, t := range []struct{ style, scope string }{{light, ":root:not([data-theme=dark])"}, {dark, "[data-theme=dark]"}} {
		var b bytes.Buffer
		if err := f.WriteCSS(&b, styles.Get(t.style)); err != nil {
			return "", err
		}
		// chroma 输出的每条规则都以 ".chroma" 开头，加上主题作用域即可。
		out.WriteString(strings.ReplaceAll(b.String(), ".chroma", t.scope+" .chroma"))
	}
	return out.String(), nil
}
