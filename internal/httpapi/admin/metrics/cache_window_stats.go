package metrics

import (
	"sync"
	"time"

	"DeepSeek_Web_To_API/internal/promptcache"
	"DeepSeek_Web_To_API/internal/sessioncache"
)

var cacheWindowDefinitions = []struct {
	key    string
	window time.Duration
}{
	{key: "1m", window: time.Minute},
	{key: "5m", window: 5 * time.Minute},
	{key: "15m", window: 15 * time.Minute},
}

var cacheWindowInitMu sync.Mutex

type cacheWindowStats struct {
	Label         string                   `json:"label"`
	WindowSeconds int64                    `json:"window_seconds"`
	ResponseCache cacheWindowResponseStats `json:"response_cache"`
	SessionCache  cacheWindowSessionStats  `json:"session_cache"`
	PromptCache   cacheWindowPromptStats   `json:"prompt_cache"`
}

type cacheWindowResponseStats struct {
	Hits              int64   `json:"hits"`
	Misses            int64   `json:"misses"`
	Stores            int64   `json:"stores"`
	HitRate           float64 `json:"hit_rate"`
	CacheableLookups  int64   `json:"cacheable_lookups"`
	CacheableMisses   int64   `json:"cacheable_misses"`
	CacheableHitRate  float64 `json:"cacheable_hit_rate"`
	UncacheableMisses int64   `json:"uncacheable_misses"`
	SingleflightHits  int64   `json:"singleflight_hits"`
	InflightWaits     int64   `json:"inflight_waits"`
}

type cacheWindowSessionStats struct {
	Hits                    int64 `json:"hits"`
	Misses                  int64 `json:"misses"`
	Stores                  int64 `json:"stores"`
	Invalidations           int64 `json:"invalidations"`
	InvalidSessionErrors    int64 `json:"invalid_session_errors"`
	InvalidSessionCooldowns int64 `json:"invalid_session_cooldowns"`
	CooldownBypasses        int64 `json:"cooldown_bypasses"`
}

type cacheWindowPromptStats struct {
	Observed         int64   `json:"observed"`
	Eligible         int64   `json:"eligible"`
	Reused           int64   `json:"reused"`
	ReuseRate        float64 `json:"reuse_rate"`
	ActualSamples    int64   `json:"actual_samples"`
	ActualHitTokens  int64   `json:"actual_hit_tokens"`
	ActualMissTokens int64   `json:"actual_miss_tokens"`
	ActualHitRate    float64 `json:"actual_hit_rate"`
}

type cacheWindowSample struct {
	at       time.Time
	response overviewCacheStats
	session  sessioncache.Stats
	prompt   promptcache.Stats
}

type cacheWindowSampler struct {
	mu      sync.Mutex
	samples []cacheWindowSample
}

func (h *Handler) cacheWindowStats(now time.Time, response overviewCacheStats, session sessioncache.Stats, prompt promptcache.Stats) map[string]cacheWindowStats {
	cacheWindowInitMu.Lock()
	if h.cacheWindows == nil {
		h.cacheWindows = &cacheWindowSampler{}
	}
	sampler := h.cacheWindows
	cacheWindowInitMu.Unlock()
	return sampler.snapshot(now, response, session, prompt)
}

func (s *cacheWindowSampler) snapshot(now time.Time, response overviewCacheStats, session sessioncache.Stats, prompt promptcache.Stats) map[string]cacheWindowStats {
	if s == nil {
		return nil
	}
	current := cacheWindowSample{at: now, response: response, session: session, prompt: prompt}
	s.mu.Lock()
	s.samples = append(s.samples, current)
	s.pruneLocked(now)
	out := make(map[string]cacheWindowStats, len(cacheWindowDefinitions))
	for _, def := range cacheWindowDefinitions {
		base := s.baselineLocked(now.Add(-def.window))
		out[def.key] = cacheWindowStats{
			Label:         def.key,
			WindowSeconds: int64(def.window.Seconds()),
			ResponseCache: diffResponseCacheWindow(response, base.response),
			SessionCache:  diffSessionCacheWindow(session, base.session),
			PromptCache:   diffPromptCacheWindow(prompt, base.prompt),
		}
	}
	s.mu.Unlock()
	return out
}

func (s *cacheWindowSampler) baselineLocked(cutoff time.Time) cacheWindowSample {
	if len(s.samples) == 0 {
		return cacheWindowSample{}
	}
	base := s.samples[0]
	for _, sample := range s.samples {
		if sample.at.After(cutoff) {
			break
		}
		base = sample
	}
	return base
}

func (s *cacheWindowSampler) pruneLocked(now time.Time) {
	if len(s.samples) == 0 {
		return
	}
	keepAfter := now.Add(-16 * time.Minute)
	idx := 0
	for idx < len(s.samples)-1 && s.samples[idx+1].at.Before(keepAfter) {
		idx++
	}
	if idx > 0 {
		s.samples = append([]cacheWindowSample(nil), s.samples[idx:]...)
	}
}

func diffResponseCacheWindow(current, base overviewCacheStats) cacheWindowResponseStats {
	out := cacheWindowResponseStats{
		Hits:              deltaInt64(current.Hits, base.Hits),
		Misses:            deltaInt64(current.Misses, base.Misses),
		Stores:            deltaInt64(current.Stores, base.Stores),
		CacheableLookups:  deltaInt64(current.CacheableLookups, base.CacheableLookups),
		CacheableMisses:   deltaInt64(current.CacheableMisses, base.CacheableMisses),
		UncacheableMisses: deltaInt64(current.UncacheableMisses, base.UncacheableMisses),
		SingleflightHits:  deltaInt64(current.SingleflightHits, base.SingleflightHits),
		InflightWaits:     deltaInt64(current.InflightWaits, base.InflightWaits),
	}
	if lookups := out.Hits + out.Misses; lookups > 0 {
		out.HitRate = round2(float64(out.Hits) * 100 / float64(lookups))
	}
	if out.CacheableLookups > 0 {
		out.CacheableHitRate = round2(float64(out.Hits) * 100 / float64(out.CacheableLookups))
	}
	return out
}

func diffSessionCacheWindow(current, base sessioncache.Stats) cacheWindowSessionStats {
	return cacheWindowSessionStats{
		Hits:                    deltaInt64(current.Hits, base.Hits),
		Misses:                  deltaInt64(current.Misses, base.Misses),
		Stores:                  deltaInt64(current.Stores, base.Stores),
		Invalidations:           deltaInt64(current.Invalidations, base.Invalidations),
		InvalidSessionErrors:    deltaInt64(current.InvalidSessionErrors, base.InvalidSessionErrors),
		InvalidSessionCooldowns: deltaInt64(current.InvalidSessionCooldowns, base.InvalidSessionCooldowns),
		CooldownBypasses:        deltaInt64(current.CooldownBypasses, base.CooldownBypasses),
	}
}

func diffPromptCacheWindow(current, base promptcache.Stats) cacheWindowPromptStats {
	out := cacheWindowPromptStats{
		Observed:         deltaInt64(current.Observed, base.Observed),
		Eligible:         deltaInt64(current.Eligible, base.Eligible),
		Reused:           deltaInt64(current.Reused, base.Reused),
		ActualSamples:    deltaInt64(current.ActualSamples, base.ActualSamples),
		ActualHitTokens:  deltaInt64(current.ActualHitTokens, base.ActualHitTokens),
		ActualMissTokens: deltaInt64(current.ActualMissTokens, base.ActualMissTokens),
	}
	if out.Eligible > 0 {
		out.ReuseRate = round2(float64(out.Reused) * 100 / float64(out.Eligible))
	}
	if total := out.ActualHitTokens + out.ActualMissTokens; total > 0 {
		out.ActualHitRate = round2(float64(out.ActualHitTokens) * 100 / float64(total))
	}
	return out
}

func deltaInt64(current, base int64) int64 {
	if current <= base {
		return 0
	}
	return current - base
}
