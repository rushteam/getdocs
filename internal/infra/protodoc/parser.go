package protodoc

import (
	"github.com/rushteam/getdocs/internal/domain/docs"
	"github.com/rushteam/getdocs/internal/infra/markdown"
)

// Parser 按文件类型分派：.proto 文件先转换为接口文档 markdown 再解析，其余交给 markdown 引擎。
type Parser struct {
	Markdown *markdown.Engine
}

// NewParser 创建支持 .proto 文件的文档解析器。
func NewParser(md *markdown.Engine) *Parser { return &Parser{Markdown: md} }

// Parse 实现 docs.Parser。
func (p *Parser) Parse(path string, content []byte) (docs.ParsedDoc, error) {
	if !docs.IsProto(path) {
		return p.Markdown.Parse(path, content)
	}
	md, err := Convert(path, content)
	if err != nil {
		return docs.ParsedDoc{}, err
	}
	out, err := p.Markdown.Parse(path, md)
	if err != nil {
		return docs.ParsedDoc{}, err
	}
	out.Content = md
	return out, nil
}

var _ docs.Parser = (*Parser)(nil)
