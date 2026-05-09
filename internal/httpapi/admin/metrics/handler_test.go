package metrics

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"

	"DeepSeek_Web_To_API/internal/chathistory"
	"DeepSeek_Web_To_API/internal/currentinputmetrics"
	"DeepSeek_Web_To_API/internal/promptcache"
	"DeepSeek_Web_To_API/internal/sessioncache"
)

type cacheStatsStub struct {
	stats map[string]any
}

func (s cacheStatsStub) Stats() map[string]any {
	return s.stats
}

type sessionCacheStatsStub struct {
	stats sessioncache.Stats
}

func (s sessionCacheStatsStub) Stats() sessioncache.Stats {
	return s.stats
}

type promptCacheStatsStub struct {
	stats promptcache.Stats
}

func (s promptCacheStatsStub) Stats() promptcache.Stats {
	return s.stats
}

func TestGetOverviewMetricsReturnsUsageCostAndHost(t *testing.T) {
	currentinputmetrics.ResetForTest()
	t.Cleanup(currentinputmetrics.ResetForTest)
	currentinputmetrics.Record(currentinputmetrics.Sample{Applied: true, PrefixReused: false, CheckpointRefresh: true, PrefixHash: "hash-a", PrefixChars: 1000, TailChars: 40, TailEntries: 1, DurationMs: 2000})
	currentinputmetrics.Record(currentinputmetrics.Sample{Applied: true, PrefixReused: true, CheckpointRefresh: false, PrefixHash: "hash-a", PrefixChars: 1000, TailChars: 120, TailEntries: 3, DurationMs: 120})
	store := chathistory.New(filepath.Join(t.TempDir(), "chat_history.json"))
	entry, err := store.Start(chathistory.StartParams{
		Model:     "deepseek-v4-pro",
		UserInput: "hello",
	})
	if err != nil {
		t.Fatalf("start history failed: %v", err)
	}
	if _, err := store.Update(entry.ID, chathistory.UpdateParams{
		Status: "success",
		Usage: map[string]any{
			"input_tokens":            1000,
			"output_tokens":           500,
			"input_cache_hit_tokens":  200,
			"input_cache_miss_tokens": 800,
			"total_tokens":            1500,
		},
		Completed: true,
	}); err != nil {
		t.Fatalf("update history failed: %v", err)
	}

	h := &Handler{
		ChatHistory: store,
		ResponseCache: cacheStatsStub{stats: map[string]any{
			"lookups":                    int64(5),
			"hits":                       int64(3),
			"misses":                     int64(2),
			"stores":                     int64(1),
			"cacheable_lookups":          int64(4),
			"cacheable_misses":           int64(1),
			"uncacheable_misses":         int64(4),
			"uncacheable_status_non_2xx": int64(1),
			"uncacheable_stream_request": int64(2),
			"uncacheable_missing_owner":  int64(1),
			"memory_hits":                int64(2),
			"disk_hits":                  int64(1),
			"singleflight_hits":          int64(5),
			"inflight_waits":             int64(6),
			"memory_items":               2,
			"memory_bytes":               int64(2048),
			"memory_max_bytes":           int64(4096),
			"memory_ttl_seconds":         300,
			"disk_max_bytes":             int64(8192),
			"disk_ttl_seconds":           14400,
			"compression":                "gzip",
		}},
		SessionCache: sessionCacheStatsStub{stats: sessioncache.Stats{
			Hits:               4,
			Misses:             2,
			Stores:             3,
			InflightWaits:      1,
			Invalidations:      1,
			Entries:            2,
			DisabledAutoDelete: 1,
		}},
		PromptCache: promptCacheStatsStub{stats: promptcache.Stats{
			Observed:             5,
			Eligible:             4,
			Reused:               2,
			ReuseRate:            50,
			EstimatedReadTokens:  300,
			EstimatedWriteTokens: 500,
			Entries:              3,
			LastPrefixHash:       "hash-a",
			LastHintPresent:      true,
			BySurface: map[string]promptcache.SurfaceStats{
				"anthropic.messages": {
					Observed:        2,
					Eligible:        2,
					Reused:          1,
					ReuseRate:       50,
					LastHintPresent: true,
				},
			},
		}},
	}
	req := httptest.NewRequest(http.MethodGet, "/admin/metrics/overview", nil)
	rec := httptest.NewRecorder()
	h.getOverviewMetrics(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var body struct {
		Success    bool `json:"success"`
		Throughput struct {
			QPS              float64 `json:"qps"`
			RequestsInWindow int64   `json:"requests_in_window"`
			TokensPerSecond  float64 `json:"tokens_per_second"`
			TokensInWindow   int64   `json:"tokens_in_window"`
		} `json:"throughput"`
		Tokens chathistory.TokenUsageStats `json:"tokens"`
		Cost   struct {
			Currency      string  `json:"currency"`
			TotalUSD      float64 `json:"total_usd"`
			PricingSource string  `json:"pricing_source"`
		} `json:"cost"`
		Cache              overviewCacheStats           `json:"cache"`
		SessionCache       sessioncache.Stats           `json:"session_cache"`
		PromptCache        promptcache.Stats            `json:"prompt_cache"`
		CurrentInputPrefix currentinputmetrics.Snapshot `json:"current_input_prefix"`
		History            overviewHistoryStats         `json:"history"`
		Host               hostSnapshot                 `json:"host"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response failed: %v", err)
	}
	if !body.Success {
		t.Fatalf("expected success response")
	}
	if body.Throughput.RequestsInWindow != 1 || body.Throughput.TokensInWindow != 1500 {
		t.Fatalf("unexpected throughput: %#v", body.Throughput)
	}
	if body.Tokens.Total.TotalTokens != 1500 {
		t.Fatalf("unexpected token totals: %#v", body.Tokens.Total)
	}
	if body.Cost.Currency != pricingCurrency || body.Cost.TotalUSD <= 0 || body.Cost.PricingSource != pricingSourceURL {
		t.Fatalf("unexpected cost breakdown: %#v", body.Cost)
	}
	if body.Cache.HitRate != 60 || body.Cache.MissRate != 40 || body.Cache.CacheableHitRate != 75 || body.Cache.CacheableMissRate != 25 || body.Cache.CacheableMisses != 1 || body.Cache.UncacheableMisses != 4 || body.Cache.MemoryHits != 2 || body.Cache.DiskHits != 1 || body.Cache.SingleflightHits != 5 || body.Cache.InflightWaits != 6 {
		t.Fatalf("unexpected cache metrics: %#v", body.Cache)
	}
	if body.Cache.UncacheableReasons["stream_request"] != 2 || body.Cache.UncacheableReasons["missing_owner"] != 1 || body.Cache.UncacheableReasons["status_non_2xx"] != 1 {
		t.Fatalf("unexpected uncacheable reason metrics: %#v", body.Cache.UncacheableReasons)
	}
	if body.SessionCache.Hits != 4 || body.SessionCache.InflightWaits != 1 || body.SessionCache.DisabledAutoDelete != 1 {
		t.Fatalf("unexpected session cache metrics: %#v", body.SessionCache)
	}
	if body.PromptCache.Observed != 5 || body.PromptCache.Eligible != 4 || body.PromptCache.Reused != 2 || body.PromptCache.ReuseRate != 50 || body.PromptCache.EstimatedReadTokens != 300 || !body.PromptCache.LastHintPresent {
		t.Fatalf("unexpected prompt cache metrics: %#v", body.PromptCache)
	}
	if body.PromptCache.BySurface["anthropic.messages"].ReuseRate != 50 || !body.PromptCache.BySurface["anthropic.messages"].LastHintPresent {
		t.Fatalf("unexpected prompt cache surface metrics: %#v", body.PromptCache.BySurface)
	}
	if body.CurrentInputPrefix.Applied != 2 || body.CurrentInputPrefix.Reused != 1 || body.CurrentInputPrefix.Refreshes != 1 || body.CurrentInputPrefix.ReuseRate != 50 || body.CurrentInputPrefix.CurrentInputFileMsReusedAvg != 120 || body.CurrentInputPrefix.CurrentInputFileMsRefreshAvg != 2000 {
		t.Fatalf("unexpected current input prefix metrics: %#v", body.CurrentInputPrefix)
	}
	if body.History.Total != 1 || body.History.Limit != chathistory.DefaultLimit || body.History.Success != 1 || body.History.SuccessRate != 100 {
		t.Fatalf("unexpected history metrics: %#v", body.History)
	}
	if body.Host.CPU.Cores <= 0 {
		t.Fatalf("expected host cpu cores, got %#v", body.Host.CPU)
	}
}

func TestGetOverviewMetricsWorksWithoutHistoryStore(t *testing.T) {
	h := &Handler{}
	req := httptest.NewRequest(http.MethodGet, "/admin/metrics/overview", nil)
	rec := httptest.NewRecorder()
	h.getOverviewMetrics(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var body overviewMetricsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response failed: %v", err)
	}
	if !body.Success || body.WindowSeconds != int64(overviewWindow.Seconds()) {
		t.Fatalf("unexpected empty metrics response: %#v", body)
	}
	if body.Cache.Lookups != 0 || body.Cache.HitRate != 0 {
		t.Fatalf("expected empty cache metrics without cache provider, got %#v", body.Cache)
	}
	if body.History.Total != 0 || body.History.Limit != chathistory.DefaultLimit {
		t.Fatalf("expected default history metrics without history store, got %#v", body.History)
	}
	if body.History.SuccessRate != 100 || len(body.History.ExcludedStatusCodes) == 0 {
		t.Fatalf("expected default success-rate metadata without history store, got %#v", body.History)
	}
}

func TestInt64StatRejectsOverflowingUnsignedValues(t *testing.T) {
	stats := map[string]any{"value": uint64(maxInt64StatUint + 1)}
	if got := int64Stat(stats, "value"); got != 0 {
		t.Fatalf("expected overflowing uint64 to be rejected, got %d", got)
	}

	if strconv.IntSize == 64 {
		stats["value"] = uint(maxInt64StatUint + 1)
		if got := int64Stat(stats, "value"); got != 0 {
			t.Fatalf("expected overflowing uint to be rejected, got %d", got)
		}
	}
}
