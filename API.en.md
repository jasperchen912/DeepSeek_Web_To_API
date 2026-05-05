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

- `X-DeepSeek-Web-To-API-Cache: memory|disk`
- `X-DeepSeek-Web-To-API-Cache-Expires-At: <RFC3339>`

Bypass with:

```http
Cache-Control: no-cache
X-DeepSeek-Web-To-API-Cache-Control: bypass
```
