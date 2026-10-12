# MCP 服务设计（2026-10-12）

## 目标

给博客增加一个 MCP（Model Context Protocol）服务，让 AI 客户端（Claude Desktop / Claude Code 等）
能够读取、新增与编辑博客文章。服务以 HTTP 方式接入现有站点，通过后台页面管理访问 Token。

## 范围

要做：

- 新增 `internal/mcp` 包，实现基于官方 MCP Go SDK 的工具服务
- 在 `blog serve` 上挂载 `/mcp` 端点（Streamable HTTP 传输）
- 提供 4 个工具：`create_post`、`update_post`、`list_posts`、`get_post`
- 新增 setting 键 `mcp_token` 与后台「MCP 接入」管理页
- `store` 新增 `UpdatePostByID`，按 ID 更新文章（含改 slug）

不做（YAGNI）：

- 不提供删除文章、发布/转草稿的独立工具（编辑工具已可改 `published`）
- 不提供 stdio 传输（仅 HTTP）
- 不提供标签管理、站点设置类工具
- 不修复后台编辑已有「改 slug 会新增重复文章」的历史缺陷（另行记录）

## 架构

### 集成方式

MCP 服务不是独立进程，而是集成进 `blog serve`：

```
blog serve
  ├── /            → 前台站点（web）
  ├── /admin/*     → 管理后台（admin）
  └── /mcp         → MCP 服务（mcp，新增）
```

三者共用同一个 `*store.Store`（即同一个 SQLite 连接）。连接池已被限制为单写连接
（`SetMaxOpenConns(1)`），因此 MCP 的写入与后台、前台的读写不会互相破坏；MCP 写入后前台立即可见。

### 包结构

新增包 `internal/mcp`，包名为 `mcp`，与 SDK 包同名，故 SDK 导入使用别名：

```go
sdk "github.com/modelcontextprotocol/go-sdk/mcp"
```

```
internal/mcp/
  server.go    // NewServer：构建 *sdk.Server 并注册工具
  handler.go   // NewHandler：Streamable HTTP handler + Bearer 认证中间件
  tools.go     // 工具入参/出参结构体与处理函数
  server_test.go
  handler_test.go
  tools_test.go
```

### 依赖

`go.mod` 新增 `github.com/modelcontextprotocol/go-sdk v1.8.0`（已核实可拉取）。
使用其中已确认的 API：

- `sdk.NewServer(impl *sdk.Implementation, opts *sdk.ServerOptions) *sdk.Server`
- `sdk.AddTool[In, Out any](s *sdk.Server, t *sdk.Tool, h sdk.ToolHandlerFor[In, Out])`
- `sdk.NewStreamableHTTPHandler(getServer func(*http.Request) *sdk.Server, opts *sdk.StreamableHTTPOptions) *sdk.StreamableHTTPHandler`

工具的输入/输出结构体由 SDK 经 `github.com/google/jsonschema-go` 自动生成 JSON Schema
（仅支持 JSON Schema 2020-12 草案），因此入参结构体只用基础类型与指针/切片，不用自定义 `MarshalJSON`。

## 认证与后台管理

### Token 存储

在 `internal/store/settings.go` 新增常量：

```go
SettingMCPToken = "mcp_token"
```

Token 明文存于 `settings` 表（与站点配置、管理员账号同表，本就明文存储）。

### 认证中间件

`/mcp` 外层中间件对每个请求：

1. 读取 `mcp_token`：
   - 读取失败 → 500
   - 值为空（未生成）→ **404**（默认关闭，避免未配置即暴露端点）
2. 校验 `Authorization: Bearer <token>`：
   - 缺失或格式不符 → **401**
   - 值不匹配（`subtle.ConstantTimeCompare`）→ **401**
3. 通过 → 交由 SDK handler 处理

### 后台管理页 `/admin/mcp`

在 `internal/admin` 增加：

- 路由（均经 `requireAuth`）：
  - `GET  /admin/mcp` → 展示当前 Token（可复制）、生成/撤销按钮
  - `POST /admin/mcp/token` → 生成或重置 Token
  - `POST /admin/mcp/token/delete` → 撤销 Token（清空）
- 生成 Token 复用 `auth.RandomToken`，加前缀 `bmt_` 便于识别
- 两个写操作沿用后台现有 CSRF 校验与 flash 提示机制
- 侧边栏新增「MCP 接入」入口（nav 常量与模板）

## 工具定义

工具出错时返回 `&sdk.CallToolResult{IsError: true, Content: []sdk.Content{&sdk.TextContent{Text: "..."}}}`，
而非协议层错误——让 LLM 能看到错误并自纠正。成功时 `Out` 为结构化结果，由 SDK 自动填入 `StructuredContent`。

### create_post — 新增文章

入参：

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `title` | string | 是 | 标题 |
| `content` | string | 是 | Markdown 正文 |
| `slug` | string | 否 | 别名；省略时由标题生成 |
| `summary` | string | 否 | 摘要；省略时自动截取 |
| `tags` | []string | 否 | 标签 |
| `published` | *bool | 否 | 默认 true |
| `createdAt` | string | 否 | RFC3339；省略时取当前时间 |

行为：

1. 校验 title、content 非空
2. 解析 slug：为空则 `store.Slugify(title)`；校验合法性（复用 admin 的 slug 规则，需抽为共享）
3. `SlugTaken(slug, 0)` 查重，已被占用则报错
4. `UpsertPost` 写入
5. 返回 `{id, slug, url}`

> slug 合法性校验：后台的 `validSlug` 目前是 `admin` 包私有函数。为复用，将其上移到
> `store` 包（如 `store.ValidSlug`），admin 改为调用它。属于服务本目标的小幅重构。

### update_post — 按 ID 编辑文章

入参：

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `id` | int64 | 是 | 文章 ID |
| `title` | *string | 否 | 传则更新 |
| `content` | *string | 否 | 传则更新 |
| `slug` | *string | 否 | 传则更新（需查重） |
| `summary` | *string | 否 | 传则更新 |
| `tags` | []string | 否 | 传则整体替换 |
| `published` | *bool | 否 | 传则更新 |

行为：

1. `GetPostByID(id)` 读出当前文章；不存在 → 报错
2. 未传的字段保留原值（指针为 nil 则不动）
3. 若传了 slug：`SlugTaken(newSlug, id)` 查重并校验合法性
4. 调用 **`store.UpdatePostByID(id, PostInput{...})`** 按 ID 更新
5. 返回 `{id, slug, url}`

### list_posts — 列出文章

入参：`status`（`all`|`published`|`draft`，默认 `all`）、`keyword`（可选）、`limit`（默认 20，上限 100）。

行为：复用 `ListPostsAdmin`，返回元数据列表（**不含正文**）：

```json
[{ "id": 12, "title": "...", "slug": "...", "summary": "...",
   "published": true, "tags": ["Go"], "createdAt": "...", "updatedAt": "..." }]
```

### get_post — 获取单篇全文

入参：`id`（必填）。

行为：复用 `GetPostByID`，返回元数据 + `content`（原始 Markdown）。

## store 层改动

新增：

```go
// UpdatePostByID 按 ID 更新文章并同步标签；id 不存在返回 ErrNotFound。
func (s *Store) UpdatePostByID(id int64, in PostInput) error
```

实现要点：

- 事务内 `UPDATE posts SET title, slug, summary, content, published, created_at, updated_at WHERE id = ?`
- `RowsAffected == 0` → `ErrNotFound`
- 与 `UpsertPost` 共享「删除并重建 `post_tags`」逻辑（抽成 `syncTags(tx, postID, names)` 私有函数）

并上移 slug 校验：

```go
// ValidSlug 校验文章别名，仅允许中文、字母、数字、连字符与下划线。
func ValidSlug(slug string) bool
```

`internal/admin` 中 `validSlug` 改为薄封装或直接调用 `store.ValidSlug`。

**为什么必须新增 `UpdatePostByID`**：`UpsertPost` 无 ID 参数，靠 slug 定位。若 `update_post`
改了 slug，`UpsertPost` 会查不到原记录而插入一条新文章，造成重复。按 ID 更新才符合语义。

## CLI 与文档

- `main.go`：
  - `cmdServe` 中构造 `mcpSrv` 并 `root.Handle("/mcp", mcpSrv.Handler())`
  - MCP handler 放在认证中间件内层；`/mcp` 需在未初始化站点时也能给出明确响应（默认 token 为空则 404）
  - `usage()` 增加 MCP 一行说明
- `README.md`：新增「MCP 接入」章节（后台生成 Token → 客户端配置示例 → 可用工具表）

## 测试策略

| 层 | 测试 |
|---|---|
| `store` | `UpdatePostByID`：正常更新、改 slug、slug 冲突、id 不存在；`ValidSlug` 边界 |
| `store` | `syncTags` 行为经 `UpsertPost`/`UpdatePostByID` 间接覆盖 |
| `internal/mcp` 工具 | 用 `sdk.NewInMemoryTransports()` 对接 server 与 client，`CallTool` 验证 create/update/list/get 的正常与错误路径 |
| `internal/mcp` 认证 | 无 token → 404、错 token → 401、对 token → 透传 |
| `internal/admin` | MCP 页面渲染；生成/撤销 token 的写库结果 |

## 已知边界

- MCP 写入后前台立即可见（同一 DB，无缓存层）
- 修改 slug 后旧链接失效（无重定向），与现有后台编辑行为一致
- Token 明文存于数据库 `settings` 表；建议配合 HTTPS 使用
- `/mcp` 的认证只校验 Token，不区分请求来源 IP
- 后台编辑改 slug 会新增重复文章的历史缺陷不在本次修复范围（另行记录 issue）
