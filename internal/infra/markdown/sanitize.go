package markdown

import (
	"bytes"
	"regexp"

	"github.com/microcosm-cc/bluemonday"
	"golang.org/x/net/html"
)

// newSanitizer 基于 UGC 策略清洗 HTML，额外放行代码高亮、提示块、任务列表与中文锚点所需的属性。
func newSanitizer() func([]byte) []byte {
	p := bluemonday.UGCPolicy()
	p.AllowAttrs("class").Matching(regexp.MustCompile(`^[\w\- ]+$`)).Globally()
	p.AllowAttrs("id").Matching(regexp.MustCompile(`^[\p{L}\p{N}_:.\-]+$`)).Globally()
	p.AllowElements("details", "summary", "kbd", "sup", "sub", "mark", "picture", "source", "video", "figure", "figcaption")
	p.AllowAttrs("open").OnElements("details")
	p.AllowAttrs("type").Matching(regexp.MustCompile(`^checkbox$`)).OnElements("input")
	p.AllowAttrs("checked", "disabled").OnElements("input")
	p.AllowAttrs("src", "srcset", "media", "type").OnElements("source")
	p.AllowAttrs("src", "poster", "controls", "width", "height").OnElements("video")
	p.AllowAttrs("loading").Matching(regexp.MustCompile(`^lazy$`)).OnElements("img")
	p.AllowAttrs("align").Matching(regexp.MustCompile(`^(left|right|center)$`)).OnElements("p", "div", "img", "h1", "h2", "h3", "th", "td")
	p.AllowElements("div", "span")
	p.AllowDataURIImages()
	p.RequireNoFollowOnFullyQualifiedLinks(true)
	p.AddTargetBlankToFullyQualifiedLinks(true)
	return p.SanitizeBytes
}

// urlAttrs 是会被链接改写处理的元素属性。
var urlAttrs = map[string]map[string]bool{
	"a":      {"href": true},
	"img":    {"src": true},
	"source": {"src": true},
	"video":  {"src": true, "poster": true},
}

// postProcess 遍历渲染结果，改写链接与资源地址，并为图片启用懒加载。
// 处理对象包括 markdown 生成的标签与文档中的原始 HTML。
func postProcess(in []byte, rewrite func(string) string) ([]byte, error) {
	var out bytes.Buffer
	out.Grow(len(in) + len(in)/8)
	z := html.NewTokenizer(bytes.NewReader(in))
	for {
		tt := z.Next()
		switch tt {
		case html.ErrorToken:
			return out.Bytes(), nil
		case html.StartTagToken, html.SelfClosingTagToken:
			raw := append([]byte(nil), z.Raw()...)
			tok := z.Token()
			attrs, ok := urlAttrs[tok.Data]
			if !ok {
				out.Write(raw)
				continue
			}
			for i, a := range tok.Attr {
				if attrs[a.Key] && rewrite != nil {
					tok.Attr[i].Val = rewrite(a.Val)
				}
			}
			if tok.Data == "img" {
				tok.Attr = append(tok.Attr, html.Attribute{Key: "loading", Val: "lazy"})
			}
			out.WriteString(tok.String())
		default:
			out.Write(z.Raw())
		}
	}
}
