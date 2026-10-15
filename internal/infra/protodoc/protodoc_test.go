package protodoc

import (
	"os"
	"strings"
	"testing"

	"github.com/rushteam/getdocs/internal/domain/docs"
	"github.com/rushteam/getdocs/internal/infra/markdown"
)

func convertFixture(t *testing.T) string {
	t.Helper()
	src, err := os.ReadFile("testdata/user.proto")
	if err != nil {
		t.Fatal(err)
	}
	out, err := Convert("api/user/v1/user.proto", src)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestConvert(t *testing.T) {
	md := convertFixture(t)
	for _, want := range []string{
		"title: user.proto\n",
		"description: 用户服务接口定义。\n",
		"format: proto\n",
		"package: acme.user.v1\n",
		"# user.proto\n\n用户服务接口定义。\n\n提供用户的增删改查。",
		"| 包 | `acme.user.v1` |",
		"`google/api/annotations.proto`<br>",
		"| `go_package` | `github.com/acme/api/user/v1;userv1` |",
		// 服务与方法
		"### UserService\n\nUserService 管理用户。",
		"| [GetUser](#getuser) | [`GetUserRequest`](#getuserrequest) | [`User`](#user) | `GET /v1/users/{id}` 获取单个用户。 |",
		"- HTTP：`POST /v1/users`，请求体 `user`",
		"- HTTP：`POST /v1/orgs/{org}/users`，请求体 `*`",
		"- 响应：stream [`UserEvent`](#userevent)\n- 流式：服务端流",
		"- 流式：双向流\n- **已废弃**",
		// 消息与字段
		"| `id` | `string` | 1 | 用户 ID |",
		"| `name` | `string` | 2 | 显示名称，最长 64 个字符。 |",
		"| `status` | [`Status`](#status) | 3 |  |",
		"| `tags` | repeated `string` | 4 |  |",
		"| `labels` | `map<string, string>` | 5 | 自定义标签 |",
		"| `create_time` | `google.protobuf.Timestamp` | 6 |  |",
		"| `email` | optional `string` | 7 |  |",
		"| `address` | [`Address`](#useraddress) | 9 | oneof `contact` |",
		"| `legacy` | `string` | 10 | **已废弃** |",
		"### User.Address\n\n地址 | 含竖线。",
		"| `kind` | [`Kind`](#useraddresskind) | 2 |  |",
		"| `page` | `acme.common.v1.PageToken` | 2 |  |",
		"### WatchUsersRequest\n\n没有字段。",
		// 枚举
		"| `ACTIVE` | 1 | 正常 |",
		"| `DISABLED` | 2 | **已废弃** |",
		"### User.Address.Kind",
		// 源文件
		"<summary>user.proto</summary>\n\n```protobuf\n// 用户服务接口定义。",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown missing %q", want)
		}
	}
	if strings.Contains(md, "LabelsEntry") {
		t.Error("map entry message should not be listed")
	}
}

// 生成的锚点必须与 markdown 引擎渲染出的标题 id 一致，页内类型链接才能跳转。
func TestAnchorsMatchRenderedHeadings(t *testing.T) {
	md := convertFixture(t)
	parsed, err := markdown.New().Parse("user.proto", []byte(md))
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, h := range parsed.Headings {
		ids[h.ID] = true
	}
	for _, l := range parsed.Links {
		if id, ok := strings.CutPrefix(l, "#"); ok && !ids[id] {
			t.Errorf("anchor %q has no matching heading", l)
		}
	}
	if !ids["useraddresskind"] || !ids["getuser"] {
		t.Errorf("headings = %v", parsed.Headings)
	}
}

func TestConvertShortHTTPOptionAndProto2(t *testing.T) {
	src := `syntax = "proto2";
package demo;
service S {
  rpc Del(Req) returns (Req) {
    option (google.api.http).delete = "/v1/items/{id}";
  }
}
message Req {
  required string id = 1;
  optional int32 n = 2 [default = 5];
  /* 多行
   * 块注释
   */
  optional bytes raw = 3;
}
`
	out, err := Convert("demo.proto", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	md := string(out)
	for _, want := range []string{
		"`DELETE /v1/items/{id}`",
		"| `id` | required `string` | 1 |  |",
		"| `n` | optional `int32` | 2 | 默认 `5` |",
		"| `raw` | optional `bytes` | 3 | 多行 块注释 |",
		"| 语法 | `proto2` |",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown missing %q\n%s", want, md)
		}
	}
}

// 注释按 markdown 渲染，但不能借此注入标题或 HTML。
func TestCommentsAreContained(t *testing.T) {
	src := "syntax = \"proto3\";\n// # 不是标题\n// 返回 List<Item>，见 `Map<K,V>`。\n// <script>alert(1)</script>\nmessage M {}\n"
	out, err := Convert("m.proto", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	md := string(out)
	for _, want := range []string{"\\# 不是标题", "List&lt;Item&gt;", "`Map<K,V>`", "&lt;script&gt;"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown missing %q\n%s", want, md)
		}
	}
	parsed, _ := markdown.New().Parse("m.proto", out)
	for _, h := range parsed.Headings {
		if strings.Contains(h.Text, "不是标题") {
			t.Errorf("comment became a heading: %v", h)
		}
	}
}

func TestConvertInvalid(t *testing.T) {
	_, err := Convert("bad.proto", []byte("syntax = \"proto3\";\nmessage {\n"))
	if err == nil || !strings.Contains(err.Error(), "bad.proto:2") {
		t.Fatalf("err = %v, want position", err)
	}
}

func TestParser(t *testing.T) {
	p := NewParser(markdown.New())
	src, _ := os.ReadFile("testdata/user.proto")
	d, err := p.Parse("api/user/v1/user.proto", src)
	if err != nil {
		t.Fatal(err)
	}
	if d.Title != "user.proto" || d.Meta["format"] != "proto" || !strings.HasPrefix(string(d.Content), "---\n") {
		t.Errorf("parsed = %q %v", d.Title, d.Meta)
	}
	var found bool
	for _, s := range d.Sections {
		if s.Heading == "GetUser" && strings.Contains(s.Text, "GET /v1/users/{id}") {
			found = true
		}
	}
	if !found {
		t.Errorf("sections do not include method text: %+v", d.Sections)
	}

	md, err := p.Parse("README.md", []byte("# Hi\n"))
	if err != nil || md.Title != "Hi" || md.Content != nil {
		t.Errorf("markdown parse = %+v, %v", md, err)
	}
	var _ docs.Parser = p
}
