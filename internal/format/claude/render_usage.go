package claude

func ApplyPromptCacheUsage(message map[string]any, hitTokens, missTokens int, hasHit, hasMiss bool) {
	if message == nil || (!hasHit && !hasMiss) {
		return
	}
	usage, _ := message["usage"].(map[string]any)
	if usage == nil {
		usage = map[string]any{}
		message["usage"] = usage
	}
	ApplyPromptCacheUsageToUsage(usage, hitTokens, missTokens, hasHit, hasMiss)
}

func ApplyPromptCacheUsageToUsage(usage map[string]any, hitTokens, missTokens int, hasHit, hasMiss bool) {
	if usage == nil || (!hasHit && !hasMiss) {
		return
	}
	if hasHit {
		if hitTokens < 0 {
			hitTokens = 0
		}
		usage["cache_read_input_tokens"] = hitTokens
		usage["prompt_cache_hit_tokens"] = hitTokens
	}
	if hasMiss {
		if missTokens < 0 {
			missTokens = 0
		}
		usage["prompt_cache_miss_tokens"] = missTokens
	}
}
