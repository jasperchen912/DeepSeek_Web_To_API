package shared

import (
	"net/http"
	"strings"

	"DeepSeek_Web_To_API/internal/auth"
	"DeepSeek_Web_To_API/internal/promptcache"
	"DeepSeek_Web_To_API/internal/promptcompat"
)

const PromptCacheKeyHeader = "X-DeepSeek-Web-To-API-Prompt-Cache-Key"

type PromptCacheObserver interface {
	Observe(promptcache.Observation) promptcache.Result
}

func ObservePromptCache(cache PromptCacheObserver, r *http.Request, a *auth.RequestAuth, stdReq promptcompat.StandardRequest, surface string) promptcompat.StandardRequest {
	stdReq.PromptCacheHint = promptCacheHint(r, stdReq.PromptCacheHint)
	if cache == nil {
		return stdReq
	}
	result := cache.Observe(promptcache.Observation{
		Auth:         a,
		Surface:      surface,
		Model:        promptCacheModel(stdReq),
		Thinking:     stdReq.Thinking,
		Search:       stdReq.Search,
		PrefixHash:   stdReq.PromptPrefixHash,
		PrefixTokens: stdReq.PromptPrefixTokens,
		TailTokens:   stdReq.PromptTailTokens,
		Eligible:     stdReq.PromptPrefixEligible,
		Hint:         stdReq.PromptCacheHint,
	})
	stdReq.PromptPrefixHash = result.PrefixHash
	stdReq.PromptPrefixTokens = result.PrefixTokens
	stdReq.PromptTailTokens = result.TailTokens
	stdReq.PromptPrefixEligible = result.Eligible
	stdReq.PromptPrefixReused = result.Reused
	return stdReq
}

func promptCacheHint(r *http.Request, bodyHint string) string {
	if r != nil {
		if value := sanitizePromptCacheHint(r.Header.Get(PromptCacheKeyHeader)); value != "" {
			return value
		}
	}
	return sanitizePromptCacheHint(bodyHint)
}

func sanitizePromptCacheHint(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 256 {
		value = value[:256]
	}
	return value
}

func promptCacheModel(stdReq promptcompat.StandardRequest) string {
	model := strings.TrimSpace(stdReq.ResolvedModel)
	if model != "" {
		return model
	}
	model = strings.TrimSpace(stdReq.RequestedModel)
	if model != "" {
		return model
	}
	return strings.TrimSpace(stdReq.ResponseModel)
}
