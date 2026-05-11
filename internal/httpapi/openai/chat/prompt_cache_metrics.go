package chat

import "DeepSeek_Web_To_API/internal/sse"

func (h *Handler) recordPromptCacheUsage(usage sse.PromptCacheUsage) {
	if h == nil || h.PromptCache == nil {
		return
	}
	h.PromptCache.RecordActualUsage("chat.completions", usage.HitTokens, usage.MissTokens, usage.HasHit, usage.HasMiss)
}
