package metrics

import (
	"net/http"
	"time"

	"DeepSeek_Web_To_API/internal/chathistory"
	"DeepSeek_Web_To_API/internal/currentinputmetrics"
	openaihistory "DeepSeek_Web_To_API/internal/httpapi/openai/history"
	"DeepSeek_Web_To_API/internal/promptcache"
	"DeepSeek_Web_To_API/internal/sessioncache"
)

const overviewWindow = time.Minute
const maxInt64StatUint = uint64(1<<63 - 1)

var tokenUsageWindows = []struct {
	key    string
	window time.Duration
}{
	{key: "24h", window: 24 * time.Hour},
	{key: "7d", window: 7 * 24 * time.Hour},
	{key: "15d", window: 15 * 24 * time.Hour},
	{key: "30d", window: 30 * 24 * time.Hour},
}

type overviewMetricsResponse struct {
	Success            bool                         `json:"success"`
	CollectedAt        int64                        `json:"collected_at"`
	WindowSeconds      int64                        `json:"window_seconds"`
	Throughput         overviewThroughput           `json:"throughput"`
	Tokens             chathistory.TokenUsageStats  `json:"tokens"`
	TokenWindows       map[string]tokenWindowStats  `json:"token_windows"`
	Cost               costBreakdown                `json:"cost"`
	Host               hostSnapshot                 `json:"host"`
	Cache              overviewCacheStats           `json:"cache"`
	SessionCache       sessioncache.Stats           `json:"session_cache"`
	PromptCache        promptcache.Stats            `json:"prompt_cache"`
	CurrentInputPrefix currentinputmetrics.Snapshot `json:"current_input_prefix"`
	History            overviewHistoryStats         `json:"history"`
	Pool               map[string]any               `json:"pool,omitempty"`
}

type overviewThroughput struct {
	QPS              float64 `json:"qps"`
	RequestsInWindow int64   `json:"requests_in_window"`
	TokensPerSecond  float64 `json:"tokens_per_second"`
	TokensInWindow   int64   `json:"tokens_in_window"`
}

type tokenWindowStats struct {
	Label         string                                  `json:"label"`
	WindowSeconds int64                                   `json:"window_seconds"`
	Totals        chathistory.TokenUsageTotals            `json:"totals"`
	ByModel       map[string]chathistory.TokenUsageTotals `json:"by_model"`
}

type overviewCacheStats struct {
	Lookups                 int64   `json:"lookups"`
	Hits                    int64   `json:"hits"`
	Misses                  int64   `json:"misses"`
	Stores                  int64   `json:"stores"`
	HitRate                 float64 `json:"hit_rate"`
	MissRate                float64 `json:"miss_rate"`
	CacheableLookups        int64   `json:"cacheable_lookups"`
	CacheableMisses         int64   `json:"cacheable_misses"`
	CacheableHitRate        float64 `json:"cacheable_hit_rate"`
	CacheableMissRate       float64 `json:"cacheable_miss_rate"`
	UncacheableMisses       int64   `json:"uncacheable_misses"`
	MemoryHits              int64   `json:"memory_hits"`
	DiskHits                int64   `json:"disk_hits"`
	UncacheableStatusNon2xx int64   `json:"uncacheable_status_non_2xx"`
	UncacheableEmptyBody    int64   `json:"uncacheable_empty_body"`
	UncacheableOversized    int64   `json:"uncacheable_oversized_response"`
	UncacheableNoStore      int64   `json:"uncacheable_response_no_store"`
	UncacheableSetCookie    int64   `json:"uncacheable_set_cookie"`
	MemoryItems             int64   `json:"memory_items"`
	MemoryBytes             int64   `json:"memory_bytes"`
	MemoryMaxBytes          int64   `json:"memory_max_bytes"`
	MemoryTTLSeconds        int64   `json:"memory_ttl_seconds"`
	DiskMaxBytes            int64   `json:"disk_max_bytes"`
	DiskTTLSeconds          int64   `json:"disk_ttl_seconds"`
	Compression             string  `json:"compression,omitempty"`
}

type overviewHistoryStats struct {
	Total                   int64   `json:"total"`
	RetainedTotal           int64   `json:"retained_total"`
	PrunedTotal             int64   `json:"pruned_total"`
	Limit                   int     `json:"limit"`
	Success                 int64   `json:"success"`
	Failed                  int64   `json:"failed"`
	Active                  int64   `json:"active"`
	Neutral                 int64   `json:"neutral"`
	ExcludedFromFailureRate int64   `json:"excluded_from_failure_rate"`
	EligibleTotal           int64   `json:"eligible_total"`
	SuccessRate             float64 `json:"success_rate"`
	ExcludedStatusCodes     []int   `json:"excluded_status_codes"`
}

func (h *Handler) getOverviewMetrics(w http.ResponseWriter, _ *http.Request) {
	now := time.Now()
	stats, err := h.tokenUsageStats()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	windowSeconds := float64(stats.WindowSeconds)
	if windowSeconds <= 0 {
		windowSeconds = overviewWindow.Seconds()
	}
	resp := overviewMetricsResponse{
		Success:       true,
		CollectedAt:   now.UnixMilli(),
		WindowSeconds: stats.WindowSeconds,
		Throughput: overviewThroughput{
			QPS:              round2(float64(stats.WindowRequests) / windowSeconds),
			RequestsInWindow: stats.WindowRequests,
			TokensPerSecond:  round2(float64(stats.Window.TotalTokens) / windowSeconds),
			TokensInWindow:   stats.Window.TotalTokens,
		},
		Tokens:             stats,
		TokenWindows:       h.tokenWindowStats(),
		Cost:               buildCostBreakdown(stats, now),
		Host:               collectHostSnapshot(now),
		Cache:              h.cacheStats(),
		SessionCache:       h.sessionCacheStats(),
		PromptCache:        h.promptCacheStats(),
		CurrentInputPrefix: h.currentInputPrefixStats(),
		History:            h.historyStats(),
	}
	if h.Pool != nil {
		resp.Pool = h.Pool.Status()
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) tokenWindowStats() map[string]tokenWindowStats {
	out := make(map[string]tokenWindowStats, len(tokenUsageWindows))
	for _, item := range tokenUsageWindows {
		out[item.key] = tokenWindowStats{
			Label:         item.key,
			WindowSeconds: int64(item.window.Seconds()),
			ByModel:       map[string]chathistory.TokenUsageTotals{},
		}
	}
	if h.ChatHistory == nil {
		return out
	}
	for _, item := range tokenUsageWindows {
		stats, err := h.ChatHistory.TokenUsageStats(item.window)
		if err != nil {
			continue
		}
		out[item.key] = tokenWindowStats{
			Label:         item.key,
			WindowSeconds: stats.WindowSeconds,
			Totals:        stats.Window,
			ByModel:       stats.WindowByModel,
		}
	}
	return out
}

func (h *Handler) tokenUsageStats() (chathistory.TokenUsageStats, error) {
	if h.ChatHistory == nil {
		return chathistory.TokenUsageStats{
			WindowSeconds: int64(overviewWindow.Seconds()),
			WindowByModel: map[string]chathistory.TokenUsageTotals{},
			TotalByModel:  map[string]chathistory.TokenUsageTotals{},
		}, nil
	}
	return h.ChatHistory.TokenUsageStats(overviewWindow)
}

func (h *Handler) historyStats() overviewHistoryStats {
	if h.ChatHistory == nil {
		return overviewHistoryStats{
			Limit:               chathistory.DefaultLimit,
			SuccessRate:         100,
			ExcludedStatusCodes: append([]int(nil), chathistory.FailureRateExcludedStatusCodes...),
		}
	}
	snapshot, total, err := h.ChatHistory.SnapshotPage(0, 0)
	if err != nil {
		return overviewHistoryStats{}
	}
	outcome, err := h.ChatHistory.OutcomeStats()
	if err != nil {
		return overviewHistoryStats{
			Total:               int64(total),
			Limit:               snapshot.Limit,
			SuccessRate:         100,
			ExcludedStatusCodes: append([]int(nil), chathistory.FailureRateExcludedStatusCodes...),
		}
	}
	retainedTotal := int64(total)
	cumulativeTotal := outcome.Total
	if cumulativeTotal < retainedTotal {
		cumulativeTotal = retainedTotal
	}
	return overviewHistoryStats{
		Total:                   cumulativeTotal,
		RetainedTotal:           retainedTotal,
		PrunedTotal:             outcome.PrunedTotal,
		Limit:                   snapshot.Limit,
		Success:                 outcome.Success,
		Failed:                  outcome.Failed,
		Active:                  outcome.Active,
		Neutral:                 outcome.Neutral,
		ExcludedFromFailureRate: outcome.ExcludedFromFailureRate,
		EligibleTotal:           outcome.EligibleTotal,
		SuccessRate:             outcome.SuccessRate,
		ExcludedStatusCodes:     outcome.ExcludedStatusCodes,
	}
}

func (h *Handler) currentInputPrefixStats() currentinputmetrics.Snapshot {
	currentinputmetrics.SetActiveStates(openaihistory.ActiveCurrentInputPrefixStates())
	return currentinputmetrics.GetSnapshot()
}

func (h *Handler) sessionCacheStats() sessioncache.Stats {
	if h.SessionCache == nil {
		return sessioncache.Stats{}
	}
	return h.SessionCache.Stats()
}

func (h *Handler) promptCacheStats() promptcache.Stats {
	if h.PromptCache == nil {
		return promptcache.Stats{}
	}
	return h.PromptCache.Stats()
}

func (h *Handler) cacheStats() overviewCacheStats {
	if h.ResponseCache == nil {
		return overviewCacheStats{}
	}
	raw := h.ResponseCache.Stats()
	hits := int64Stat(raw, "hits")
	misses := int64Stat(raw, "misses")
	stores := int64Stat(raw, "stores")
	lookups := int64Stat(raw, "lookups")
	if lookups <= 0 {
		lookups = hits + misses
	}
	cacheableLookups := int64Stat(raw, "cacheable_lookups")
	if cacheableLookups <= 0 {
		cacheableLookups = hits + stores
	}
	cacheableMisses := int64Stat(raw, "cacheable_misses")
	if cacheableMisses <= 0 && stores > 0 {
		cacheableMisses = stores
	}
	stats := overviewCacheStats{
		Lookups:                 lookups,
		Hits:                    hits,
		Misses:                  misses,
		Stores:                  stores,
		CacheableLookups:        cacheableLookups,
		CacheableMisses:         cacheableMisses,
		UncacheableMisses:       int64Stat(raw, "uncacheable_misses"),
		MemoryHits:              int64Stat(raw, "memory_hits"),
		DiskHits:                int64Stat(raw, "disk_hits"),
		UncacheableStatusNon2xx: int64Stat(raw, "uncacheable_status_non_2xx"),
		UncacheableEmptyBody:    int64Stat(raw, "uncacheable_empty_body"),
		UncacheableOversized:    int64Stat(raw, "uncacheable_oversized_response"),
		UncacheableNoStore:      int64Stat(raw, "uncacheable_response_no_store"),
		UncacheableSetCookie:    int64Stat(raw, "uncacheable_set_cookie"),
		MemoryItems:             int64Stat(raw, "memory_items"),
		MemoryBytes:             int64Stat(raw, "memory_bytes"),
		MemoryMaxBytes:          int64Stat(raw, "memory_max_bytes"),
		MemoryTTLSeconds:        int64Stat(raw, "memory_ttl_seconds"),
		DiskMaxBytes:            int64Stat(raw, "disk_max_bytes"),
		DiskTTLSeconds:          int64Stat(raw, "disk_ttl_seconds"),
		Compression:             stringStat(raw, "compression"),
	}
	if lookups > 0 {
		stats.HitRate = round2(float64(hits) * 100 / float64(lookups))
		stats.MissRate = round2(float64(misses) * 100 / float64(lookups))
	}
	if cacheableLookups > 0 {
		stats.CacheableHitRate = round2(float64(hits) * 100 / float64(cacheableLookups))
		stats.CacheableMissRate = round2(float64(cacheableMisses) * 100 / float64(cacheableLookups))
	}
	return stats
}

func int64Stat(stats map[string]any, key string) int64 {
	if stats == nil {
		return 0
	}
	switch v := stats[key].(type) {
	case int:
		return int64(v)
	case int8:
		return int64(v)
	case int16:
		return int64(v)
	case int32:
		return int64(v)
	case int64:
		return v
	case uint:
		return uint64ToInt64Stat(uint64(v))
	case uint8:
		return int64(v)
	case uint16:
		return int64(v)
	case uint32:
		return int64(v)
	case uint64:
		return uint64ToInt64Stat(v)
	case float64:
		if v > float64(maxInt64StatUint) || v < -float64(maxInt64StatUint)-1 {
			return 0
		}
		return int64(v)
	default:
		return 0
	}
}

func uint64ToInt64Stat(v uint64) int64 {
	if v > maxInt64StatUint {
		return 0
	}
	return int64(v)
}

func stringStat(stats map[string]any, key string) string {
	if stats == nil {
		return ""
	}
	v, _ := stats[key].(string)
	return v
}
