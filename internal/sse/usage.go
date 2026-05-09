package sse

import (
	"encoding/json"
	"strconv"
	"strings"
)

type PromptCacheUsage struct {
	HitTokens  int
	MissTokens int
	HasHit     bool
	HasMiss    bool
}

func (u *PromptCacheUsage) Merge(next PromptCacheUsage) {
	if u == nil {
		return
	}
	if next.HasHit {
		u.HitTokens = next.HitTokens
		u.HasHit = true
	}
	if next.HasMiss {
		u.MissTokens = next.MissTokens
		u.HasMiss = true
	}
}

func ExtractPromptCacheUsage(chunk map[string]any) PromptCacheUsage {
	var out PromptCacheUsage
	collectPromptCacheUsage(chunk, &out)
	return out
}

func collectPromptCacheUsage(value any, out *PromptCacheUsage) {
	if out == nil || value == nil {
		return
	}
	switch v := value.(type) {
	case map[string]any:
		out.Merge(promptCacheUsageFromMap(v))
		if p, _ := v["p"].(string); isUsagePath(p) {
			collectPromptCacheUsage(v["v"], out)
		}
		for _, item := range v {
			collectPromptCacheUsage(item, out)
		}
	case []any:
		for _, item := range v {
			collectPromptCacheUsage(item, out)
		}
	}
}

func promptCacheUsageFromMap(m map[string]any) PromptCacheUsage {
	var out PromptCacheUsage
	if n, ok := usageInt(m["prompt_cache_hit_tokens"]); ok {
		out.HitTokens = n
		out.HasHit = true
	}
	if n, ok := usageInt(m["prompt_cache_miss_tokens"]); ok {
		out.MissTokens = n
		out.HasMiss = true
	}
	return out
}

func isUsagePath(path string) bool {
	path = strings.ToLower(strings.TrimSpace(path))
	return path == "usage" || path == "token_usage" || strings.HasSuffix(path, "/usage") || strings.HasSuffix(path, "/token_usage")
}

func usageInt(value any) (int, bool) {
	switch n := value.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return int(i), true
		}
		if f, err := strconv.ParseFloat(n.String(), 64); err == nil {
			return int(f), true
		}
	case string:
		if i, err := strconv.Atoi(strings.TrimSpace(n)); err == nil {
			return i, true
		}
	}
	return 0, false
}
