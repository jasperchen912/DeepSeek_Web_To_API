package claude

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"DeepSeek_Web_To_API/internal/auth"
	"DeepSeek_Web_To_API/internal/chathistory"
	"DeepSeek_Web_To_API/internal/promptcache"
)

type directClaudeAuthStub struct {
	seenAffinityHeader string
	seenBody           []byte
}

func (s *directClaudeAuthStub) Determine(req *http.Request) (*auth.RequestAuth, error) {
	if req != nil {
		s.seenAffinityHeader = req.Header.Get(auth.SessionAffinityHeader)
	}
	return &auth.RequestAuth{UseConfigToken: false, DeepSeekToken: "direct-token", CallerID: "caller:test", AccountID: "acc-direct", TriedAccounts: map[string]bool{}}, nil
}

func (s *directClaudeAuthStub) DetermineWithSession(req *http.Request, body []byte) (*auth.RequestAuth, error) {
	if req != nil {
		s.seenAffinityHeader = req.Header.Get(auth.SessionAffinityHeader)
	}
	s.seenBody = append([]byte(nil), body...)
	return &auth.RequestAuth{UseConfigToken: false, DeepSeekToken: "direct-token", CallerID: "caller:test", AccountID: "acc-direct", TriedAccounts: map[string]bool{}}, nil
}

func (s *directClaudeAuthStub) DetermineCaller(req *http.Request) (*auth.RequestAuth, error) {
	if req != nil {
		s.seenAffinityHeader = req.Header.Get(auth.SessionAffinityHeader)
	}
	return &auth.RequestAuth{UseConfigToken: false, CallerID: "caller:test", TriedAccounts: map[string]bool{}}, nil
}

func (*directClaudeAuthStub) Release(_ *auth.RequestAuth) {}

type directClaudeDSStub struct {
	resp        *http.Response
	seenPayload map[string]any
}

func (s *directClaudeDSStub) CreateSession(_ context.Context, _ *auth.RequestAuth, _ int) (string, error) {
	return "session-direct", nil
}

func (s *directClaudeDSStub) GetPow(_ context.Context, _ *auth.RequestAuth, _ int) (string, error) {
	return "pow", nil
}

func (s *directClaudeDSStub) CallCompletion(_ context.Context, _ *auth.RequestAuth, payload map[string]any, _ string, _ int) (*http.Response, error) {
	s.seenPayload = payload
	return s.resp, nil
}

func historyUsageNumberEqual(value any, want float64) bool {
	switch n := value.(type) {
	case int:
		return float64(n) == want
	case int64:
		return float64(n) == want
	case float64:
		return n == want
	}
	return false
}

func TestClaudeMessagesUsesNativeDirectStream(t *testing.T) {
	authStub := &directClaudeAuthStub{}
	dsStub := &directClaudeDSStub{
		resp: makeClaudeSSEHTTPResponse(
			`data: {"p":"response/content","v":"Hel"}`,
			`data: {"p":"response/content","v":"lo"}`,
			`data: [DONE]`,
		),
	}
	h := &Handler{
		Store: streamStatusClaudeStoreStub{},
		Auth:  authStub,
		DS:    dsStub,
	}

	reqBody := `{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hi"}],"stream":true}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages?beta=true", strings.NewReader(reqBody))
	req.Header.Set("Authorization", "Bearer local-key")
	rec := httptest.NewRecorder()
	h.Messages(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "chat.completion.chunk") || strings.Contains(body, "data: [DONE]") {
		t.Fatalf("expected native Claude SSE, got OpenAI-shaped stream: %s", body)
	}
	for _, event := range []string{"message_start", "content_block_delta", "message_delta", "message_stop"} {
		if !strings.Contains(body, "event: "+event) {
			t.Fatalf("missing Claude event %s, body=%s", event, body)
		}
	}

	frames := parseClaudeFrames(t, body)
	var combined strings.Builder
	for _, frame := range findClaudeFrames(frames, "content_block_delta") {
		delta, _ := frame.Payload["delta"].(map[string]any)
		if delta["type"] == "text_delta" {
			combined.WriteString(asString(delta["text"]))
		}
	}
	if combined.String() != "Hello" {
		t.Fatalf("unexpected text delta content: %q body=%s", combined.String(), body)
	}
	if !strings.HasPrefix(authStub.seenAffinityHeader, "claude:") {
		t.Fatalf("expected Claude affinity scope, got %q", authStub.seenAffinityHeader)
	}
	if string(authStub.seenBody) != reqBody {
		t.Fatalf("expected affinity body to use original Claude request")
	}
	if got := dsStub.seenPayload["chat_session_id"]; got != "session-direct" {
		t.Fatalf("expected DeepSeek session id in payload, got %#v", dsStub.seenPayload)
	}
	if got := dsStub.seenPayload["thinking_enabled"]; got != false {
		t.Fatalf("expected default Claude stream thinking disabled, got %#v payload=%#v", got, dsStub.seenPayload)
	}
}

func TestClaudeMessagesObservesPromptCacheControlHint(t *testing.T) {
	cache := promptcache.New(promptcache.Options{})
	dsStub := &directClaudeDSStub{}
	h := &Handler{
		Store:       streamStatusClaudeStoreStub{},
		Auth:        &directClaudeAuthStub{},
		DS:          dsStub,
		PromptCache: cache,
	}
	reqBody := `{"model":"claude-sonnet-4-5","cache_control":{"type":"ephemeral","ttl":"1h"},"system":"stable","messages":[{"role":"user","content":"remember this"},{"role":"assistant","content":"ok"},{"role":"user","content":"answer now"}]}`

	for i := 0; i < 2; i++ {
		dsStub.resp = makeClaudeSSEHTTPResponse(
			`data: {"p":"response/content","v":"ok"}`,
			`data: [DONE]`,
		)
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(reqBody))
		req.Header.Set("Authorization", "Bearer local-key")
		rec := httptest.NewRecorder()
		h.Messages(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d expected 200, got %d body=%s", i+1, rec.Code, rec.Body.String())
		}
	}

	stats := cache.Stats()
	if stats.Observed != 2 || stats.Eligible != 2 || stats.Reused != 1 {
		t.Fatalf("unexpected prompt cache stats: %#v", stats)
	}
	if !stats.LastHintPresent {
		t.Fatalf("expected Claude cache_control hint to be present: %#v", stats)
	}
}

func TestClaudeMessagesDirectNonStreamStripsDefaultThinking(t *testing.T) {
	dsStub := &directClaudeDSStub{
		resp: makeClaudeSSEHTTPResponse(
			`data: {"p":"response","o":"BATCH","v":[{"p":"token_usage","v":{"prompt_cache_hit_tokens":64,"prompt_cache_miss_tokens":32}}]}`,
			`data: {"p":"response/thinking_content","v":"hidden"}`,
			`data: {"p":"response/content","v":"visible"}`,
			`data: [DONE]`,
		),
	}
	h := &Handler{
		Store: streamStatusClaudeStoreStub{},
		Auth:  &directClaudeAuthStub{},
		DS:    dsStub,
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer local-key")
	rec := httptest.NewRecorder()
	h.Messages(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := dsStub.seenPayload["thinking_enabled"]; got != true {
		t.Fatalf("expected non-stream Claude to keep internal thinking enabled, got %#v payload=%#v", got, dsStub.seenPayload)
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response failed: %v", err)
	}
	content, _ := out["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("expected only visible text block, got %#v", content)
	}
	block, _ := content[0].(map[string]any)
	if block["type"] != "text" || block["text"] != "visible" {
		t.Fatalf("expected visible text block only, got %#v", block)
	}
}

func TestClaudeMessagesDirectNonStreamPreservesDeepSeekPromptCacheUsage(t *testing.T) {
	dsStub := &directClaudeDSStub{
		resp: makeClaudeSSEHTTPResponse(
			`data: {"p":"response/content","v":"visible"}`,
			`data: {"p":"response","o":"BATCH","v":[{"p":"token_usage","v":{"prompt_cache_hit_tokens":64,"prompt_cache_miss_tokens":32}}]}`,
			`data: [DONE]`,
		),
	}
	h := &Handler{
		Store: streamStatusClaudeStoreStub{},
		Auth:  &directClaudeAuthStub{},
		DS:    dsStub,
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer local-key")
	rec := httptest.NewRecorder()
	h.Messages(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response failed: %v", err)
	}
	usage, _ := out["usage"].(map[string]any)
	if usage["cache_read_input_tokens"] != float64(64) || usage["prompt_cache_hit_tokens"] != float64(64) || usage["prompt_cache_miss_tokens"] != float64(32) {
		t.Fatalf("unexpected prompt cache usage: %#v", usage)
	}
	if _, ok := usage["cache_creation_input_tokens"]; ok {
		t.Fatalf("DeepSeek miss tokens must not be reported as Claude cache creation tokens: %#v", usage)
	}
}

func TestClaudeMessagesDirectNonStreamCapturesPromptCacheUsageInHistory(t *testing.T) {
	historyStore := chathistory.New(filepath.Join(t.TempDir(), "chat_history.json"))
	dsStub := &directClaudeDSStub{
		resp: makeClaudeSSEHTTPResponse(
			`data: {"p":"response/content","v":"visible"}`,
			`data: {"p":"response","o":"BATCH","v":[{"p":"token_usage","v":{"prompt_cache_hit_tokens":64,"prompt_cache_miss_tokens":32}}]}`,
			`data: [DONE]`,
		),
	}
	h := &Handler{
		Store:       streamStatusClaudeStoreStub{},
		Auth:        &directClaudeAuthStub{},
		DS:          dsStub,
		ChatHistory: historyStore,
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"history usage"}]}`))
	req.Header.Set("Authorization", "Bearer local-key")
	rec := httptest.NewRecorder()
	h.Messages(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	snapshot, err := historyStore.Snapshot()
	if err != nil {
		t.Fatalf("snapshot failed: %v", err)
	}
	item, err := historyStore.Get(snapshot.Items[0].ID)
	if err != nil {
		t.Fatalf("get history item failed: %v", err)
	}
	if !historyUsageNumberEqual(item.Usage["cache_read_input_tokens"], 64) || !historyUsageNumberEqual(item.Usage["prompt_cache_hit_tokens"], 64) || !historyUsageNumberEqual(item.Usage["prompt_cache_miss_tokens"], 32) {
		t.Fatalf("expected Claude prompt cache usage in history, got %#v", item.Usage)
	}
}

func TestClaudeMessagesDirectPropagatesUpstreamStatus(t *testing.T) {
	h := &Handler{
		Store: streamStatusClaudeStoreStub{},
		Auth:  &directClaudeAuthStub{},
		DS: &directClaudeDSStub{
			resp: &http.Response{
				StatusCode: http.StatusTooManyRequests,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("busy")),
			},
		},
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer local-key")
	rec := httptest.NewRecorder()
	h.Messages(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected upstream status 429, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestClaudeMessagesDirectStreamPropagatesUpstreamStatus(t *testing.T) {
	h := &Handler{
		Store: streamStatusClaudeStoreStub{},
		Auth:  &directClaudeAuthStub{},
		DS: &directClaudeDSStub{
			resp: &http.Response{
				StatusCode: http.StatusTooManyRequests,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("busy")),
			},
		},
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hi"}],"stream":true}`))
	req.Header.Set("Authorization", "Bearer local-key")
	rec := httptest.NewRecorder()
	h.Messages(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected upstream stream status 429, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "rate_limit_exceeded") {
		t.Fatalf("expected Claude rate limit error, body=%s", rec.Body.String())
	}
}

func TestClaudeMessagesDirectNonStreamCapturesChatHistory(t *testing.T) {
	historyStore := chathistory.New(filepath.Join(t.TempDir(), "chat_history.json"))
	dsStub := &directClaudeDSStub{
		resp: makeClaudeSSEHTTPResponse(
			`data: {"p":"response/thinking_content","v":"hidden"}`,
			`data: {"p":"response/content","v":"visible"}`,
			`data: [DONE]`,
		),
	}
	h := &Handler{
		Store:       streamStatusClaudeStoreStub{},
		Auth:        &directClaudeAuthStub{},
		DS:          dsStub,
		ChatHistory: historyStore,
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"history please"}]}`))
	req.Header.Set("Authorization", "Bearer local-key")
	rec := httptest.NewRecorder()
	h.Messages(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	snapshot, err := historyStore.Snapshot()
	if err != nil {
		t.Fatalf("snapshot failed: %v", err)
	}
	if len(snapshot.Items) != 1 {
		t.Fatalf("expected one history item, got %d", len(snapshot.Items))
	}
	item, err := historyStore.Get(snapshot.Items[0].ID)
	if err != nil {
		t.Fatalf("get history item failed: %v", err)
	}
	if item.Status != "success" || item.Content != "visible" || item.ReasoningContent != "hidden" {
		t.Fatalf("unexpected captured item: %#v", item)
	}
	if item.UserInput != "history please" || item.AccountID != "acc-direct" || item.Model != "claude-sonnet-4-5" {
		t.Fatalf("unexpected captured metadata: %#v", item)
	}
}

func TestClaudeMessagesDirectStreamCapturesChatHistory(t *testing.T) {
	historyStore := chathistory.New(filepath.Join(t.TempDir(), "chat_history.json"))
	dsStub := &directClaudeDSStub{
		resp: makeClaudeSSEHTTPResponse(
			`data: {"p":"response/content","v":"Hel"}`,
			`data: {"p":"response/content","v":"lo"}`,
			`data: {"p":"response","o":"BATCH","v":[{"p":"token_usage","v":{"prompt_cache_hit_tokens":128,"prompt_cache_miss_tokens":16}}]}`,
			`data: [DONE]`,
		),
	}
	h := &Handler{
		Store:       streamStatusClaudeStoreStub{},
		Auth:        &directClaudeAuthStub{},
		DS:          dsStub,
		ChatHistory: historyStore,
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"stream history"}],"stream":true}`))
	req.Header.Set("Authorization", "Bearer local-key")
	rec := httptest.NewRecorder()
	h.Messages(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	snapshot, err := historyStore.Snapshot()
	if err != nil {
		t.Fatalf("snapshot failed: %v", err)
	}
	if len(snapshot.Items) != 1 {
		t.Fatalf("expected one history item, got %d", len(snapshot.Items))
	}
	item, err := historyStore.Get(snapshot.Items[0].ID)
	if err != nil {
		t.Fatalf("get history item failed: %v", err)
	}
	if item.Status != "success" || item.Content != "Hello" || !item.Stream {
		t.Fatalf("unexpected captured stream item: %#v", item)
	}
	if !historyUsageNumberEqual(item.Usage["cache_read_input_tokens"], 128) || !historyUsageNumberEqual(item.Usage["prompt_cache_hit_tokens"], 128) || !historyUsageNumberEqual(item.Usage["prompt_cache_miss_tokens"], 16) {
		t.Fatalf("expected Claude stream prompt cache usage in history, got %#v", item.Usage)
	}
}
