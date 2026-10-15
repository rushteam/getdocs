# GetDocs

GetDocs 从多个 git 代码仓库同步 markdown 文档，按 **命名空间 → 文档树 → 文档** 组织并渲染成文档站点，同时提供 HTTP API、MCP 服务和面向 RAG 的同步扩展点。

- **多仓库**：每棵文档树对应一个仓库（或本地目录），可指定文档根目录、单个文件，或自动探测 `docs/`、`doc/`、`website/docs` 等目录；支持 include / exclude glob 过滤。
- **只拉文档**：浅克隆 + 部分克隆 + 稀疏检出，大型代码仓库也只下载提交信息、目录树和文档目录下的文件。
- **同步**：定时同步、手动同步（Web / API / CLI）、git 平台 push webhook。只改代码的提交不会产生新版本。
- **渲染**：GFM（表格、任务列表、删除线、自动链接）、脚注、定义列表、代码高亮、mermaid、GitHub 风格提示块（`> [!NOTE]`）、front matter、中文标题锚点；输出经过 XSS 清洗。仓库内的相对链接和图片会被改写为站内地址。
- **版本**：每次同步生成不可变快照，内容按哈希去重存储，保留最近 N 个版本。
- **AI 接入**：`llms.txt` / `llms-full.txt`、markdown 原文 API、内置全文检索（BM25，中文二元组切分）、MCP（Streamable HTTP 与 stdio）、按章节切片的 chunks API（支持增量）、同步钩子（导出文件 / 推送 webhook）。站点的 [`/ai`](#给-ai-与开发者使用) 页面列出了这些入口的实际地址。

## 快速开始

```sh
go install github.com/rushteam/getdocs/cmd/getdocs@latest
cp config.example.yaml getdocs.yaml   # 按需修改 namespaces
getdocs serve                         # http://localhost:8080
```

也可以克隆仓库后用 `make build` 构建，产物在 `bin/getdocs`。

运行依赖系统中的 `git`（≥ 2.27，需要 `sparse-checkout` 与 `--filter`）。

## 给 AI 与开发者使用

所有文档都能以 markdown 原文获取，读接口无需鉴权。服务运行后，打开站点顶栏的 **AI 接入**（`/ai`）可以看到下表中的实际地址和可直接运行的示例；它的 markdown 版本 `/ai.md` 可以整页交给 AI。

| 用途 | 地址 | 返回 |
|------|------|------|
| 文档索引 | `/llms.txt` | 接口用法 + 全部文档的标题与原文地址（[llms.txt](https://llmstxt.org) 约定） |
| 全部文档原文 | `/llms-full.txt[?namespace=&tree=]` | 所有文档拼接成一个文件，每篇用 `<document>` 标签包裹 |
| 单篇文档原文 | `/api/v1/trees/{ns}/{tree}/docs/{path}?format=raw` | `text/markdown`；`{path}` 可以是 `guide/install.md` 或页面 slug `guide/install` |
| 文档页原文 | `/docs/{ns}/{tree}/{slug}` + `Accept: text/markdown` | 同上，页面地址可以直接交给 agent |
| 文档目录 | `/api/v1/trees/{ns}/{tree}/docs` | JSON：路径、标题、页面地址 |
| 全文检索 | `/api/v1/search?q=` | JSON：文档树、路径、章节锚点、摘要 |
| RAG 切片 | `/api/v1/trees/{ns}/{tree}/chunks` | JSON：按章节切分的纯文本，`?since=` 增量 |
| MCP | `/mcp` | Streamable HTTP，工具 `list_trees` / `get_tree` / `search_docs` / `read_doc` |
| 接入说明 | `/ai`、`/ai.md` | 本节内容，地址已替换为当前服务 |

```sh
BASE=http://localhost:8080

curl $BASE/llms.txt                                                  # 有哪些文档
curl -G $BASE/api/v1/search --data-urlencode 'q=安装'                # 检索，取结果中的 key 与 path
curl "$BASE/api/v1/trees/platform/gateway/docs/guide/install.md?format=raw"   # 读原文
curl -H 'Accept: text/markdown' $BASE/docs/platform/gateway/guide/install     # 同上，用页面地址
curl "$BASE/llms-full.txt?namespace=platform&tree=gateway" > gateway.md       # 整棵树，粘贴给 AI
```

推荐给 agent 的流程：读 `llms.txt` 了解全貌 → 用检索 API 定位文档和章节 → 按 `?format=raw` 读原文。支持 MCP 的客户端直接接入 [MCP](#mcp) 即可，工具会完成同样的流程。

站点上也为人工使用提供了入口：首页底部有 `llms.txt` 链接；每个文档页底部有“Markdown 原文”和“复制 Markdown”（复制后可直接粘贴进对话）；页面头部声明了 `<link rel="alternate" type="text/markdown">`，能识别该标签的工具可以自动找到原文。

## 概念

| 层级 | 说明 | URL |
|------|------|-----|
| 命名空间 namespace | 一组相关文档树，如一个团队或产品线 | `/docs/{ns}/` |
| 文档树 tree | 一个仓库（或其中某个目录 / 文件）里的文档 | `/docs/{ns}/{tree}/` |
| 文档 doc | 一个 markdown 文件（开启 `proto` 后也包括 `.proto` 文件），URL 去掉扩展名；`README.md` / `index.md` 作为目录首页 | `/docs/{ns}/{tree}/guide/install` |

### 文档根

- `root: docs/guide`：以该目录为文档根。
- `root: README.md`：文档树只包含这一个文件。
- 留空：依次尝试 `docs`、`doc`、`documentation`、`wiki`、`website/docs`、`site/docs`、`docs/content`、`content/docs`，使用第一个包含文档的目录；都没有时扫描整个仓库。

### 过滤

`include` / `exclude` 是相对文档根的 glob，支持 `**`。不带通配符的模式按目录前缀匹配，例如 `drafts` 等价于排除 `drafts/` 下所有文件。设置 `include` 后只收录匹配的文档。`node_modules`、`vendor`、隐藏目录、`dist`、`build`、`target`、`_site` 始终排除。front matter 中 `draft: true` 的文档也不会收录。

图片等资源只有被文档引用时才会收录（可以位于文档根之外，如 `../assets/logo.png`）。

### protobuf 接口文档

文档树配置 `proto: true` 后，文档根下的 `.proto` 文件也会进入文档树，每个文件生成一页接口文档：

- 文件概览：包名、语法、导入与文件级选项（如 `go_package`）；
- 服务与方法：请求 / 响应类型、流式类型、`google.api.http` 注解的 HTTP 方法与路径（含 `additional_bindings` 与请求体）；
- 消息：字段表（字段、类型、编号、说明），涵盖 `repeated`、`optional`、`map`、`oneof`、嵌套消息、默认值与废弃标记；
- 枚举：值表；
- 源文件：页面底部折叠展示带高亮的原文。

说明取自 proto 源码中的前置注释与行尾注释，按 markdown 渲染。转换只做语法解析，不需要 import 的文件（如 googleapis）存在于仓库中：同文件内定义的类型链接到页内锚点，其他类型按源码写法展示。语法错误的文件会被跳过并记录日志。

生成的页面与 markdown 文档一样可被搜索、切片和通过 API 获取：`?format=raw`、llms.txt、MCP 返回的都是转换后的 markdown（front matter 带 `format: proto` 与 `package`），便于直接交给 AI 理解接口。markdown 文档中可以直接链接 `.proto` 文件，例如 `[用户接口](../proto/user/v1/user.proto#getuser)`，锚点为方法、消息或枚举名的小写形式（嵌套类型去掉点号，如 `User.Address` → `#useraddress`）。

```yaml
- name: user-api
  repo: https://github.com/example/apis.git
  root: proto
  proto: true
  exclude: ["third_party"]
```

### front matter

```yaml
---
title: 安装指南        # 默认取第一个标题，其次为文件名
weight: 10            # 导航排序，也识别 sidebar_position / order / nav_order
draft: true           # 不收录
---
```

## 配置

完整说明见 [config.example.yaml](config.example.yaml)。配置文件默认是当前目录下的 `getdocs.yaml`（`-config` 或环境变量 `GETDOCS_CONFIG` 可改），修改后重启服务生效；也可以[在页面添加](#在页面添加)仓库。所有字符串都支持 `${VAR}` / `${VAR:-默认值}`，令牌等敏感信息建议通过环境变量注入。

### 添加 git 仓库

每棵文档树对应一个仓库（或其中的一个目录 / 文件），在 `namespaces[].trees[]` 下加一项 `repo` 即可：

```yaml
server:
  base_url: https://docs.example.com
  admin_token: ${GETDOCS_ADMIN_TOKEN}
namespaces:
  - name: platform
    title: 平台服务
    trees:
      - name: gateway                                     # GitHub
        repo: https://github.com/example/gateway.git
        exclude: ["drafts/**"]
        auth: { token: "${GITHUB_TOKEN}" }
      - name: billing                                     # 自建 GitLab，SSH
        repo: git@gitlab.example.com:platform/billing.git
        root: docs/user-guide
        auth: { ssh_key: ~/.ssh/id_ed25519_docs }
      - name: order                                       # Gitee
        repo: https://gitee.com/example/order.git
        auth: { username: "${GITEE_USER}", token: "${GITEE_TOKEN}" }
      - name: cli                                         # Gitea
        repo: https://gitea.example.com/tools/cli.git
        root: README.md
      - name: deploy                                      # 域名看不出平台的自建服务
        repo: https://code.example.com/ops/deploy.git
        provider: gitlab
        auth: { token: "${CODE_TOKEN}" }
```

| 字段 | 说明 |
|------|------|
| `repo` | https 或 ssh 地址（`git@host:org/repo.git`、`ssh://git@host:2222/org/repo.git`）；本地目录用 `path` 代替 |
| `provider` | `github` / `gitlab` / `gitee` / `gitea` / `bitbucket`，留空按域名识别；用于生成“查看源文件”链接和令牌认证的默认用户名 |
| `branch` | 跟踪的分支，留空为远端默认分支 |
| `root`、`include`、`exclude` | 文档根与过滤规则，见上文“文档根” |
| `auth.token` / `auth.username` | HTTPS 访问令牌；公开仓库不用填 |
| `auth.ssh_key` | SSH 私钥路径，主机密钥首次连接时自动信任 |
| `browse_url` | 自定义“查看源文件”链接模板，可用 `{branch}` `{commit}` `{path}` |

各平台的令牌：

| 平台 | 令牌 | `auth.username` 默认值 | 浏览链接 |
|------|------|------------------------|----------|
| GitHub | Personal access token（Contents: Read） | `x-access-token` | `/blob/{branch}/{path}` |
| GitLab | Personal / Project access token（`read_repository`） | `oauth2` | `/-/blob/{branch}/{path}` |
| Gitee | 私人令牌（`projects`） | 无，**必须填 Gitee 用户名** | `/blob/{branch}/{path}` |
| Gitea / Codeberg | Access token（repository: Read） | `oauth2` | `/src/branch/{branch}/{path}` |
| Bitbucket | Repository access token；App password 需填真实用户名 | `x-token-auth` | `/src/{branch}/{path}` |
| 其他 git 服务 | 按服务要求填写 `username` 与 `token` | `oauth2` | 默认按 GitHub 规则，不对时配 `browse_url` |

令牌通过环境变量以 HTTP 头传给 git，不会写入 `.git/config`、进程参数或日志。添加后可在 `/status` 查看识别出的平台、同步结果与该树的 webhook 地址。

### 在页面添加

配置了 `server.admin_token` 后，`/status` 页面提供“添加文档树”，填写仓库地址、命名空间（不存在会自动创建）、名称、分支与文档根即可，保存后立即同步，无需重启。页面登记的文档树可在其状态页删除；配置文件里的文档树只能改配置文件。

- 登记结果保存在 `<storage.dir>/registry.json`，与配置合并，重名时以配置文件为准。存储通过 `TreeRegistry` 接口抽象，后续可替换为数据库等。
- 私有仓库的令牌不经过浏览器：在配置文件 `credentials` 中定义具名凭据，页面上按名称选择。
- 只接受 https / ssh 地址，https 地址中不能带用户名密码；解析到内网或本机地址的主机会被拒绝，自建平台需加入 `registry.allowed_hosts`（非空时只允许列出的主机）。

```yaml
credentials:
  - name: github-bot
    token: ${GITHUB_TOKEN}
registry:
  allowed_hosts: [gitlab.internal.example.com]
```

## 命令行

```text
getdocs serve [-config getdocs.yaml]     运行 Web / API / MCP 服务与定时同步（默认命令）
getdocs sync [-force] [ns/tree ...]      同步指定文档树（默认全部）后退出，结果以 JSON 输出
getdocs mcp [-allow-sync] [-refresh 30s] 以 stdio 运行 MCP 服务
getdocs version                          打印版本、提交、构建时间与 Go 版本
```

通用参数：`-config`（或环境变量 `GETDOCS_CONFIG`）、`-log-level`（或 `GETDOCS_LOG_LEVEL`：debug / info / warn / error）、`-log-json`。

## 同步

一次同步的流程：拉取最新提交 → 若提交与过滤配置都没变则跳过 → 确定文档根并按过滤规则选出文档 → 稀疏检出这些目录 → 解析文档、收集被引用的资源 → 与上个版本对比，文档没有变化则不产生新版本 → 保存快照 → 调用同步钩子。

失败的同步不会影响当前版本，站点继续提供上一个成功的快照。同一棵树的同步串行执行，同步期间收到的请求会合并为一次后续同步。

| 方式 | 用法 |
|------|------|
| 定时 | `sync.interval`（默认 15m），每棵树可用 `interval` 覆盖、`auto_sync: false` 关闭 |
| 启动时 | `sync.on_start: true`（默认） |
| Web | 状态页 `/status` 上的“同步”按钮 |
| API | `POST /api/v1/trees/{ns}/{tree}/sync`、`POST /api/v1/sync?namespace=` |
| CLI | `getdocs sync platform/gateway` |
| webhook | 见下文 |

### git 平台 webhook

地址填 `https://docs.example.com/api/v1/webhooks`，content type 选 `application/json`。服务会按事件里的仓库地址匹配所有使用该仓库的文档树，并忽略非跟踪分支和 tag 的推送。也可以用 `/api/v1/webhooks/{ns}/{tree}` 指定文档树。

| 平台 | 配置 |
|------|------|
| GitHub | Secret 填 `webhook_secret`，校验 `X-Hub-Signature-256` |
| Gitea / Gogs | Secret 填 `webhook_secret`，校验 `X-Gitea-Signature` |
| GitLab | Secret token 填 `webhook_secret`，校验 `X-Gitlab-Token` |
| Gitee | WebHook 密码或签名密钥填 `webhook_secret`，校验 `X-Gitee-Token`（签名模式同时校验 `X-Gitee-Timestamp`，偏差不超过 1 小时） |
| 其他 | `?token=<webhook_secret>` 或 `Authorization: Bearer <webhook_secret>` |

未配置 `webhook_secret` 时使用 `admin_token`；两者都为空时不校验。

## Web 与 API

页面：`/`（首页）、`/docs/{ns}/{tree}/{path}`、`/search?q=`、`/status`、`/ai`（接入说明，markdown 版 `/ai.md`）、`/llms.txt`、`/llms-full.txt`。文档页和 `/ai` 在请求头带 `Accept: text/markdown` 时返回 markdown。

| 接口 | 说明 |
|------|------|
| `GET /api/v1/namespaces` | 命名空间与文档树列表 |
| `GET /api/v1/trees/{ns}/{tree}` | 文档树状态：当前版本、提交、最近一次同步 |
| `GET /api/v1/trees/{ns}/{tree}/nav` | 导航树 |
| `GET /api/v1/trees/{ns}/{tree}/docs` | 文档列表 |
| `GET /api/v1/trees/{ns}/{tree}/docs/{path}` | 单篇文档；`?format=raw` 返回 markdown，`?format=html` 返回渲染结果 |
| `GET /api/v1/trees/{ns}/{tree}/chunks` | RAG 切片；`?path=` 单篇，`?since=<version>` 增量，`?size=` 切片大小 |
| `GET /api/v1/trees/{ns}/{tree}/versions` | 保留的历史版本 |
| `GET /api/v1/trees/{ns}/{tree}/runs` | 同步历史 |
| `GET /api/v1/search?q=&namespace=&tree=&limit=` | 全文检索 |
| `POST /api/v1/trees/{ns}/{tree}/sync?force=&wait=` | 触发同步（需 admin token）；默认异步返回 202，`wait=true` 等待结果 |
| `POST /api/v1/sync?namespace=&force=&wait=` | 批量同步（需 admin token） |
| `POST /api/v1/webhooks[/{ns}/{tree}]` | git 平台推送事件 |
| `POST /api/v1/trees` | 登记 git 文档树（需 admin token）：`{"namespace","name","repo","branch","root","provider","credential","title"}`，返回 201 并开始首次同步 |
| `DELETE /api/v1/trees/{ns}/{tree}` | 删除页面登记的文档树（需 admin token） |

读接口都支持 `?v=<version>` 读取历史版本。写接口需要 `Authorization: Bearer <admin_token>`，并拒绝浏览器跨站请求。

## 运维

服务基于 [beauty](https://github.com/rushteam/beauty) 框架运行，HTTP 服务统一提供以下能力：

| 能力 | 说明 |
|------|------|
| 健康检查 | `GET /healthz` 存活探针，始终返回 `{"status":"ok"}`；`GET /readyz` 就绪探针，至少一棵文档树有可用快照时返回 200，首次同步完成前或停机过程中返回 503 |
| 请求 ID | 透传请求头 `X-Request-ID`，没有时生成，写回响应头并记录在访问日志中，排查问题时可据此关联日志 |
| 访问日志 | 每个请求一条 `msg=access` 日志（方法、路径、状态码、耗时、请求 ID），5xx 为 warn 级别；探针请求不记录 |
| 压缩 | 客户端支持时对 1KB 以上的文本 / JSON 响应做 gzip 压缩 |
| 优雅退出 | 收到 SIGINT / SIGTERM 后停止接收新请求，等待进行中的请求与同步完成（最长 15 秒）；再次发送信号立即退出 |

**调试端口**：配置 `server.debug_addr`（如 `127.0.0.1:6060`）后，在该地址额外提供：

- `/debug/pprof/`：Go 性能分析，如 `go tool pprof http://127.0.0.1:6060/debug/pprof/heap`；
- `/debug/loglevel`：查看或在线调整日志级别，`curl -X PUT -d '{"level":"debug"}' http://127.0.0.1:6060/debug/loglevel`；
- `/debug/version`：构建信息（JSON）。

调试端口不鉴权，只应监听本机地址，远程排查请通过 SSH 隧道访问。

**OpenTelemetry**：配置 `telemetry.enabled: true` 后通过 OTLP/HTTP 上报 trace 与指标（含 Go runtime 指标），每个 HTTP 请求生成一个 span，日志自动附带 `trace_id` / `span_id`。上报地址、鉴权头等使用标准环境变量配置：

```sh
OTEL_EXPORTER_OTLP_ENDPOINT=http://otel-collector:4318 getdocs serve
```

## MCP

提供的工具：

| 工具 | 说明 |
|------|------|
| `list_trees` | 列出命名空间与文档树 |
| `get_tree` | 文档树目录：每篇文档的路径、标题和章节 |
| `search_docs` | 全文检索，返回路径、章节锚点与摘要 |
| `read_doc` | 读取文档 markdown，可通过 `section` 只取某一章节 |
| `sync_tree` | 触发同步，仅在 `mcp.allow_sync: true` 或 `-allow-sync` 时提供 |

另有资源模板 `docs://{namespace}/{tree}/{path}`。

**HTTP**：`serve` 时挂载在 `/mcp`（Streamable HTTP），`mcp.token` 非空时需要 Bearer 鉴权。

```sh
claude mcp add --transport http getdocs https://docs.example.com/mcp \
  --header "Authorization: Bearer $GETDOCS_MCP_TOKEN"
```

**stdio**：`getdocs mcp` 读取与 `serve` 相同的存储目录，并每 30 秒加载一次 `serve` 同步的新版本。如果没有运行 `serve`，先执行一次 `getdocs sync`。

```json
{
  "mcpServers": {
    "getdocs": {
      "command": "/usr/local/bin/getdocs",
      "args": ["mcp", "-config", "/etc/getdocs/getdocs.yaml"]
    }
  }
}
```

## RAG 扩展

每次同步产生新版本时，服务都会计算文档级变更（新增 / 修改 / 删除）和切片级增量。切片按标题划分章节，超长章节再按段落拆分。切片 ID 由文档树、路径、章节和序号决定，内容变化时 ID 不变，可以直接对向量库做 upsert / delete。

下游有四种接入方式：

1. **拉取**：`GET /api/v1/trees/{ns}/{tree}/chunks?since=<上次处理的版本>`，返回 `upsert` 与 `delete`。省略 `since` 时返回全量。
2. **文件导出**：配置 `hooks.chunk_export.dir`，每个新版本写入 `<dir>/<ns>/<tree>/<version>.json`。
3. **推送**：配置 `hooks.webhooks`，同步完成后 POST 事件（`with_chunks: true` 时附带切片增量），失败时按指数退避重试 2 次（4xx 响应视为请求错误，不重试），`X-Getdocs-Signature: sha256=<hmac>` 签名。
4. **代码内扩展**：实现 `docs.SyncHook`，在 `internal/bootstrap` 中通过 `svc.AddHook` 注册。

```go
type SyncHook interface {
	Name() string
	OnSync(ctx context.Context, ev docs.SyncEvent) error // ev 含 Previous / Current 快照与 Changes
}
```

钩子在快照保存之后执行，各自有超时，失败或 panic 只记录在同步记录的 `hook_errors` 里，不影响同步结果。服务启动从存储恢复快照时也会以 `ev.Restored = true` 调用钩子，便于重建内存索引；持久化的下游可以忽略这类事件。

推送事件示例：

```json
{
  "event": "docs.synced",
  "key": {"namespace": "platform", "tree": "gateway"},
  "version": "20261013T080000000000000-1a2b3c4d",
  "revision": {"commit": "…", "branch": "main", "author": "…", "message": "…"},
  "changes": {"added": ["guide/new.md"], "modified": [], "deleted": [], "unchanged": 12},
  "chunks": {"from_version": "…", "to_version": "…", "upsert": [{"id": "…", "path": "guide/new.md", "headings": ["安装"], "text": "…", "hash": "…"}], "delete": []}
}
```

替换内置检索（如改用向量检索）时，实现 `docs.Searcher`，并作为 `SyncHook` 注册以接收快照更新。Web 搜索页、API 和 MCP 的 `search_docs` 都会使用它。

## 架构

```text
cmd/getdocs              命令行入口
internal/
  domain/docs            领域模型与规则：快照、导航、过滤、链接解析、差异、切片；端口接口
  application/catalog    用例：同步、调度、快照构建与查询
  application/search     内置 BM25 检索（实现 Searcher 与 SyncHook）
  adapter/web            Web 页面、HTTP API、webhook
  adapter/mcp            MCP 工具与资源
  infra/git              git 拉取（实现 Fetcher）
  infra/markdown         goldmark 解析与渲染（实现 Parser）
  infra/storage          文件系统存储（实现 SnapshotStore / StateStore）
  infra/hooks            切片导出与 webhook 推送（实现 SyncHook）
  infra/config           YAML 配置
  bootstrap              组合根：装配各层，并以 beauty 应用运行 HTTP、定时同步与调试服务
```

依赖只指向内层：domain 只依赖标准库；application 只依赖 domain；外层实现 domain 定义的端口。beauty 框架只出现在外层（infra、bootstrap 与 cmd），定时调度等用例逻辑不依赖框架。

存储目录结构：

```text
data/
  blobs/ab/abcdef…                          文档原文与资源，按内容哈希去重
  trees/<ns>/<tree>/versions/<version>.json 快照清单
  trees/<ns>/<tree>/CURRENT                 当前版本
  trees/<ns>/<tree>/state.json              同步状态
  trees/<ns>/<tree>/runs.jsonl              同步历史（最近 200 条）
  repos/<hash>/                             git 工作副本缓存
```

## 开发

```sh
go test -race ./...
go vet ./...
make build   # 产物 bin/getdocs，构建时注入版本、提交与构建时间
```

容器运行（配置中 `storage.dir: /data`）：

```sh
make docker
docker run -p 8080:8080 -v $PWD/getdocs.yaml:/etc/getdocs/getdocs.yaml:ro -v getdocs-data:/data getdocs:dev
```

### 前端

页面不依赖前端构建工具，静态文件在 `internal/adapter/web/static/` 中，随二进制嵌入：

- `app.css`：设计令牌（颜色、圆角、阴影）定义在 `:root`，暗色主题在 `[data-theme="dark"]` 中覆盖。
- `app.js`：交互脚本。下拉选择、对话框和轻提示都是自绘组件，不使用浏览器原生的 `<select>` 与 `alert` / `confirm` / `prompt`。
  - 下拉选择的标记写在模板里（`data-select`），选中值通过隐藏输入框随表单提交。
  - 对话框由 `ask({mode: "alert" | "confirm" | "prompt", ...})` 创建，返回 Promise。
  - 按钮加上 `data-confirm="说明"` 后，点击会先弹出确认框。
- `fonts/`：自托管的 Geist 字体。页面的 CSP 不允许加载外部字体，所以字体随二进制一起分发，许可证见 `Geist-OFL.txt`（SIL OFL 1.1）。

## 许可证

[MIT](LICENSE)
