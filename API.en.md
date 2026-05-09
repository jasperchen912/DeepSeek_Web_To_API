# DeepSeek_Web_To_API API Reference

Language: [中文](API.md) | [English](API.en.md)

Default Base URL: `http://127.0.0.1:5001`.

## Common Rules

- JSON endpoints require valid UTF-8 request bodies.
- Health probes: `GET/HEAD /healthz`, `GET/HEAD /readyz`.
- Protocol auth: `Authorization: Bearer <token>` or `x-api-key: <token>`; Gemini also accepts `x-goog-api-key`, `?key=`, and `?api_key=`.
- Managed mode: tokens configured in `config.json` `keys` use the account pool.
- Direct-token mode: unknown tokens are passed through as DeepSeek tokens.
- Admin auth: `POST /admin/login` returns a JWT; protected admin APIs require `Authorization: Bearer <jwt>` or the admin key.

## OpenAI-Compatible Routes

| Method | Path | Description |
| --- | --- | --- |
| `GET` | `/v1/models` | List models |
| `GET` | `/v1/models/{model_id}` | Get one model |
| `POST` | `/v1/chat/completions` | Chat Completions |
| `POST` | `/v1/responses` | Responses |
| `GET` | `/v1/responses/{response_id}` | Stored Response lookup |
| `POST` | `/v1/files` | File upload |
| `POST` | `/v1/embeddings` | Embeddings-compatible response |

Root aliases and `/v1/v1/*` aliases are also supported.

DeepSeek model objects returned by `/v1/models` and `/v1/models/{model_id}` include the base OpenAI fields plus `input`, `output`, and zero-valued `cost` metadata for gateways that inspect model capabilities or pricing. Chat Completions `usage` keeps `prompt_tokens`, `completion_tokens`, and `total_tokens`, and also includes `prompt_tokens_details`, `completion_tokens_details`, `input`, `output`, `cacheRead`, `cacheWrite`, `totalTokens`, and zero-valued `cost`.

## Claude-Compatible Routes

| Method | Path | Description |
| --- | --- | --- |
| `GET` | `/anthropic/v1/models` | Claude-style model list |
| `POST` | `/anthropic/v1/messages` | Messages |
| `POST` | `/anthropic/v1/messages/count_tokens` | Token estimate |
| `POST` | `/v1/messages`, `/messages` | Messages aliases |

## Gemini-Compatible Routes

| Method | Path | Description |
| --- | --- | --- |
| `POST` | `/v1beta/models/{model}:generateContent` | Non-streaming generation |
| `POST` | `/v1beta/models/{model}:streamGenerateContent` | Streaming generation |
| `POST` | `/v1/models/{model}:generateContent` | v1 alias |
| `POST` | `/v1/models/{model}:streamGenerateContent` | v1 streaming alias |

## Admin Routes

Admin routes include login, config import/export, API keys, account pool, proxies, settings, chat history, overview metrics, and version inspection. See [docs/README.md](docs/README.md) for the Chinese operational documentation.

## Response Cache

Cacheable protocol responses may include:

- `X-DeepSeek-Web-To-API-Cache: memory|disk|singleflight`
- `X-DeepSeek-Web-To-API-Cache-Expires-At: <RFC3339>`

The response cache covers OpenAI Chat/Responses/Embeddings, Claude Messages/CountTokens, and Gemini GenerateContent/StreamGenerateContent routes. Cache keys are isolated by caller, canonical protocol path, query string, normalized output-affecting request headers, and normalized JSON request body. Concurrent misses for the same key are coalesced into one upstream request; waiters replay the complete result and are marked as `singleflight`.

Streaming requests and streaming responses are not written to the response cache. Other non-cacheable cases include non-2xx responses, oversized request or response bodies, explicit bypass, missing caller ownership, `Set-Cookie`, and `Cache-Control: no-store`.

OpenAI Chat Completions and Responses also reuse DeepSeek `chat_session_id` values on upstream cache misses. The session cache is isolated by caller, stable SessionKey, account or direct-token hash, model/model_type, thinking/search, and API surface. It is enabled by default and can be disabled with `cache.session.enabled=false`; it does not change prompt assembly or turn normal requests into `parent_message_id` chained follow-ups. If a newly created or cached remote session immediately returns not found/expired, the key is invalidated and retried once with a fresh session.

OpenAI Chat/Responses also record prompt-prefix cache diagnostics: stable prefix hash, estimated prefix/tail tokens, and whether the prefix was seen before in the local tracker. Request body `prompt_cache_key` or header `X-DeepSeek-Web-To-API-Prompt-Cache-Key` can be used as a diagnostic hint. The hint is not forwarded to DeepSeek, is not part of the full response-cache key, and does not enable old-answer reuse. `usage.prompt_tokens_details.cached_tokens` keeps its upstream-real meaning and is not filled from local estimates.

Bypass with:

```http
Cache-Control: no-cache
X-DeepSeek-Web-To-API-Cache-Control: bypass
```
