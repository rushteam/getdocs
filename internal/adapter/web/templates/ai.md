# AI 与开发者接入

{{.Title}} 的每篇文档都可以通过 HTTP 以 markdown 原文获取，可直接交给大模型、AI agent 或 RAG 管线使用。本页中的地址都是当前服务的实际地址，可以直接复制；本页的 markdown 版本位于 `{{.Base}}/ai.md`，可以整页交给 AI。

## 速查

| 用途 | 地址 | 返回 |
|------|------|------|
| 文档索引 | `{{.Base}}/llms.txt` | 全部文档的标题与原文地址 |
| 全部文档原文 | `{{.Base}}/llms-full.txt` | 所有文档拼接成一个文件，可用 `?namespace=&tree=` 过滤 |
| 单篇文档原文 | `{{.Base}}/api/v1/trees/{namespace}/{tree}/docs/{path}?format=raw` | `text/markdown` |
| 文档树列表 | `{{.Base}}/api/v1/namespaces` | JSON |
| 文档树目录 | `{{.Base}}/api/v1/trees/{namespace}/{tree}/docs` | JSON：路径、标题、页面地址 |
{{- if .Search}}
| 全文检索 | `{{.Base}}/api/v1/search?q={关键词}` | JSON：命中文档、章节锚点、摘要 |
{{- end}}
| RAG 切片 | `{{.Base}}/api/v1/trees/{namespace}/{tree}/chunks` | JSON：按章节切分的纯文本 |
{{- if .MCP}}
| MCP | `{{.Base}}/mcp` | Streamable HTTP{{if .MCPAuth}}，需要 Bearer 令牌{{end}} |
{{- end}}

`{path}` 是文档相对文档根的路径（如 `guide/install.md`），也可以使用页面地址中的 slug（如 `guide/install`）。所有读接口都无需鉴权。

开启了 `proto` 的文档树中，`.proto` 文件（如 `api/user/v1/user.proto`）同样是一篇文档：原文接口返回由它生成的 markdown 接口文档（服务、方法、HTTP 映射、消息字段、枚举，末尾附 proto 源码），front matter 中带 `format: proto` 与 `package`。

`{{.Base}}/readyz` 在至少一棵文档树同步完成后返回 200，可在批量拉取前用来确认内容可用。每个响应都带有 `X-Request-ID`；请求时自带该标头会原样回显，便于和服务端日志对应。

## 给 AI agent 的推荐流程

1. 读取 `{{.Base}}/llms.txt`，了解有哪些文档以及每篇文档的原文地址。
{{- if .Search}}
2. 不确定在哪篇文档时，调用 `{{.Base}}/api/v1/search?q=...` 检索，结果中的 `path` 可直接用于下一步。
3. 用 `{{.Base}}/api/v1/trees/{namespace}/{tree}/docs/{path}?format=raw` 读取文档原文。
{{- else}}
2. 用 `{{.Base}}/api/v1/trees/{namespace}/{tree}/docs/{path}?format=raw` 读取需要的文档原文。
{{- end}}
{{- if .MCP}}

支持 MCP 的客户端（Claude Code、Cursor 等）建议直接接入 [MCP](#mcp)，由 agent 自行检索和读取。
{{- end}}

在对话中临时提供上下文时，可以在文档页底部点击“复制 Markdown”，或把 `llms-full.txt` 的内容整体粘贴给 AI。

## llms.txt

`{{.Base}}/llms.txt` 遵循 [llms.txt](https://llmstxt.org) 约定：开头是站点说明与接口用法，之后按“命名空间 / 文档树”分组列出每篇文档的标题和原文地址。只需把这个地址告诉 AI，它就能找到全部文档。

`{{.Base}}/llms-full.txt` 包含所有文档的原文，每篇文档用 `<document>` 标签包裹，并标明来源：

```text
<document namespace="{{.Example.NS}}" tree="{{.Example.Tree}}" path="{{.Example.Path}}" title="…" url="…">
…markdown 原文…
</document>
```

内容较多时可以只取一棵文档树：`{{.Base}}/llms-full.txt?namespace={{.Example.NS}}&tree={{.Example.Tree}}`。

## 读取文档原文

```sh
# markdown 原文（含 front matter）
curl '{{.Example.Raw}}'

# JSON：元数据、标题列表、按章节切分的纯文本、源文件地址
curl '{{.Example.API}}'

# 文档树中的全部文档
curl '{{.Base}}/api/v1/trees/{{.Example.Key}}/docs'
```

其他方式：

- 文档页本身也支持内容协商：请求头带 `Accept: text/markdown` 时返回原文，例如 `curl -H 'Accept: text/markdown' '{{.Example.Page}}'`。
- 每个文档页的 HTML 头部都有 `<link rel="alternate" type="text/markdown">` 指向原文，页面底部有“Markdown 原文”链接。
- 所有读接口都可以加 `?v=<version>` 读取历史版本，版本号见 `{{.Base}}/api/v1/trees/{{.Example.Key}}/versions`。
- 原文响应带有 `ETag`（内容哈希），可用于判断文档是否变化。
{{- if .Search}}

## 检索

```sh
curl -G '{{.Base}}/api/v1/search' --data-urlencode 'q=安装' -d limit=5
```

可选参数：`namespace`、`tree`（需同时指定 `namespace`）、`limit`。返回示例：

```json
{
  "query": "安装",
  "hits": [
    {
      "key": {"namespace": "{{.Example.NS}}", "tree": "{{.Example.Tree}}"},
      "path": "{{.Example.Path}}",
      "slug": "{{.Example.Slug}}",
      "title": "…",
      "heading": "…",
      "anchor": "…",
      "snippet": "…",
      "score": 3.2,
      "url": "{{.Example.Page}}#…"
    }
  ]
}
```

取得 `key` 与 `path` 后即可拼出原文地址。
{{- end}}

## RAG 切片

```sh
# 全量切片
curl '{{.Base}}/api/v1/trees/{{.Example.Key}}/chunks'

# 单篇文档
curl '{{.Base}}/api/v1/trees/{{.Example.Key}}/chunks?path={{.Example.Path}}'

# 相对上次处理版本的增量：upsert 与 delete
curl '{{.Base}}/api/v1/trees/{{.Example.Key}}/chunks?since=<version>'
```

切片按标题划分章节，`size` 参数控制最大字符数（默认 1500）。切片 ID 由文档树、路径、章节和序号决定，内容变化时保持不变，可以直接对向量库做 upsert / delete。需要服务主动推送时，请让管理员配置同步钩子（`hooks.webhooks` / `hooks.chunk_export`）。
{{- if .MCP}}

## MCP

地址：`{{.Base}}/mcp`（Streamable HTTP）{{if .MCPAuth}}，请求需带 `Authorization: Bearer <token>`，令牌向管理员获取{{end}}。

| 工具 | 说明 |
|------|------|
| `list_trees` | 列出命名空间与文档树 |
| `get_tree` | 文档树目录：每篇文档的路径、标题和章节 |
{{- if .Search}}
| `search_docs` | 全文检索，返回路径、章节锚点与摘要 |
{{- end}}
| `read_doc` | 读取文档 markdown，可通过 `section` 只取某一章节 |

另有资源模板 `docs://{namespace}/{tree}/{path}`。

Claude Code：

```sh
claude mcp add --transport http getdocs {{.Base}}/mcp{{if .MCPAuth}} \
  --header "Authorization: Bearer $GETDOCS_MCP_TOKEN"{{end}}
```

其他客户端（Cursor、Windsurf 等的 `mcpServers` 配置）：

```json
{
  "mcpServers": {
    "getdocs": {
      "type": "http",
      "url": "{{.Base}}/mcp"{{if .MCPAuth}},
      "headers": {"Authorization": "Bearer <token>"}{{end}}
    }
  }
}
```
{{- end}}

## 当前文档树
{{if .Trees}}
| 命名空间 / 文档树 | 名称 | 文档数 | 文档列表 |
|------|------|------|------|
{{- range .Trees}}
| `{{.Key}}` | {{cell .Title}} | {{.Docs}} | `{{$.Base}}/api/v1/trees/{{.Key}}/docs` |
{{- end}}
{{else}}
还没有同步成功的文档树。
{{end}}
