package openai

import "DeepSeek_Web_To_API/internal/util"

func BuildChatUsageForModel(model, finalPrompt, finalThinking, finalText string, refFileTokens int) map[string]any {
	promptTokens := util.CountPromptTokens(finalPrompt, model) + refFileTokens
	reasoningTokens := util.CountOutputTokens(finalThinking, model)
	completionTokens := util.CountOutputTokens(finalText, model)
	outputTokens := reasoningTokens + completionTokens
	totalTokens := promptTokens + outputTokens
	return map[string]any{
		"prompt_tokens":     promptTokens,
		"completion_tokens": outputTokens,
		"total_tokens":      totalTokens,
		"prompt_tokens_details": map[string]any{
			"cached_tokens":      0,
			"cache_write_tokens": 0,
		},
		"completion_tokens_details": map[string]any{
			"reasoning_tokens": reasoningTokens,
		},
		"input":       promptTokens,
		"output":      outputTokens,
		"cacheRead":   0,
		"cacheWrite":  0,
		"totalTokens": totalTokens,
		"cost": map[string]any{
			"input":      0,
			"output":     0,
			"cacheRead":  0,
			"cacheWrite": 0,
			"total":      0,
		},
	}
}

func BuildChatUsage(finalPrompt, finalThinking, finalText string) map[string]any {
	return BuildChatUsageForModel("", finalPrompt, finalThinking, finalText, 0)
}

func BuildResponsesUsageForModel(model, finalPrompt, finalThinking, finalText string, refFileTokens int) map[string]any {
	promptTokens := util.CountPromptTokens(finalPrompt, model) + refFileTokens
	reasoningTokens := util.CountOutputTokens(finalThinking, model)
	completionTokens := util.CountOutputTokens(finalText, model)
	return map[string]any{
		"input_tokens":  promptTokens,
		"output_tokens": reasoningTokens + completionTokens,
		"total_tokens":  promptTokens + reasoningTokens + completionTokens,
	}
}

func BuildResponsesUsage(finalPrompt, finalThinking, finalText string) map[string]any {
	return BuildResponsesUsageForModel("", finalPrompt, finalThinking, finalText, 0)
}

func ApplyPromptCacheUsage(usage map[string]any, hitTokens, missTokens int, hasHit, hasMiss bool) {
	if usage == nil || (!hasHit && !hasMiss) {
		return
	}
	if hasHit {
		if hitTokens < 0 {
			hitTokens = 0
		}
		usage["prompt_cache_hit_tokens"] = hitTokens
		if _, ok := usage["cacheRead"]; ok {
			usage["cacheRead"] = hitTokens
		}
		if _, ok := usage["prompt_tokens"]; ok {
			setUsageDetail(usage, "prompt_tokens_details", "cached_tokens", hitTokens)
		}
		if _, ok := usage["input_tokens"]; ok {
			setUsageDetail(usage, "input_tokens_details", "cached_tokens", hitTokens)
		}
	}
	if hasMiss {
		if missTokens < 0 {
			missTokens = 0
		}
		usage["prompt_cache_miss_tokens"] = missTokens
	}
}

func setUsageDetail(usage map[string]any, parentKey, childKey string, value int) {
	details, _ := usage[parentKey].(map[string]any)
	if details == nil {
		details = map[string]any{}
		usage[parentKey] = details
	}
	details[childKey] = value
}
