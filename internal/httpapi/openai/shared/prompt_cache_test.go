package shared

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"DeepSeek_Web_To_API/internal/auth"
	"DeepSeek_Web_To_API/internal/promptcache"
	"DeepSeek_Web_To_API/internal/promptcompat"
)

func TestObservePromptCacheUsesHeaderHintAndMarksReuse(t *testing.T) {
	t.Parallel()

	cache := promptcache.New(promptcache.Options{TTL: time.Hour, MaxEntries: 10})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set(PromptCacheKeyHeader, "header-hint")
	a := &auth.RequestAuth{CallerID: "caller", AccountID: "acct-a"}
	stdReq := promptcompat.StandardRequest{
		ResolvedModel:        "deepseek-v4-flash",
		PromptCacheHint:      "body-hint",
		PromptPrefixHash:     "hash-a",
		PromptPrefixTokens:   100,
		PromptTailTokens:     10,
		PromptPrefixEligible: true,
	}

	first := ObservePromptCache(cache, req, a, stdReq, "chat.completions")
	second := ObservePromptCache(cache, req, a, stdReq, "chat.completions")
	if first.PromptPrefixReused {
		t.Fatal("first observation should not be reused")
	}
	if !second.PromptPrefixReused {
		t.Fatal("second observation should be reused")
	}
	if second.PromptCacheHint != "header-hint" {
		t.Fatalf("expected header hint to win, got %q", second.PromptCacheHint)
	}
}
