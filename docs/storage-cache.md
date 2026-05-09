# 存储与缓存

<cite>
**本文档引用的文件**
- [internal/chathistory/store.go](file://internal/chathistory/store.go)
- [internal/chathistory/sqlite_store.go](file://internal/chathistory/sqlite_store.go)
- [internal/chathistory/sqlite_detail.go](file://internal/chathistory/sqlite_detail.go)
- [internal/config/account_sqlite.go](file://internal/config/account_sqlite.go)
- [internal/responsecache/cache.go](file://internal/responsecache/cache.go)
- [config.example.json](file://config.example.json)
</cite>

## 目录

1. [简介](#简介)
2. [项目结构](#项目结构)
3. [核心组件](#核心组件)
4. [架构总览](#架构总览)
5. [详细组件分析](#详细组件分析)
6. [性能考虑](#性能考虑)
7. [故障排查指南](#故障排查指南)
8. [结论](#结论)

## 简介

当前项目有三类本地状态：账号 SQLite、SQLite 对话历史和协议响应缓存。账号池由 `data/accounts.sqlite` 单独保存，避免继续把大量账号写入 JSON；对话历史默认上限为 2 万条，达到 2 万条时会批量清理旧记录，仅保留最近 500 条详情，同时把已清理记录的累计指标写入 meta，避免总览页总请求数被物理保留上限截断。响应缓存用于减少相同协议请求重复打到上游，默认内存 5 分钟、磁盘 4 小时，并对磁盘内容启用 gzip；同一调用方、路径、请求体和关键请求头的并发未命中会合并为一次上游请求，其余请求等待并复用完整结果。

**章节来源**
- [internal/chathistory/store.go](file://internal/chathistory/store.go)
- [internal/responsecache/cache.go](file://internal/responsecache/cache.go)

## 项目结构

```mermaid
graph TB
subgraph "Chat History"
STORE["internal/chathistory<br/>Store facade"]
SQLITE["data/chat_history.sqlite<br/>SQLite WAL"]
DETAIL["detail_blob<br/>gzip detail"]
LEGACY["data/chat_history.json<br/>legacy import source"]
end
subgraph "Account Store"
ACCDB["internal/config/account_sqlite.go<br/>accountSQLiteStore"]
ACCSQL["data/accounts.sqlite<br/>账号池"]
end
subgraph "Response Cache"
CACHE["internal/responsecache<br/>middleware"]
MEM["memory map<br/>5 min default"]
DISK["data/response_cache<br/>json.gz files"]
end
STORE --> SQLITE
SQLITE --> DETAIL
LEGACY --> SQLITE
ACCDB --> ACCSQL
CACHE --> MEM
CACHE --> DISK
```

**图表来源**
- [internal/chathistory/sqlite_store.go](file://internal/chathistory/sqlite_store.go)
- [internal/chathistory/sqlite_detail.go](file://internal/chathistory/sqlite_detail.go)
- [internal/responsecache/cache.go](file://internal/responsecache/cache.go)

**章节来源**
- [config.example.json](file://config.example.json)

## 核心组件

- `chat_history` 表：存储摘要字段、状态、模型、账号、耗时、状态码、usage、详情版本。
- `detail_blob`：保存 gzip 压缩后的完整详情，`detail_json` 只用于旧数据迁移。
- `chat_history_meta`：保存保留上限、版本、修订号，以及被批量清理记录的累计请求、成功率和 token 统计。
- `accounts` 表：保存账号标识、邮箱、手机号、密码、运行态 token 和代理绑定；旧配置中的 `accounts` 会在账号库为空时自动迁移。
- `responsecache.Cache`：在路由中间件层读取请求体、计算缓存键、命中回放、未命中捕获响应并写入缓存。
- 磁盘缓存文件：以 `.json.gz` 保存，包含状态码、响应头、响应体、创建时间和过期时间。

**章节来源**
- [internal/chathistory/sqlite_store.go](file://internal/chathistory/sqlite_store.go)
- [internal/responsecache/cache.go](file://internal/responsecache/cache.go)

## 架构总览

```mermaid
sequenceDiagram
participant Client as Client
participant Router as Router Middleware
participant Cache as Response Cache
participant Handler as Protocol Handler
participant History as SQLite History
participant DS as DeepSeek
Client->>Router: POST protocol request
Router->>Cache: cacheable path?
Cache-->>Client: cache hit replay
Cache->>Handler: cache miss
Handler->>History: start history item
Handler->>DS: upstream request
DS-->>Handler: stream or response
Handler->>History: update detail and status
Handler-->>Cache: captured 2xx response
Cache->>Cache: memory + gzip disk store
Cache-->>Client: response
```

**图表来源**
- [internal/server/router.go](file://internal/server/router.go)
- [internal/responsecache/cache.go](file://internal/responsecache/cache.go)
- [internal/chathistory/sqlite_write.go](file://internal/chathistory/sqlite_write.go)

**章节来源**
- [internal/server/router.go](file://internal/server/router.go)
- [internal/httpapi/historycapture/capture.go](file://internal/httpapi/historycapture/capture.go)

## 详细组件分析

### SQLite 历史记录

默认路径为 `data/chat_history.sqlite`。启动时会：

- 创建目录和 SQLite 连接。
- 设置 WAL、`synchronous=NORMAL`、`busy_timeout=5000`。
- 建表和索引。
- 从旧 `data/chat_history.json` 首次导入。
- 将上次未完成请求标记为停止。
- 压缩旧的未压缩详情，并执行 checkpoint/VACUUM。

历史保留上限由数据库 meta 保存，默认和最大值都是 `20000`。当 `limit=20000` 且记录数达到阈值时，系统会把较旧的 `19500` 条记录滚入累计指标并删除详情，只保留最近 `500` 条可展开记录；总览页的总请求数、成功率和总 token 会继续使用“已清理累计 + 当前保留记录”的口径。

### 账号 SQLite

默认路径为 `data/accounts.sqlite`，也可以通过 `storage.accounts_sqlite_path` 或 `DEEPSEEK_WEB_TO_API_ACCOUNTS_SQLITE_PATH` 覆盖。启动时会创建账号表和邮箱/手机号唯一索引；如果旧配置文件或 `.env` 的结构化 JSON 中仍带 `accounts`，且账号库为空，会自动导入一次。管理台新增、编辑、批量导入账号时，内存快照和 SQLite 会一起更新；保存结构化配置时会剥离 `accounts` 字段。

### 响应缓存

默认缓存路径为 `data/response_cache`。缓存覆盖：

- OpenAI Chat Completions、Responses、Embeddings。
- Claude Messages、CountTokens。
- Gemini GenerateContent、StreamGenerateContent。

缓存键包含调用方、规范化路径、查询参数、影响输出的请求头和规范化 JSON 请求体。`Content-Type`、`Accept` 等请求头会做等价规范化，减少 `application/json` 与 `application/json; charset=utf-8` 这类碎片。部分缓存控制字段会从 JSON key 中忽略，以提高相同内容请求的命中率。

响应缓存默认只缓存完整的非流式响应。OpenAI、Claude、Gemini 的 `stream=true` 请求以及 Gemini `:streamGenerateContent` 端点都会按 `stream_request` 计入不可缓存原因，不写入响应缓存；如果上游返回 `text/event-stream`，也会按 `stream_response` 拒绝写入。不可缓存统计还会区分 `oversized_request`、`missing_owner`、`status_non_2xx`、`response_no_store`、`set_cookie` 等原因，便于在总览页判断命中率低是语义不可缓存还是缓存碎片导致。

### 会话缓存

OpenAI Chat Completions 和 Responses 会在 cache miss 进入上游时复用 DeepSeek `chat_session_id`。缓存只保存远端 session ID，不保存 prompt 正文，也不会把普通请求改成 `parent_message_id` 链式续写。

会话缓存按调用方、稳定 `SessionKey`、账号或直通 token hash、API surface、模型/model_type、thinking/search 隔离。默认开启，TTL 为 `cache.session.ttl_seconds=7200`，最多 `cache.session.max_entries=50000` 条；当 `auto_delete.mode` 为 `single` 或 `all` 时会自动绕过，避免复用已删除的远端 session。协议别名路径会归一到同一个 `SessionKey`，例如 `/v1/chat/completions`、`/chat/completions`、`/v1/v1/chat/completions` 会共享 affinity 作用域。若远端刚创建的 session 或缓存命中的 session 立刻返回 not found/expired，系统会失效该 key 并重建一次。

### Prompt Prefix 诊断

OpenAI Chat Completions 和 Responses 会记录 prompt prefix cache 诊断数据，用于对齐 OpenAI/Claude 的“稳定前缀更容易命中上游缓存”语义。诊断 tracker 只保存 prefix hash、估算 token 和时间戳，不保存 prompt 原文，也不会复用旧答案。

prefix 边界固定为 tools/system/developer/历史消息以及最后一条消息之前的内容；最后一条消息视为 tail。请求体 `prompt_cache_key` 或请求头 `X-DeepSeek-Web-To-API-Prompt-Cache-Key` 会作为 hint 参与 tracker 分组，但不会转发给 DeepSeek，也不会加入完整响应缓存 key。管理台总览的 `prompt_cache` 字段展示 observed、eligible、reused、reuse_rate、estimated_read_tokens、estimated_write_tokens、last_prefix_hash 和 last_hint_present；`usage.prompt_tokens_details.cached_tokens` 仍保持真实上游语义，不写入本地估算值。

**章节来源**
- [internal/chathistory/sqlite_store.go](file://internal/chathistory/sqlite_store.go)
- [internal/chathistory/sqlite_import.go](file://internal/chathistory/sqlite_import.go)
- [internal/responsecache/cache.go](file://internal/responsecache/cache.go)
- [internal/sessioncache/cache.go](file://internal/sessioncache/cache.go)
- [internal/promptcache/cache.go](file://internal/promptcache/cache.go)

## 性能考虑

- SQLite 单连接配合 WAL，适合本地嵌入式记录和管理台读取。
- 账号 SQLite 使用独立文件，和聊天历史分离，账号批量导入不会撑大 `config.json` 或 `.env`。
- 历史详情使用 `gzip.BestCompression`，节省磁盘空间，读取详情时按需解压。
- 响应缓存的内存层有总字节数上限，命中时刷新内存 TTL，并按最近访问时间做 LRU 淘汰；磁盘 TTL 仍是绝对上限，磁盘层会按过期和容量删除旧文件。
- 大请求体或大响应超过 `cache.response.max_body_bytes` 时不会进入缓存。
- 同 key 并发未命中会合并为一次上游请求，能降低突发重复请求对 DeepSeek 的压力。
- 会话缓存是内存态短 TTL 缓存，只复用 `chat_session_id`，不会跨账号或跨直通 token 共享。
- Prompt prefix tracker 默认 TTL 为 10 分钟，最多 10 万条，只用于诊断稳定前缀是否复现，不参与答案复用。

**章节来源**
- [internal/chathistory/sqlite_detail.go](file://internal/chathistory/sqlite_detail.go)
- [internal/responsecache/cache.go](file://internal/responsecache/cache.go)

## 故障排查指南

- 历史列表数量不是 2 万：这是长保留模式的预期行为，达到 2 万后会自动压缩为最近 500 条；如需确认累计量，请看总览页或 `chat_history_meta` 中的清理累计指标。若提前清理，检查管理台保留策略或 `chat_history_meta.limit` 是否被改成 10、20、50 或关闭。
- SQLite 文件过大：确认当前版本已启动过，旧未压缩详情会在启动时分批压缩并 VACUUM。
- 账号导入后 JSON 里看不到账号：这是预期行为，账号已经进入 `data/accounts.sqlite`；管理台账号列表和批量导出会从内存快照读取。
- 缓存命中率低：检查请求体中是否存在每次变化的字段、是否显式 `no-cache`、是否跨 API Key/调用方；响应缓存会规范化协议别名路径和常见内容协商头，但语义不同的请求头、模型或请求体仍会拆分 key。若关注会话复用，再确认 `SessionKey` 稳定且 `auto_delete.mode=none`。
- 磁盘缓存未生效：确认 `cache.response.dir` 可写，且响应为 2xx、非流式、无 `Set-Cookie`、无 `Cache-Control: no-store`、响应体未超过上限。磁盘写失败不会计入成功 store。
- Prompt prefix 复用率低：检查 system/tools/历史内容是否每次变化，是否只有单轮纯 user 请求，或者 `prompt_cache_key` / `X-DeepSeek-Web-To-API-Prompt-Cache-Key` 是否在同一会话中不稳定。

**章节来源**
- [internal/chathistory/store.go](file://internal/chathistory/store.go)
- [internal/responsecache/cache.go](file://internal/responsecache/cache.go)

## 结论

账号 SQLite、历史 SQLite 和 gzip 响应缓存是当前版本的核心运行态能力：账号库让批量账号脱离 JSON，历史库服务管理台与问题回溯，缓存降低重复请求成本。三者都使用本地文件系统，部署时应持久化 `data/` 或至少持久化配置指定的账号、历史与缓存路径。

**章节来源**
- [config.example.json](file://config.example.json)
