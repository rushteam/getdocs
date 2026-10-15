// Package protodoc 把 protobuf 接口定义（.proto 源文件）转换为 markdown 接口文档。
//
// 转换只做语法解析、不做链接，因此不需要 import 的文件（如 googleapis）存在于仓库中：
// 同文件内定义的类型渲染为页内锚点链接，其余类型按源码中的写法原样展示。
package protodoc

import (
	"bytes"
	"fmt"
	"html"
	"path"
	"strconv"
	"strings"

	"github.com/bufbuild/protocompile/ast"
	"github.com/bufbuild/protocompile/parser"
	"github.com/bufbuild/protocompile/reporter"
	"github.com/bufbuild/protocompile/sourceinfo"
	"google.golang.org/protobuf/types/descriptorpb"
	"gopkg.in/yaml.v3"

	"getdocs/internal/infra/markdown"
)

// descriptor.proto 中的字段编号，用于定位源码注释。
const (
	tagFileMessage    = 4
	tagFileEnum       = 5
	tagFileService    = 6
	tagMessageField   = 2
	tagMessageNested  = 3
	tagMessageEnum    = 4
	tagEnumValue      = 2
	tagServiceMethod  = 2
	tagFilePackage    = 2
	tagFileSyntax     = 12
	tagFileEditionTag = 14
)

// Convert 把 .proto 源文件转换为接口文档 markdown：文件概览、服务与方法（含 google.api.http 映射）、
// 消息字段表、枚举值表，以及折叠展示的源文件。语法错误时返回带行列号的错误。
func Convert(name string, src []byte) ([]byte, error) {
	h := reporter.NewHandler(nil)
	file, err := parser.Parse(name, bytes.NewReader(src), h)
	if err != nil {
		return nil, err
	}
	res, err := parser.ResultFromAST(file, true, h)
	if err != nil {
		return nil, err
	}
	g := &gen{name: name, src: src, res: res, fd: res.FileDescriptorProto(), anchors: map[string]string{}, seen: map[string]bool{}}
	g.pkg = g.fd.GetPackage()
	g.comments = map[string]*descriptorpb.SourceCodeInfo_Location{}
	for _, loc := range sourceinfo.GenerateSourceInfo(file, nil).GetLocation() {
		g.comments[pathKey(loc.GetPath())] = loc
	}
	g.collect()
	g.assignIDs()
	return g.render()
}

type msgEntry struct {
	fq, display, id string
	d               *descriptorpb.DescriptorProto
	path            []int32
}

type enumEntry struct {
	fq, display, id string
	d               *descriptorpb.EnumDescriptorProto
	path            []int32
}

type gen struct {
	name     string
	src      []byte
	res      parser.Result
	fd       *descriptorpb.FileDescriptorProto
	pkg      string
	comments map[string]*descriptorpb.SourceCodeInfo_Location

	msgs    []msgEntry
	enums   []enumEntry
	anchors map[string]string // 类型全限定名 → 页内锚点

	seen       map[string]bool // 已生成的标题锚点，规则与 markdown 引擎一致
	titleID    string
	sectionIDs map[string]string
	serviceIDs []string
	methodIDs  [][]string

	b strings.Builder
}

func (g *gen) collect() {
	for i, e := range g.fd.GetEnumType() {
		g.enums = append(g.enums, enumEntry{fq: qualify(g.pkg, e.GetName()), display: e.GetName(), d: e, path: []int32{tagFileEnum, int32(i)}})
	}
	var walk func(m *descriptorpb.DescriptorProto, fq, display string, p []int32)
	walk = func(m *descriptorpb.DescriptorProto, fq, display string, p []int32) {
		if m.GetOptions().GetMapEntry() {
			return // map 字段的合成消息不单独列出
		}
		g.msgs = append(g.msgs, msgEntry{fq: fq, display: display, d: m, path: p})
		for j, e := range m.GetEnumType() {
			g.enums = append(g.enums, enumEntry{fq: fq + "." + e.GetName(), display: display + "." + e.GetName(), d: e, path: sub(p, tagMessageEnum, j)})
		}
		for j, n := range m.GetNestedType() {
			walk(n, fq+"."+n.GetName(), display+"."+n.GetName(), sub(p, tagMessageNested, j))
		}
	}
	for i, m := range g.fd.GetMessageType() {
		walk(m, qualify(g.pkg, m.GetName()), m.GetName(), []int32{tagFileMessage, int32(i)})
	}
}

// assignIDs 按标题在页面中出现的顺序生成锚点，使引用可以先于定义出现。
func (g *gen) assignIDs() {
	g.titleID = g.id(path.Base(g.name))
	g.sectionIDs = map[string]string{}
	if len(g.fd.GetService()) > 0 {
		g.sectionIDs["服务"] = g.id("服务")
		for _, s := range g.fd.GetService() {
			g.serviceIDs = append(g.serviceIDs, g.id(s.GetName()))
			var ms []string
			for _, m := range s.GetMethod() {
				ms = append(ms, g.id(m.GetName()))
			}
			g.methodIDs = append(g.methodIDs, ms)
		}
	}
	if len(g.msgs) > 0 {
		g.sectionIDs["消息"] = g.id("消息")
		for i := range g.msgs {
			g.msgs[i].id = g.id(g.msgs[i].display)
			g.anchors[g.msgs[i].fq] = g.msgs[i].id
		}
	}
	if len(g.enums) > 0 {
		g.sectionIDs["枚举"] = g.id("枚举")
		for i := range g.enums {
			g.enums[i].id = g.id(g.enums[i].display)
			g.anchors[g.enums[i].fq] = g.enums[i].id
		}
	}
}

// id 复刻 markdown 引擎的标题锚点规则（含重名时的 -1、-2 后缀）。
func (g *gen) id(text string) string {
	base := markdown.Slugify(text)
	if base == "" {
		base = "heading"
	}
	id := base
	for i := 1; g.seen[id]; i++ {
		id = base + "-" + strconv.Itoa(i)
	}
	g.seen[id] = true
	return id
}

type frontMatter struct {
	Title       string   `yaml:"title"`
	Description string   `yaml:"description,omitempty"`
	Format      string   `yaml:"format"`
	Package     string   `yaml:"package,omitempty"`
	Services    []string `yaml:"services,omitempty"`
}

func (g *gen) render() ([]byte, error) {
	base := path.Base(g.name)
	fileDoc := g.comment(tagFilePackage)
	if fileDoc == "" {
		fileDoc = g.comment(tagFileSyntax)
	}
	if fileDoc == "" {
		fileDoc = g.comment(tagFileEditionTag)
	}
	fm := frontMatter{Title: base, Description: summary(fileDoc), Format: "proto", Package: g.pkg}
	for _, s := range g.fd.GetService() {
		fm.Services = append(fm.Services, s.GetName())
	}
	head, err := yaml.Marshal(fm)
	if err != nil {
		return nil, fmt.Errorf("front matter: %w", err)
	}
	g.b.WriteString("---\n")
	g.b.Write(head)
	g.b.WriteString("---\n\n")

	g.line("# " + base)
	g.line("")
	g.para(block(fileDoc))
	g.overview()
	g.services()
	g.messages()
	g.enumsSection()
	g.source(base)
	return []byte(g.b.String()), nil
}

func (g *gen) overview() {
	g.line("| 项目 | 内容 |")
	g.line("| --- | --- |")
	if g.pkg != "" {
		g.row("包", code(g.pkg))
	}
	g.row("语法", code(g.syntax()))
	if deps := g.fd.GetDependency(); len(deps) > 0 {
		cells := make([]string, len(deps))
		for i, d := range deps {
			cells[i] = code(d)
		}
		g.row("导入", strings.Join(cells, "<br>"))
	}
	for _, o := range g.fd.GetOptions().GetUninterpretedOption() {
		g.row(code(optionName(o)), code(optionValue(o)))
	}
	g.line("")
}

func (g *gen) syntax() string {
	file := g.res.AST()
	switch {
	case file != nil && file.Syntax != nil:
		return file.Syntax.Syntax.AsString()
	case file != nil && file.Edition != nil:
		return "edition " + file.Edition.Edition.AsString()
	}
	return "proto2"
}

func (g *gen) services() {
	if len(g.fd.GetService()) == 0 {
		return
	}
	g.line("## 服务")
	g.line("")
	for i, s := range g.fd.GetService() {
		sp := []int32{tagFileService, int32(i)}
		g.line("### " + s.GetName())
		g.line("")
		if isDeprecated(s.GetOptions().GetUninterpretedOption()) {
			g.para("**已废弃**")
		}
		g.para(block(g.comment(sp...)))
		if len(s.GetMethod()) == 0 {
			g.para("没有定义方法。")
			continue
		}
		g.line("| 方法 | 请求 | 响应 | 说明 |")
		g.line("| --- | --- | --- | --- |")
		for j, m := range s.GetMethod() {
			desc := summary(g.comment(sub(sp, tagServiceMethod, j)...))
			if rules := g.httpRules(m); len(rules) > 0 {
				desc = strings.TrimSpace(code(rules[0].method+" "+rules[0].path) + " " + desc)
			}
			g.row("["+m.GetName()+"](#"+g.methodIDs[i][j]+")", g.rpcType(m.GetInputType(), m.GetClientStreaming()),
				g.rpcType(m.GetOutputType(), m.GetServerStreaming()), inline(desc))
		}
		g.line("")
		for j, m := range s.GetMethod() {
			g.method(m, sub(sp, tagServiceMethod, j))
		}
	}
}

func (g *gen) method(m *descriptorpb.MethodDescriptorProto, p []int32) {
	g.line("#### " + m.GetName())
	g.line("")
	g.para(block(g.comment(p...)))
	g.line("- 请求：" + g.rpcType(m.GetInputType(), m.GetClientStreaming()))
	g.line("- 响应：" + g.rpcType(m.GetOutputType(), m.GetServerStreaming()))
	for _, r := range g.httpRules(m) {
		s := "- HTTP：" + code(r.method+" "+r.path)
		if r.body != "" {
			s += "，请求体 " + code(r.body)
		}
		g.line(s)
	}
	switch {
	case m.GetClientStreaming() && m.GetServerStreaming():
		g.line("- 流式：双向流")
	case m.GetClientStreaming():
		g.line("- 流式：客户端流")
	case m.GetServerStreaming():
		g.line("- 流式：服务端流")
	}
	if isDeprecated(m.GetOptions().GetUninterpretedOption()) {
		g.line("- **已废弃**")
	}
	g.line("")
}

func (g *gen) rpcType(name string, stream bool) string {
	t := g.named(g.pkg, name)
	if stream {
		return "stream " + t
	}
	return t
}

func (g *gen) messages() {
	if len(g.msgs) == 0 {
		return
	}
	g.line("## 消息")
	g.line("")
	for _, e := range g.msgs {
		g.line("### " + e.display)
		g.line("")
		if isDeprecated(e.d.GetOptions().GetUninterpretedOption()) {
			g.para("**已废弃**")
		}
		g.para(block(g.comment(e.path...)))
		if len(e.d.GetField()) == 0 {
			g.para("没有字段。")
			continue
		}
		g.line("| 字段 | 类型 | 编号 | 说明 |")
		g.line("| --- | --- | --- | --- |")
		for k, f := range e.d.GetField() {
			var notes []string
			if f.OneofIndex != nil && !f.GetProto3Optional() {
				notes = append(notes, "oneof "+code(e.d.GetOneofDecl()[f.GetOneofIndex()].GetName()))
			}
			opts := f.GetOptions().GetUninterpretedOption()
			if isDeprecated(opts) {
				notes = append(notes, "**已废弃**")
			}
			for _, o := range opts {
				if optionName(o) == "default" {
					notes = append(notes, "默认 "+code(optionValue(o)))
				}
			}
			if c := inline(g.comment(sub(e.path, tagMessageField, k)...)); c != "" {
				notes = append(notes, c)
			}
			g.row(code(f.GetName()), g.fieldType(e, f), strconv.Itoa(int(f.GetNumber())), strings.Join(notes, "；"))
		}
		g.line("")
	}
}

func (g *gen) fieldType(e msgEntry, f *descriptorpb.FieldDescriptorProto) string {
	if entry := mapEntry(e.d, f); entry != nil && len(entry.GetField()) == 2 {
		k, v := entry.GetField()[0], entry.GetField()[1]
		t := "map<" + typeName(k) + ", " + typeName(v) + ">"
		if !isScalar(v) {
			if id, ok := g.resolve(e.fq, v.GetTypeName()); ok {
				return "[" + code(t) + "](#" + id + ")"
			}
		}
		return code(t)
	}
	t := code(typeName(f))
	if !isScalar(f) {
		t = g.named(e.fq, f.GetTypeName())
	}
	switch {
	case f.GetLabel() == descriptorpb.FieldDescriptorProto_LABEL_REPEATED:
		return "repeated " + t
	case f.GetLabel() == descriptorpb.FieldDescriptorProto_LABEL_REQUIRED:
		return "required " + t
	case f.GetProto3Optional(), f.GetLabel() == descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL && g.syntax() == "proto2":
		return "optional " + t
	}
	return t
}

func (g *gen) enumsSection() {
	if len(g.enums) == 0 {
		return
	}
	g.line("## 枚举")
	g.line("")
	for _, e := range g.enums {
		g.line("### " + e.display)
		g.line("")
		if isDeprecated(e.d.GetOptions().GetUninterpretedOption()) {
			g.para("**已废弃**")
		}
		g.para(block(g.comment(e.path...)))
		g.line("| 名称 | 值 | 说明 |")
		g.line("| --- | --- | --- |")
		for k, v := range e.d.GetValue() {
			desc := inline(g.comment(sub(e.path, tagEnumValue, k)...))
			if isDeprecated(v.GetOptions().GetUninterpretedOption()) {
				desc = strings.TrimSpace("**已废弃** " + desc)
			}
			g.row(code(v.GetName()), strconv.Itoa(int(v.GetNumber())), desc)
		}
		g.line("")
	}
}

func (g *gen) source(base string) {
	g.line("## 源文件")
	g.line("")
	g.line("<details>")
	g.line("<summary>" + html.EscapeString(base) + "</summary>")
	g.line("")
	src := strings.TrimRight(strings.ReplaceAll(string(g.src), "\r\n", "\n"), "\n")
	f := fence(src)
	g.line(f + "protobuf")
	g.line(src)
	g.line(f)
	g.line("")
	g.line("</details>")
}

// named 渲染消息或枚举类型引用：同文件定义的类型链接到页内锚点。
func (g *gen) named(scope, name string) string {
	if id, ok := g.resolve(scope, name); ok {
		return "[" + code(name) + "](#" + id + ")"
	}
	return code(strings.TrimPrefix(name, "."))
}

// resolve 按 protobuf 的作用域规则（由内向外）在本文件定义的类型中查找引用。
func (g *gen) resolve(scope, name string) (string, bool) {
	if strings.HasPrefix(name, ".") {
		id, ok := g.anchors[name[1:]]
		return id, ok
	}
	for s := scope; ; {
		if id, ok := g.anchors[qualify(s, name)]; ok {
			return id, true
		}
		if s == "" {
			return "", false
		}
		if i := strings.LastIndexByte(s, '.'); i >= 0 {
			s = s[:i]
		} else {
			s = ""
		}
	}
}

// comment 返回源码位置上的前置与行尾注释。
func (g *gen) comment(p ...int32) string {
	loc := g.comments[pathKey(p)]
	if loc == nil {
		return ""
	}
	var parts []string
	for _, c := range []string{loc.GetLeadingComments(), loc.GetTrailingComments()} {
		if c = cleanComment(c); c != "" {
			parts = append(parts, c)
		}
	}
	return strings.Join(parts, "\n\n")
}

type httpRule struct{ method, path, body string }

// httpRules 解析方法上的 google.api.http 注解（HTTP/JSON 转码映射）。
func (g *gen) httpRules(m *descriptorpb.MethodDescriptorProto) []httpRule {
	var out []httpRule
	var short httpRule // option (google.api.http).get = "..." 形式
	for _, o := range m.GetOptions().GetUninterpretedOption() {
		parts := o.GetName()
		if len(parts) == 0 || !parts[0].GetIsExtension() || strings.TrimPrefix(parts[0].GetNamePart(), ".") != "google.api.http" {
			continue
		}
		node, _ := g.res.OptionNode(o).(*ast.OptionNode)
		if node == nil {
			continue
		}
		if len(parts) == 1 {
			if lit, ok := node.Val.(*ast.MessageLiteralNode); ok {
				out = append(out, parseHTTPRule(lit)...)
			}
			continue
		}
		switch k := parts[1].GetNamePart(); k {
		case "get", "put", "post", "delete", "patch":
			short.method, short.path = strings.ToUpper(k), stringValue(node.Val)
		case "body":
			short.body = stringValue(node.Val)
		}
	}
	if short.path != "" {
		out = append(out, short)
	}
	return out
}

func parseHTTPRule(lit *ast.MessageLiteralNode) []httpRule {
	var r httpRule
	var extra []httpRule
	for _, el := range lit.Elements {
		switch k := el.Name.Value(); k {
		case "get", "put", "post", "delete", "patch":
			r.method, r.path = strings.ToUpper(k), stringValue(el.Val)
		case "custom":
			if c, ok := el.Val.(*ast.MessageLiteralNode); ok {
				for _, f := range c.Elements {
					switch f.Name.Value() {
					case "kind":
						r.method = stringValue(f.Val)
					case "path":
						r.path = stringValue(f.Val)
					}
				}
			}
		case "body":
			r.body = stringValue(el.Val)
		case "additional_bindings":
			switch v := el.Val.(type) {
			case *ast.MessageLiteralNode:
				extra = append(extra, parseHTTPRule(v)...)
			case *ast.ArrayLiteralNode:
				for _, item := range v.Elements {
					if m, ok := item.(*ast.MessageLiteralNode); ok {
						extra = append(extra, parseHTTPRule(m)...)
					}
				}
			}
		}
	}
	if r.path == "" {
		return extra
	}
	return append([]httpRule{r}, extra...)
}

func stringValue(v ast.ValueNode) string {
	if s, ok := v.(ast.StringValueNode); ok {
		return s.AsString()
	}
	return ""
}

func mapEntry(m *descriptorpb.DescriptorProto, f *descriptorpb.FieldDescriptorProto) *descriptorpb.DescriptorProto {
	if f.GetLabel() != descriptorpb.FieldDescriptorProto_LABEL_REPEATED || isScalar(f) {
		return nil
	}
	for _, n := range m.GetNestedType() {
		if n.GetName() == f.GetTypeName() && n.GetOptions().GetMapEntry() {
			return n
		}
	}
	return nil
}

// isScalar 判断字段是否为标量类型；未链接的描述中消息与枚举字段的 Type 为空。
func isScalar(f *descriptorpb.FieldDescriptorProto) bool {
	return f.Type != nil && f.GetType() != descriptorpb.FieldDescriptorProto_TYPE_GROUP &&
		f.GetType() != descriptorpb.FieldDescriptorProto_TYPE_MESSAGE && f.GetType() != descriptorpb.FieldDescriptorProto_TYPE_ENUM
}

func typeName(f *descriptorpb.FieldDescriptorProto) string {
	if isScalar(f) {
		return strings.ToLower(strings.TrimPrefix(f.GetType().String(), "TYPE_"))
	}
	return strings.TrimPrefix(f.GetTypeName(), ".")
}

func isDeprecated(opts []*descriptorpb.UninterpretedOption) bool {
	for _, o := range opts {
		if optionName(o) == "deprecated" && o.GetIdentifierValue() == "true" {
			return true
		}
	}
	return false
}

func optionName(o *descriptorpb.UninterpretedOption) string {
	parts := make([]string, len(o.GetName()))
	for i, p := range o.GetName() {
		parts[i] = p.GetNamePart()
		if p.GetIsExtension() {
			parts[i] = "(" + parts[i] + ")"
		}
	}
	return strings.Join(parts, ".")
}

func optionValue(o *descriptorpb.UninterpretedOption) string {
	switch {
	case o.StringValue != nil:
		return string(o.GetStringValue())
	case o.IdentifierValue != nil:
		return o.GetIdentifierValue()
	case o.PositiveIntValue != nil:
		return strconv.FormatUint(o.GetPositiveIntValue(), 10)
	case o.NegativeIntValue != nil:
		return strconv.FormatInt(o.GetNegativeIntValue(), 10)
	case o.DoubleValue != nil:
		return strconv.FormatFloat(o.GetDoubleValue(), 'g', -1, 64)
	case o.AggregateValue != nil:
		return "{ " + o.GetAggregateValue() + " }"
	}
	return ""
}

func (g *gen) line(s string) { g.b.WriteString(s + "\n") }

func (g *gen) para(s string) {
	if s != "" {
		g.b.WriteString(s + "\n\n")
	}
}

// row 输出一行表格；单元格中的竖线需转义，否则会被当作列分隔符。
func (g *gen) row(cells ...string) {
	for i, c := range cells {
		cells[i] = strings.ReplaceAll(c, "|", `\|`)
	}
	g.line("| " + strings.Join(cells, " | ") + " |")
}

func qualify(scope, name string) string {
	if scope == "" {
		return name
	}
	return scope + "." + name
}

func sub(p []int32, tag int32, i int) []int32 {
	return append(append(make([]int32, 0, len(p)+2), p...), tag, int32(i))
}

func pathKey(p []int32) string { return fmt.Sprint(p) }
