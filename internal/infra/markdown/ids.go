package markdown

import (
	"strconv"
	"strings"
	"unicode"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
)

// ids 生成与 GitHub 一致的标题锚点：保留各语言的字母与数字（含中文），
// 空格转为连字符，其余标点删除；重复时追加 -1、-2。
type ids struct{ seen map[string]bool }

func newIDs() parser.IDs { return &ids{seen: map[string]bool{}} }

func (s *ids) Generate(value []byte, kind ast.NodeKind) []byte {
	base := Slugify(string(value))
	if base == "" {
		base = "heading"
	}
	id := base
	for i := 1; s.seen[id]; i++ {
		id = base + "-" + strconv.Itoa(i)
	}
	s.seen[id] = true
	return []byte(id)
}

func (s *ids) Put(value []byte) { s.seen[string(value)] = true }

// Slugify 把标题文本转换为锚点标识。
func Slugify(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case unicode.IsLetter(r) || unicode.IsNumber(r) || r == '_' || r == '-':
			b.WriteRune(r)
		case r == ' ':
			b.WriteRune('-')
		}
	}
	return b.String()
}
