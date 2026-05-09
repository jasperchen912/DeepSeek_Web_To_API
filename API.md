# DeepSeek_Web_To_API API 文档

语言 / Language: [中文](API.md) | [English](API.en.md)

本文记录当前 Go 代码库实际暴露的 HTTP 接口。默认 Base URL 为 `http://127.0.0.1:5001`。

## 基础规则

- 请求体：JSON 接口要求合法 UTF-8。
- 健康检查：`GET /healthz`、`HEAD /healthz`、`GET /readyz`、`HEAD /readyz`。
- 业务鉴权：`Authorization: Bearer <token>`、`x-api-key: <token>`；Gemini 兼容 `x-goog-api-key`、`?key=`、`?api_key=`。
- 托管账号模式：token 命中 `config.json` 的 `keys` 后使用账号池。
- 直通 token 模式：token 不在 `keys` 中时作为 DeepSeek token 直通。
- 管理鉴权：`POST /admin/login` 获取 JWT，其余管理接口使用 `Authorization: Bearer <jwt>` 或管理密钥。
- 缓存命中响应头：`X-DeepSeek-Web-To-API-Cache: memory|disk|singleflight`、`X-DeepSeek-Web-To-API-Cache-Expires-At`。

## OpenAI 兼容接口

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| `GET` | `/v1/models` | 返回可见模型列表 |
| `GET` | `/v1/models/{model_id}` | 查询单个模型，支持 alias |
| `POST` | `/v1/chat/completions` | Chat Completions，支持流式 |
| `POST` | `/v1/responses` | Responses，支持流式和响应暂存 |
| `GET` | `/v1/responses/{response_id}` | 读取暂存 Response |
| `POST` | `/v1/files` | 上传文件并写入兼容引用 |
| `POST` | `/v1/embeddings` | Embeddings 兼容响应 |

同时支持 `/models`、`/chat/completions`、`/responses`、`/files`、`/embeddings` 根路径别名，以及 `/v1/v1/*` 兼容别名。

`/v1/models` 和 `/v1/models/{model_id}` 的 DeepSeek 模型对象包含 OpenAI 基础字段，并额外返回 `input`、`output` 和零值 `cost`，用于兼容会读取模型能力或计费单价的网关。Chat Completions 的 `usage` 保留 `prompt_tokens`、`completion_tokens`、`total_tokens`，并补充 `prompt_tokens_details`、`completion_tokens_details`、`input`、`output`、`cacheRead`、`cacheWrite`、`totalTokens` 和零值 `cost`。

示例：

```bash
curl http://127.0.0.1:5001/v1/chat/completions \
  -H "Authorization: Bearer your-api-key-1" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-5.5",
    "messages": [{"role": "user", "content": "你好"}],
    "stream": true
  }'
```

## Claude 兼容接口

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| `GET` | `/anthropic/v1/models` | Claude 风格模型列表 |
| `POST` | `/anthropic/v1/messages` | Claude Messages，支持流式 |
| `POST` | `/anthropic/v1/messages/count_tokens` | Token 估算 |
| `POST` | `/v1/messages`、`/messages` | Messages 别名 |
| `POST` | `/v1/messages/count_tokens`、`/messages/count_tokens` | Count Tokens 别名 |

示例：

```bash
curl http://127.0.0.1:5001/anthropic/v1/messages \
  -H "x-api-key: your-api-key-1" \
  -H "anthropic-version: 2023-06-01" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "claude-sonnet-4-6",
    "max_tokens": 1024,
    "messages": [{"role": "user", "content": "你好"}],
    "stream": true
  }'
```

## Gemini 兼容接口

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| `POST` | `/v1beta/models/{model}:generateContent` | 非流式内容生成 |
| `POST` | `/v1beta/models/{model}:streamGenerateContent` | 流式内容生成 |
| `POST` | `/v1/models/{model}:generateContent` | v1 别名 |
| `POST` | `/v1/models/{model}:streamGenerateContent` | v1 流式别名 |

示例：

```bash
curl "http://127.0.0.1:5001/v1beta/models/gemini-2.5-pro:generateContent?key=your-api-key-1" \
  -H "Content-Type: application/json" \
  -d '{
    "contents": [{"role": "user", "parts": [{"text": "你好"}]}]
  }'
```

## Admin 接口

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| `POST` | `/admin/login` | 登录管理台 |
| `GET` | `/admin/verify` | 校验 JWT |
| `GET` / `POST` | `/admin/config` | 读取或更新完整配置 |
| `POST` | `/admin/config/import` | 导入配置 |
| `GET` | `/admin/config/export` | 导出配置 |
| `GET` / `POST` | `/admin/accounts` | 查询或新增账号 |
| `PUT` / `DELETE` | `/admin/accounts/{identifier}` | 更新或删除账号 |
| `POST` | `/admin/accounts/test` | 测试单账号 |
| `POST` | `/admin/accounts/test-all` | 批量测试账号 |
| `GET` | `/admin/queue/status` | 账号池与队列状态 |
| `GET` / `POST` | `/admin/proxies` | 查询或新增代理 |
| `PUT` / `DELETE` | `/admin/proxies/{proxyID}` | 更新或删除代理 |
| `GET` | `/admin/chat-history` | 分页查看历史记录 |
| `GET` | `/admin/chat-history/{id}` | 查看历史详情 |
| `DELETE` | `/admin/chat-history` | 清空历史记录 |
| `DELETE` | `/admin/chat-history/{id}` | 删除单条历史 |
| `PUT` | `/admin/chat-history/settings` | 修改历史保留策略 |
| `GET` | `/admin/metrics/overview` | 总览指标 |
| `GET` | `/admin/version` | 版本信息 |

## 缓存规则

缓存覆盖 OpenAI Chat/Responses/Embeddings、Claude Messages/CountTokens、Gemini GenerateContent/StreamGenerateContent。请求键按调用方、协议路径、查询参数、影响输出的规范化请求头和规范化 JSON 请求体隔离；常见协议别名路径、`Content-Type` 和 `Accept` 等等价形式会归一。同 key 并发未命中会合并为一次上游请求，其余请求等待并复用结果，响应头会标记 `singleflight`。

以下场景不写入缓存：流式请求或流式响应、非 2xx、请求体或响应体过大、请求显式绕过、无法确定调用方、非缓存路径、响应 `Set-Cookie`、响应 `Cache-Control: no-store`。

OpenAI Chat Completions 与 Responses 还会复用 DeepSeek `chat_session_id`。会话缓存按调用方、稳定 SessionKey、账号或直通 token hash、模型/model_type、thinking/search 和 API surface 隔离；默认开启，可通过 `cache.session.enabled=false` 关闭。它只复用远端 session 容器，不改变 prompt 组装，也不会把普通请求改成 `parent_message_id` 链式续写。若远端 session 新建后或缓存命中后立刻返回 not found/expired，会失效并重建一次。

OpenAI Chat/Responses 会记录 prompt prefix cache 诊断指标：稳定 prefix hash、估算 prefix/tail tokens、是否在本地 tracker 中复用。请求体 `prompt_cache_key` 或请求头 `X-DeepSeek-Web-To-API-Prompt-Cache-Key` 可作为诊断 hint；该 hint 不转发 DeepSeek，不加入完整响应缓存 key，也不会让旧答案被复用。`usage.prompt_tokens_details.cached_tokens` 仍保持上游真实语义，当前不会写入本地估算值。

显式绕过：

```http
Cache-Control: no-cache
X-DeepSeek-Web-To-API-Cache-Control: bypass
```

## 更多文档

- [配置说明](docs/configuration.md)
- [API 兼容系统](docs/API%20Compatibility%20System/API%20Compatibility%20System.md)
- [Prompt 兼容流程](docs/prompt-compatibility.md)
- [工具调用语义](docs/toolcall-semantics.md)
