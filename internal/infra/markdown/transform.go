package markdown

import (
	"bytes"
	"strings"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// alertTransformer 把 GitHub 风格的提示块（> [!NOTE]）转换为带样式类的 blockquote。
type alertTransformer struct{}

var alertTitles = map[string]string{
	"NOTE": "Note", "TIP": "Tip", "IMPORTANT": "Important", "WARNING": "Warning", "CAUTION": "Caution",
}

func (alertTransformer) Transform(doc *ast.Document, reader text.Reader, _ parser.Context) {
	src := reader.Source()
	var quotes []*ast.Blockquote
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if q, ok := n.(*ast.Blockquote); ok && entering {
			quotes = append(quotes, q)
		}
		return ast.WalkContinue, nil
	})
	for _, q := range quotes {
		para, ok := q.FirstChild().(*ast.Paragraph)
		if !ok || para.Lines().Len() == 0 {
			continue
		}
		first := para.Lines().At(0)
		marker := strings.TrimSpace(string(first.Value(src)))
		if !strings.HasPrefix(marker, "[!") || !strings.HasSuffix(marker, "]") {
			continue
		}
		kind := strings.ToUpper(marker[2 : len(marker)-1])
		title, ok := alertTitles[kind]
		if !ok {
			continue
		}
		// 删除第一行（标记行）对应的行内节点。
		for c := para.FirstChild(); c != nil; {
			next := c.NextSibling()
			t, isText := c.(*ast.Text)
			if isText && t.Segment.Start >= first.Stop {
				break
			}
			para.RemoveChild(para, c)
			if isText && (t.SoftLineBreak() || t.HardLineBreak()) {
				break
			}
			c = next
		}
		if !para.HasChildren() {
			q.RemoveChild(q, para)
		}
		lower := strings.ToLower(kind)
		q.SetAttributeString("class", []byte("markdown-alert markdown-alert-"+lower))
		head := ast.NewParagraph()
		head.SetAttributeString("class", []byte("markdown-alert-title"))
		head.AppendChild(head, ast.NewString([]byte(title)))
		q.InsertBefore(q, q.FirstChild(), head)
	}
}

// KindMermaid 是 mermaid 图表节点类型。
var KindMermaid = ast.NewNodeKind("Mermaid")

// mermaidBlock 承载 ```mermaid 代码块，渲染为 <pre class="mermaid"> 由前端绘制。
type mermaidBlock struct {
	ast.BaseBlock
}

func (*mermaidBlock) Kind() ast.NodeKind { return KindMermaid }
func (n *mermaidBlock) Dump(src []byte, level int) {
	ast.DumpHelper(n, src, level, nil, nil)
}
func (*mermaidBlock) IsRaw() bool { return true }

type mermaidTransformer struct{}

func (mermaidTransformer) Transform(doc *ast.Document, reader text.Reader, _ parser.Context) {
	src := reader.Source()
	var blocks []*ast.FencedCodeBlock
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if b, ok := n.(*ast.FencedCodeBlock); ok && entering && string(b.Language(src)) == "mermaid" {
			blocks = append(blocks, b)
		}
		return ast.WalkContinue, nil
	})
	for _, b := range blocks {
		m := &mermaidBlock{}
		m.SetLines(b.Lines())
		b.Parent().ReplaceChild(b.Parent(), b, m)
	}
}

type mermaidRenderer struct{}

func (mermaidRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(KindMermaid, func(w util.BufWriter, src []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		var code bytes.Buffer
		for i := 0; i < n.Lines().Len(); i++ {
			seg := n.Lines().At(i)
			code.Write(seg.Value(src))
		}
		_, _ = w.WriteString(`<pre class="mermaid">`)
		_, _ = w.Write(util.EscapeHTML(code.Bytes()))
		_, _ = w.WriteString("</pre>\n")
		return ast.WalkSkipChildren, nil
	})
}
