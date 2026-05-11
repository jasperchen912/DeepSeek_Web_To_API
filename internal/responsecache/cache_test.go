package responsecache

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"DeepSeek_Web_To_API/internal/auth"
)

type stubResolver struct {
	caller string
	err    error
}

func (s stubResolver) DetermineCaller(_ *http.Request) (*auth.RequestAuth, error) {
	if s.err != nil {
		return nil, s.err
	}
	return &auth.RequestAuth{CallerID: s.caller}, nil
}

func TestMiddlewareCachesProtocolResponseInMemory(t *testing.T) {
	t.Parallel()

	var hits int32
	cache := New(Options{
		Dir:       t.TempDir(),
		MemoryTTL: time.Minute,
		DiskTTL:   time.Hour,
		OnHit: func(_ *http.Request, entry Entry, source string) {
			if source == "memory" && string(entry.Body) == `{"ok":true}` {
				atomic.AddInt32(&hits, 1)
			}
		},
	})
	var calls int32
	handler := cache.Wrap(stubResolver{caller: "caller-a"}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))

	req1 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m","messages":[]}`))
	req1.Header.Set("Authorization", "Bearer key-a")
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("first status=%d body=%s", rec1.Code, rec1.Body.String())
	}

	req2 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m","messages":[]}`))
	req2.Header.Set("Authorization", "Bearer key-a")
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("second status=%d body=%s", rec2.Code, rec2.Body.String())
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected handler once, got %d", got)
	}
	if got := rec2.Header().Get("X-DeepSeek-Web-To-API-Cache"); got != "memory" {
		t.Fatalf("expected memory cache hit, got %q", got)
	}
	if got := rec2.Body.String(); got != `{"ok":true}` {
		t.Fatalf("unexpected cached body: %s", got)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("expected one hit callback, got %d", got)
	}
	stats := cache.Stats()
	if got := stats["lookups"]; got != int64(2) {
		t.Fatalf("expected two cache lookups, got %v", got)
	}
	if got := stats["hits"]; got != int64(1) {
		t.Fatalf("expected one cache hit, got %v", got)
	}
	if got := stats["misses"]; got != int64(1) {
		t.Fatalf("expected one cache miss, got %v", got)
	}
	if got := stats["stores"]; got != int64(1) {
		t.Fatalf("expected one cache store, got %v", got)
	}
	if got := stats["cacheable_lookups"]; got != int64(2) {
		t.Fatalf("expected two cacheable lookups, got %v", got)
	}
	if got := stats["cacheable_misses"]; got != int64(1) {
		t.Fatalf("expected one cacheable miss, got %v", got)
	}
	if got := stats["memory_hits"]; got != int64(1) {
		t.Fatalf("expected one memory hit, got %v", got)
	}
}

func TestMiddlewareCoalescesConcurrentMissesForSameKey(t *testing.T) {
	t.Parallel()

	cache := New(Options{Dir: t.TempDir(), MemoryTTL: time.Minute, DiskTTL: time.Hour})
	var calls int32
	ready := make(chan struct{})
	release := make(chan struct{})
	handler := cache.Wrap(stubResolver{caller: "caller-a"}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		call := atomic.AddInt32(&calls, 1)
		if call == 1 {
			close(ready)
		}
		<-release
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))

	const total = 8
	var wg sync.WaitGroup
	statuses := make(chan int, total)
	cacheHeaders := make(chan string, total)
	for i := 0; i < total; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m","messages":[]}`))
			req.Header.Set("Authorization", "Bearer key-a")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			statuses <- rec.Code
			cacheHeaders <- rec.Header().Get("X-DeepSeek-Web-To-API-Cache")
		}()
	}
	<-ready
	time.Sleep(10 * time.Millisecond)
	close(release)
	wg.Wait()
	close(statuses)
	close(cacheHeaders)

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected one upstream handler call, got %d", got)
	}
	for status := range statuses {
		if status != http.StatusOK {
			t.Fatalf("unexpected status=%d", status)
		}
	}
	var coalesced int
	for header := range cacheHeaders {
		if header == "singleflight" || header == "memory" {
			coalesced++
		}
	}
	if coalesced == 0 {
		t.Fatal("expected at least one coalesced or memory cache replay")
	}
	stats := cache.Stats()
	if got := stats["stores"]; got != int64(1) {
		t.Fatalf("expected one store, got %v", got)
	}
	if got := stats["inflight_waits"]; got == int64(0) {
		t.Fatalf("expected inflight waits, got %v", got)
	}
}

func TestRequestKeyNormalizesEquivalentHeaders(t *testing.T) {
	t.Parallel()

	body := []byte(`{"model":"m","messages":[]}`)
	reqA := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	reqA.Header.Set("Content-Type", "application/json; charset=utf-8")
	reqA.Header.Set("Accept", "application/json, text/event-stream")
	reqB := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	reqB.Header.Set("Content-Type", "application/json")
	reqB.Header.Set("Accept", " text/event-stream , application/json ")

	if RequestKey(reqA, "caller-a", body) != RequestKey(reqB, "caller-a", body) {
		t.Fatal("expected equivalent content negotiation headers to share cache key")
	}
}

func TestCacheFallsBackToCompressedDiskAfterMemoryExpiry(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cache := New(Options{Dir: dir, MemoryTTL: time.Millisecond, DiskTTL: time.Hour})
	key := strings.Repeat("a", 64)
	cache.Set(key, Entry{
		Status: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"application/json"},
		},
		Body: []byte(`{"disk":true}`),
	})
	path, ok := cache.diskPath(key)
	if !ok {
		t.Fatal("expected cache disk path")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read cache file: %v", err)
	}
	if len(raw) < 2 || raw[0] != 0x1f || raw[1] != 0x8b {
		t.Fatalf("expected gzip-compressed cache file, got prefix %x", raw[:min(len(raw), 2)])
	}

	time.Sleep(5 * time.Millisecond)
	entry, source, ok := cache.Get(key)
	if !ok {
		t.Fatal("expected disk cache hit")
	}
	if source != "disk" {
		t.Fatalf("expected disk source, got %q", source)
	}
	if got := string(entry.Body); got != `{"disk":true}` {
		t.Fatalf("unexpected body: %s", got)
	}
}

func TestRequestBypassSkipsCache(t *testing.T) {
	t.Parallel()

	cache := New(Options{Dir: t.TempDir(), MemoryTTL: time.Minute, DiskTTL: time.Hour})
	var calls int32
	handler := cache.Wrap(stubResolver{caller: "caller-a"}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		call := atomic.AddInt32(&calls, 1)
		_, _ = fmt.Fprintf(w, `{"call":%d}`, call)
	}))

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m"}`))
		req.Header.Set("Authorization", "Bearer key-a")
		req.Header.Set("Cache-Control", "no-cache")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Header().Get("X-DeepSeek-Web-To-API-Cache") != "" {
			t.Fatalf("unexpected cache hit on bypass request")
		}
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("expected bypass to call handler twice, got %d", got)
	}
	stats := cache.Stats()
	if got := stats["misses"]; got != int64(2) {
		t.Fatalf("expected bypass misses to be counted, got %v", got)
	}
	if got := stats["uncacheable_request_no_cache"]; got != int64(2) {
		t.Fatalf("expected request_no_cache reason to be counted, got %v", got)
	}
	if got := stats["uncacheable_misses"]; got != int64(2) {
		t.Fatalf("expected uncacheable misses to include bypasses, got %v", got)
	}
}

func TestStreamRequestSkipsCache(t *testing.T) {
	t.Parallel()

	cache := New(Options{Dir: t.TempDir(), MemoryTTL: time.Minute, DiskTTL: time.Hour})
	var calls int32
	handler := cache.Wrap(stubResolver{caller: "caller-a"}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		call := atomic.AddInt32(&calls, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: {\"call\":%d}\n\ndata: [DONE]\n\n", call)
	}))

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m","stream":true}`))
		req.Header.Set("Authorization", "Bearer key-a")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Header().Get("X-DeepSeek-Web-To-API-Cache") != "" {
			t.Fatalf("unexpected cache hit on stream request")
		}
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("expected stream requests to call handler twice, got %d", got)
	}
	stats := cache.Stats()
	if got := stats["misses"]; got != int64(2) {
		t.Fatalf("expected two stream misses, got %v", got)
	}
	if got := stats["stores"]; got != int64(0) {
		t.Fatalf("expected zero stream cache stores, got %v", got)
	}
	if got := stats["uncacheable_stream_request"]; got != int64(2) {
		t.Fatalf("expected two stream_request misses, got %v", got)
	}
}

func TestGeminiStreamEndpointSkipsCache(t *testing.T) {
	t.Parallel()

	cache := New(Options{Dir: t.TempDir(), MemoryTTL: time.Minute, DiskTTL: time.Hour})
	var calls int32
	handler := cache.Wrap(stubResolver{caller: "caller-a"}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {}\n\n"))
	}))

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/v1/models/gemini-2.5-pro:streamGenerateContent", strings.NewReader(`{"contents":[]}`))
		req.Header.Set("Authorization", "Bearer key-a")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Header().Get("X-DeepSeek-Web-To-API-Cache") != "" {
			t.Fatalf("unexpected cache hit on Gemini stream request")
		}
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("expected stream endpoint to call handler twice, got %d", got)
	}
	if got := cache.Stats()["uncacheable_stream_request"]; got != int64(2) {
		t.Fatalf("expected two stream_request misses, got %v", got)
	}
}

func TestOversizedBodySkipsCacheWithoutConsumingBody(t *testing.T) {
	t.Parallel()

	cache := New(Options{Dir: t.TempDir(), MemoryTTL: time.Minute, DiskTTL: time.Hour, MaxBody: 4})
	var calls int32
	handler := cache.Wrap(stubResolver{caller: "caller-a"}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := atomic.AddInt32(&calls, 1)
		body := make([]byte, 16)
		n, err := r.Body.Read(body)
		if err != nil && !errors.Is(err, io.EOF) {
			t.Fatalf("read body: %v", err)
		}
		_, _ = fmt.Fprintf(w, `{"call":%d,"body":%q}`, call, string(body[:n]))
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`ok`))
	req.Header.Set("Authorization", "Bearer key-a")
	req.ContentLength = 100
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Header().Get("X-DeepSeek-Web-To-API-Cache") != "" {
		t.Fatalf("unexpected cache hit on oversized request")
	}
	if !strings.Contains(rec.Body.String(), `"body":"ok"`) {
		t.Fatalf("handler did not receive original body: %s", rec.Body.String())
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected oversized body to call handler once, got %d", got)
	}
	stats := cache.Stats()
	if got := stats["misses"]; got != int64(1) {
		t.Fatalf("expected one oversized miss, got %v", got)
	}
	if got := stats["uncacheable_oversized_request"]; got != int64(1) {
		t.Fatalf("expected one oversized_request reason, got %v", got)
	}
}

func TestUnknownLengthBodyCanBeCached(t *testing.T) {
	t.Parallel()

	cache := New(Options{Dir: t.TempDir(), MemoryTTL: time.Minute, DiskTTL: time.Hour, MaxBody: 1024})
	var calls int32
	handler := cache.Wrap(stubResolver{caller: "caller-a"}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		_, _ = fmt.Fprintf(w, `{"body":%q}`, string(body))
	}))

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m"}`))
		req.Header.Set("Authorization", "Bearer key-a")
		req.ContentLength = -1
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		if i == 1 && rec.Header().Get("X-DeepSeek-Web-To-API-Cache") != "memory" {
			t.Fatalf("expected second unknown-length request to hit memory cache, got %q", rec.Header().Get("X-DeepSeek-Web-To-API-Cache"))
		}
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected unknown-length body to cache after first call, got %d handler calls", got)
	}
}

func TestUnknownLengthOversizedBodyBypassesCacheWithoutDroppingBody(t *testing.T) {
	t.Parallel()

	cache := New(Options{Dir: t.TempDir(), MemoryTTL: time.Minute, DiskTTL: time.Hour, MaxBody: 4})
	var calls int32
	handler := cache.Wrap(stubResolver{caller: "caller-a"}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := atomic.AddInt32(&calls, 1)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		_, _ = fmt.Fprintf(w, `{"call":%d,"body":%q}`, call, string(body))
	}))

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`abcdef`))
		req.Header.Set("Authorization", "Bearer key-a")
		req.ContentLength = -1
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Header().Get("X-DeepSeek-Web-To-API-Cache") != "" {
			t.Fatalf("unexpected cache hit on oversized unknown-length request")
		}
		if !strings.Contains(rec.Body.String(), `"body":"abcdef"`) {
			t.Fatalf("handler did not receive original body: %s", rec.Body.String())
		}
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("expected oversized unknown-length body to call handler twice, got %d", got)
	}
}

func TestCacheableRequestCoversSupportedProtocols(t *testing.T) {
	t.Parallel()

	paths := []string{
		"/v1/chat/completions",
		"/v1/v1/chat/completions",
		"/chat/completions",
		"/v1/responses",
		"/v1/v1/responses",
		"/responses",
		"/v1/embeddings",
		"/v1/v1/embeddings",
		"/embeddings",
		"/anthropic/v1/messages",
		"/v1/messages",
		"/v1/v1/messages",
		"/messages",
		"/anthropic/v1/messages/count_tokens",
		"/v1/v1/messages/count_tokens",
		"/messages/count_tokens",
		"/v1beta/models/gemini-2.5-pro:generateContent",
		"/v1/models/gemini-2.5-pro:streamGenerateContent",
	}
	for _, path := range paths {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
		if !CacheableRequest(req) {
			t.Fatalf("expected %s to be cacheable", path)
		}
	}

}

func TestRequestKeyVariesByCallerAndProtocolHeaders(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages", strings.NewReader(`{"model":"claude"}`))
	req.Header.Set("Anthropic-Version", "2023-06-01")
	body := []byte(`{"model":"claude"}`)

	base := RequestKey(req, "caller-a", body)
	if base == RequestKey(req, "caller-b", body) {
		t.Fatal("expected caller to affect cache key")
	}

	req.Header.Set("Anthropic-Version", "2024-01-01")
	if base == RequestKey(req, "caller-a", body) {
		t.Fatal("expected protocol version header to affect cache key")
	}
}

func TestRequestKeyCanonicalizesProtocolAliases(t *testing.T) {
	t.Parallel()

	body := []byte(`{"model":"m"}`)
	base := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(body)))
	root := httptest.NewRequest(http.MethodPost, "/chat/completions", strings.NewReader(string(body)))
	doubleV1 := httptest.NewRequest(http.MethodPost, "/v1/v1/chat/completions", strings.NewReader(string(body)))
	if RequestKey(base, "caller-a", body) != RequestKey(root, "caller-a", body) {
		t.Fatal("expected root OpenAI alias to share cache key")
	}
	if RequestKey(base, "caller-a", body) != RequestKey(doubleV1, "caller-a", body) {
		t.Fatal("expected double-v1 OpenAI alias to share cache key")
	}

	claude := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(string(body)))
	claudeRoot := httptest.NewRequest(http.MethodPost, "/messages", strings.NewReader(string(body)))
	claudeAnthropic := httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages", strings.NewReader(string(body)))
	if RequestKey(claude, "caller-a", body) != RequestKey(claudeRoot, "caller-a", body) {
		t.Fatal("expected Claude root alias to share cache key")
	}
	if RequestKey(claude, "caller-a", body) != RequestKey(claudeAnthropic, "caller-a", body) {
		t.Fatal("expected Anthropic-prefixed alias to share cache key")
	}
}

func TestRequestKeyCanonicalizesJSONBodyAndIgnoredMetadata(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Content-Type", "application/json")
	bodyA := []byte(`{"metadata":{"trace":"a"},"model":"m","messages":[{"content":"hello","role":"user"}],"user":"u1"}`)
	bodyB := []byte(`{
		"user":"u2",
		"messages":[{"role":"user","content":"hello"}],
		"model":"m",
		"metadata":{"trace":"b"}
	}`)

	if RequestKey(req, "caller-a", bodyA) != RequestKey(req, "caller-a", bodyB) {
		t.Fatal("expected equivalent JSON payloads to share cache key")
	}

	bodyC := []byte(`{"model":"m","messages":[{"role":"user","content":"different"}]}`)
	if RequestKey(req, "caller-a", bodyA) == RequestKey(req, "caller-a", bodyC) {
		t.Fatal("expected semantic prompt body to affect cache key")
	}
}

func TestMiddlewareCachesCanonicalJSONAcrossFieldOrder(t *testing.T) {
	t.Parallel()

	cache := New(Options{Dir: t.TempDir(), MemoryTTL: time.Minute, DiskTTL: time.Hour})
	var calls int32
	handler := cache.Wrap(stubResolver{caller: "caller-a"}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))

	reqA := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"metadata":{"trace":"a"},"messages":[{"content":"hello","role":"user"}],"model":"m"}`))
	reqA.Header.Set("Content-Type", "application/json; charset=utf-8")
	recA := httptest.NewRecorder()
	handler.ServeHTTP(recA, reqA)
	if recA.Code != http.StatusOK {
		t.Fatalf("first status=%d body=%s", recA.Code, recA.Body.String())
	}

	reqB := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"model":"m",
		"messages":[{"role":"user","content":"hello"}],
		"metadata":{"trace":"b"}
	}`))
	reqB.Header.Set("Content-Type", "application/json")
	recB := httptest.NewRecorder()
	handler.ServeHTTP(recB, reqB)
	if recB.Code != http.StatusOK {
		t.Fatalf("second status=%d body=%s", recB.Code, recB.Body.String())
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected canonical JSON request to reuse cache, handler calls=%d", got)
	}
	if got := recB.Header().Get("X-DeepSeek-Web-To-API-Cache"); got != "memory" {
		t.Fatalf("expected memory hit for canonical JSON request, got %q", got)
	}
}

func TestRequestKeyIgnoresPromptCacheKeyHint(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Content-Type", "application/json")
	bodyA := []byte(`{"model":"m","messages":[{"role":"user","content":"hello"}],"prompt_cache_key":"hint-a"}`)
	bodyB := []byte(`{"model":"m","messages":[{"role":"user","content":"hello"}],"prompt_cache_key":"hint-b"}`)

	if RequestKey(req, "caller-a", bodyA) != RequestKey(req, "caller-a", bodyB) {
		t.Fatal("expected prompt_cache_key hint to be ignored in response cache key")
	}
}

func TestRequestKeyPreservesSemanticJSONNull(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Content-Type", "application/json")
	bodyWithNull := []byte(`{"model":"m","messages":[{"role":"user","content":null}]}`)
	bodyWithoutContent := []byte(`{"model":"m","messages":[{"role":"user"}]}`)

	if RequestKey(req, "caller-a", bodyWithNull) == RequestKey(req, "caller-a", bodyWithoutContent) {
		t.Fatal("expected semantic JSON null to remain part of the cache key")
	}
}

func TestRequestKeyIgnoresClaudeTransportCacheFields(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages", nil)
	req.Header.Set("Content-Type", "application/json")
	bodyA := []byte(`{
		"model":"claude-sonnet-4-6",
		"betas":["claude-code"],
		"context_management":{"edits":[{"type":"clear_thinking_20251015"}]},
		"messages":[{
			"role":"user",
			"content":[
				{"type":"text","text":"hello","cache_control":{"type":"ephemeral"}},
				{"type":"cache_edits","edits":[{"type":"delete","cache_reference":"toolu_old"}]}
			]
		}]
	}`)
	bodyB := []byte(`{
		"messages":[{
			"content":[{"text":"hello","type":"text"}],
			"role":"user"
		}],
		"model":"claude-sonnet-4-6"
	}`)

	if RequestKey(req, "caller-a", bodyA) != RequestKey(req, "caller-a", bodyB) {
		t.Fatal("expected Claude transport cache fields to be ignored in cache key")
	}
}

func TestMiddlewareCountsUncacheableMisses(t *testing.T) {
	t.Parallel()

	cache := New(Options{Dir: t.TempDir(), MemoryTTL: time.Minute, DiskTTL: time.Hour})
	handler := cache.Wrap(stubResolver{caller: "caller-a"}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"rate limited"}`))
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m"}`))
	req.Header.Set("Authorization", "Bearer key-a")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	stats := cache.Stats()
	if got := stats["misses"]; got != int64(1) {
		t.Fatalf("expected one miss, got %v", got)
	}
	if got := stats["stores"]; got != int64(0) {
		t.Fatalf("expected no cache store, got %v", got)
	}
	if got := stats["cacheable_misses"]; got != int64(0) {
		t.Fatalf("expected no cacheable misses, got %v", got)
	}
	if got := stats["uncacheable_misses"]; got != int64(1) {
		t.Fatalf("expected one uncacheable miss, got %v", got)
	}
	if got := stats["uncacheable_status_non_2xx"]; got != int64(1) {
		t.Fatalf("expected status_non_2xx reason, got %v", got)
	}
}

func TestMiddlewareCountsMissingOwnerAsUncacheable(t *testing.T) {
	t.Parallel()

	cache := New(Options{Dir: t.TempDir(), MemoryTTL: time.Minute, DiskTTL: time.Hour})
	var calls int32
	handler := cache.Wrap(stubResolver{caller: ""}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m"}`))
	req.Header.Set("Authorization", "Bearer key-a")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected handler call, got %d", got)
	}
	stats := cache.Stats()
	if got := stats["misses"]; got != int64(1) {
		t.Fatalf("expected one miss, got %v", got)
	}
	if got := stats["uncacheable_missing_owner"]; got != int64(1) {
		t.Fatalf("expected missing_owner reason, got %v", got)
	}
}

func TestCacheSkipsUncacheableResponses(t *testing.T) {
	t.Parallel()

	cache := New(Options{Dir: t.TempDir(), MemoryTTL: time.Minute, DiskTTL: time.Hour})
	key := strings.Repeat("b", 64)
	cache.Set(key, Entry{
		Status: http.StatusOK,
		Header: http.Header{
			"Set-Cookie": []string{"sid=1"},
		},
		Body: []byte(`{"private":true}`),
	})
	if _, _, ok := cache.Get(key); ok {
		t.Fatal("expected Set-Cookie response to skip cache")
	}

	cache.Set(key, Entry{
		Status: http.StatusOK,
		Header: http.Header{
			"Cache-Control": []string{"no-store"},
		},
		Body: []byte(`{"private":true}`),
	})
	if _, _, ok := cache.Get(key); ok {
		t.Fatal("expected no-store response to skip cache")
	}
}

func TestSetDoesNotReportStoreWhenMemoryAndDiskWritesFail(t *testing.T) {
	t.Parallel()

	dirFile := filepath.Join(t.TempDir(), "response-cache")
	if err := os.WriteFile(dirFile, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("write dir sentinel: %v", err)
	}
	cache := New(Options{Dir: dirFile, MemoryTTL: time.Minute, DiskTTL: time.Hour, MemoryMaxBytes: 1})
	key := strings.Repeat("f", 64)
	if stored := cache.Set(key, Entry{Status: http.StatusOK, Body: []byte(`{"ok":true}`)}); stored {
		t.Fatal("expected Set to report failed store")
	}
	stats := cache.Stats()
	if got := stats["stores"]; got != int64(0) {
		t.Fatalf("expected zero stores, got %v", got)
	}
	if _, _, ok := cache.Get(key); ok {
		t.Fatal("expected failed store to be unavailable")
	}
}

func TestStatsReportsCompressionAndTTLs(t *testing.T) {
	t.Parallel()

	cache := New(Options{
		Dir:            t.TempDir(),
		MemoryTTL:      2 * time.Minute,
		DiskTTL:        3 * time.Hour,
		MemoryMaxBytes: 1234,
		DiskMaxBytes:   5678,
	})
	stats := cache.Stats()
	if got := stats["memory_ttl_seconds"]; got != 120 {
		t.Fatalf("memory_ttl_seconds=%v", got)
	}
	if got := stats["disk_ttl_seconds"]; got != 10800 {
		t.Fatalf("disk_ttl_seconds=%v", got)
	}
	if got := stats["memory_max_bytes"]; got != int64(1234) {
		t.Fatalf("memory_max_bytes=%v", got)
	}
	if got := stats["disk_max_bytes"]; got != int64(5678) {
		t.Fatalf("disk_max_bytes=%v", got)
	}
	if got := stats["compression"]; got != "gzip" {
		t.Fatalf("compression=%v", got)
	}
}

func TestMemoryLimitEvictsEntries(t *testing.T) {
	t.Parallel()

	cache := New(Options{Dir: t.TempDir(), MemoryTTL: time.Hour, DiskTTL: time.Hour, MemoryMaxBytes: 6})
	cache.Set(strings.Repeat("c", 64), Entry{Status: http.StatusOK, Body: []byte(`aaaa`)})
	cache.Set(strings.Repeat("d", 64), Entry{Status: http.StatusOK, Body: []byte(`bbbb`)})

	stats := cache.Stats()
	if got := stats["memory_bytes"].(int64); got > 6 {
		t.Fatalf("memory_bytes=%d exceeds limit", got)
	}
	if got := stats["memory_items"]; got != 1 {
		t.Fatalf("memory_items=%v, want 1", got)
	}
}

func TestMemoryLimitEvictsLeastRecentlyUsedEntry(t *testing.T) {
	t.Parallel()

	cache := New(Options{Dir: t.TempDir(), MemoryTTL: time.Hour, DiskTTL: time.Hour, MemoryMaxBytes: 8})
	keyA := strings.Repeat("a", 64)
	keyB := strings.Repeat("b", 64)
	keyC := strings.Repeat("c", 64)
	cache.Set(keyA, Entry{Status: http.StatusOK, Body: []byte(`aaaa`)})
	time.Sleep(time.Millisecond)
	cache.Set(keyB, Entry{Status: http.StatusOK, Body: []byte(`bbbb`)})
	time.Sleep(time.Millisecond)
	if _, source, ok := cache.Get(keyA); !ok || source != "memory" {
		t.Fatalf("expected keyA memory hit before eviction, ok=%v source=%q", ok, source)
	}
	time.Sleep(time.Millisecond)
	cache.Set(keyC, Entry{Status: http.StatusOK, Body: []byte(`cccc`)})

	cache.mu.Lock()
	_, hasA := cache.items[keyA]
	_, hasB := cache.items[keyB]
	_, hasC := cache.items[keyC]
	cache.mu.Unlock()
	if !hasA || hasB || !hasC {
		t.Fatalf("expected LRU memory set to keep A/C and evict B; hasA=%v hasB=%v hasC=%v", hasA, hasB, hasC)
	}
}

func TestDiskLimitPrunesCompressedFiles(t *testing.T) {
	t.Parallel()

	cache := New(Options{Dir: t.TempDir(), MemoryTTL: time.Hour, DiskTTL: time.Hour, MemoryMaxBytes: 1, DiskMaxBytes: 1})
	key := strings.Repeat("e", 64)
	if stored := cache.Set(key, Entry{Status: http.StatusOK, Body: []byte(`{"too":"large for tiny disk limit"}`)}); stored {
		t.Fatal("expected store to report false after disk limit prunes the entry")
	}

	if _, _, ok := cache.Get(key); ok {
		t.Fatal("expected disk limit to prune cache entry")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
