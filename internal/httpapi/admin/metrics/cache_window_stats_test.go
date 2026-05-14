package metrics

import (
	"testing"
	"time"

	"DeepSeek_Web_To_API/internal/promptcache"
	"DeepSeek_Web_To_API/internal/sessioncache"
)

func TestCacheWindowSamplerComputesDeltasAndRates(t *testing.T) {
	t.Parallel()

	sampler := &cacheWindowSampler{}
	now := time.Unix(2000, 0)
	baseResponse := overviewCacheStats{
		Hits:              10,
		Misses:            6,
		Stores:            4,
		CacheableLookups:  14,
		CacheableMisses:   4,
		UncacheableMisses: 2,
		SingleflightHits:  1,
		InflightWaits:     2,
	}
	baseSession := sessioncache.Stats{
		Hits:                    20,
		Misses:                  5,
		Stores:                  3,
		Invalidations:           1,
		InvalidSessionErrors:    2,
		InvalidSessionCooldowns: 1,
		CooldownBypasses:        4,
	}
	basePrompt := promptcache.Stats{
		Observed:         10,
		Eligible:         8,
		Reused:           4,
		ActualSamples:    2,
		ActualHitTokens:  100,
		ActualMissTokens: 300,
	}
	sampler.snapshot(now.Add(-time.Minute), baseResponse, baseSession, basePrompt)

	currentResponse := overviewCacheStats{
		Hits:              16,
		Misses:            10,
		Stores:            7,
		CacheableLookups:  22,
		CacheableMisses:   6,
		UncacheableMisses: 4,
		SingleflightHits:  3,
		InflightWaits:     5,
	}
	currentSession := sessioncache.Stats{
		Hits:                    29,
		Misses:                  9,
		Stores:                  5,
		Invalidations:           2,
		InvalidSessionErrors:    5,
		InvalidSessionCooldowns: 3,
		CooldownBypasses:        7,
	}
	currentPrompt := promptcache.Stats{
		Observed:         16,
		Eligible:         13,
		Reused:           7,
		ActualSamples:    5,
		ActualHitTokens:  180,
		ActualMissTokens: 420,
	}
	out := sampler.snapshot(now, currentResponse, currentSession, currentPrompt)

	oneMinute := out["1m"]
	if oneMinute.WindowSeconds != 60 {
		t.Fatalf("unexpected window metadata: %#v", oneMinute)
	}
	if oneMinute.ResponseCache.Hits != 6 || oneMinute.ResponseCache.Misses != 4 || oneMinute.ResponseCache.HitRate != 60 || oneMinute.ResponseCache.CacheableHitRate != 75 || oneMinute.ResponseCache.SingleflightHits != 2 || oneMinute.ResponseCache.InflightWaits != 3 {
		t.Fatalf("unexpected response-cache window: %#v", oneMinute.ResponseCache)
	}
	if oneMinute.SessionCache.Hits != 9 || oneMinute.SessionCache.InvalidSessionErrors != 3 || oneMinute.SessionCache.InvalidSessionCooldowns != 2 || oneMinute.SessionCache.CooldownBypasses != 3 {
		t.Fatalf("unexpected session-cache window: %#v", oneMinute.SessionCache)
	}
	if oneMinute.PromptCache.Observed != 6 || oneMinute.PromptCache.Eligible != 5 || oneMinute.PromptCache.Reused != 3 || oneMinute.PromptCache.ReuseRate != 60 || oneMinute.PromptCache.ActualSamples != 3 || oneMinute.PromptCache.ActualHitRate != 40 {
		t.Fatalf("unexpected prompt-cache window: %#v", oneMinute.PromptCache)
	}
}
