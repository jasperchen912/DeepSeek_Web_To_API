package promptcache

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"time"

	"DeepSeek_Web_To_API/internal/auth"
)

const (
	defaultTTL        = 10 * time.Minute
	defaultMaxEntries = 100000
)

type Options struct {
	TTL        time.Duration
	MaxEntries int
}

type Observation struct {
	Auth         *auth.RequestAuth
	Surface      string
	Model        string
	Thinking     bool
	Search       bool
	PrefixHash   string
	PrefixTokens int
	TailTokens   int
	Eligible     bool
	Hint         string
	Reason       string
}

type Result struct {
	Eligible         bool
	Reused           bool
	PrefixHash       string
	PrefixTokens     int
	TailTokens       int
	HintPresent      bool
	IneligibleReason string
	MissReason       string
}

type Stats struct {
	Observed             int64                   `json:"observed"`
	Eligible             int64                   `json:"eligible"`
	Reused               int64                   `json:"reused"`
	ReuseRate            float64                 `json:"reuse_rate"`
	EstimatedReadTokens  int64                   `json:"estimated_read_tokens"`
	EstimatedWriteTokens int64                   `json:"estimated_write_tokens"`
	ActualSamples        int64                   `json:"actual_samples"`
	ActualHitTokens      int64                   `json:"actual_hit_tokens"`
	ActualMissTokens     int64                   `json:"actual_miss_tokens"`
	ActualHitRate        float64                 `json:"actual_hit_rate"`
	Entries              int64                   `json:"entries"`
	LastPrefixHash       string                  `json:"last_prefix_hash,omitempty"`
	LastHintPresent      bool                    `json:"last_hint_present"`
	IneligibleReasons    map[string]int64        `json:"ineligible_reasons,omitempty"`
	MissReasons          map[string]int64        `json:"miss_reasons,omitempty"`
	BySurface            map[string]SurfaceStats `json:"by_surface,omitempty"`
}

type SurfaceStats struct {
	Observed             int64            `json:"observed"`
	Eligible             int64            `json:"eligible"`
	Reused               int64            `json:"reused"`
	ReuseRate            float64          `json:"reuse_rate"`
	EstimatedReadTokens  int64            `json:"estimated_read_tokens"`
	EstimatedWriteTokens int64            `json:"estimated_write_tokens"`
	ActualSamples        int64            `json:"actual_samples"`
	ActualHitTokens      int64            `json:"actual_hit_tokens"`
	ActualMissTokens     int64            `json:"actual_miss_tokens"`
	ActualHitRate        float64          `json:"actual_hit_rate"`
	LastPrefixHash       string           `json:"last_prefix_hash,omitempty"`
	LastHintPresent      bool             `json:"last_hint_present"`
	IneligibleReasons    map[string]int64 `json:"ineligible_reasons,omitempty"`
	MissReasons          map[string]int64 `json:"miss_reasons,omitempty"`
}

type entry struct {
	prefixHash   string
	prefixTokens int
	updatedAt    time.Time
}

type diagnosticEntry struct {
	model      string
	thinking   bool
	search     bool
	hint       string
	prefixHash string
	updatedAt  time.Time
}

type Cache struct {
	mu          sync.Mutex
	ttl         time.Duration
	maxEntries  int
	entries     map[string]entry
	bySurface   map[string]*surfaceCounters
	lastByScope map[string]diagnosticEntry
	expiredKeys map[string]time.Time
	evictedKeys map[string]time.Time

	observed             int64
	eligible             int64
	reused               int64
	estimatedReadTokens  int64
	estimatedWriteTokens int64
	actualSamples        int64
	actualHitTokens      int64
	actualMissTokens     int64
	lastPrefixHash       string
	lastHintPresent      bool
	ineligibleReasons    map[string]int64
	missReasons          map[string]int64
}

type surfaceCounters struct {
	observed             int64
	eligible             int64
	reused               int64
	estimatedReadTokens  int64
	estimatedWriteTokens int64
	actualSamples        int64
	actualHitTokens      int64
	actualMissTokens     int64
	lastPrefixHash       string
	lastHintPresent      bool
	ineligibleReasons    map[string]int64
	missReasons          map[string]int64
}

func New(opts Options) *Cache {
	ttl := opts.TTL
	if ttl <= 0 {
		ttl = defaultTTL
	}
	maxEntries := opts.MaxEntries
	if maxEntries <= 0 {
		maxEntries = defaultMaxEntries
	}
	return &Cache{
		ttl:         ttl,
		maxEntries:  maxEntries,
		entries:     map[string]entry{},
		bySurface:   map[string]*surfaceCounters{},
		lastByScope: map[string]diagnosticEntry{},
		expiredKeys: map[string]time.Time{},
		evictedKeys: map[string]time.Time{},
	}
}

func (c *Cache) Observe(in Observation) Result {
	result := Result{
		Eligible:     in.Eligible,
		PrefixHash:   normalizeHash(in.PrefixHash),
		PrefixTokens: clampNonNegative(in.PrefixTokens),
		TailTokens:   clampNonNegative(in.TailTokens),
		HintPresent:  strings.TrimSpace(in.Hint) != "",
	}
	if c == nil {
		return result
	}

	now := time.Now()
	key := Key(in)
	c.mu.Lock()
	defer c.mu.Unlock()
	surfaceStats := c.surfaceStatsLocked(in.Surface)

	c.observed++
	c.lastPrefixHash = result.PrefixHash
	c.lastHintPresent = result.HintPresent
	surfaceStats.observed++
	surfaceStats.lastPrefixHash = result.PrefixHash
	surfaceStats.lastHintPresent = result.HintPresent
	if !in.Eligible || key == "" || result.PrefixHash == "" {
		result.IneligibleReason = ineligibleReason(in, key, result.PrefixHash)
		c.recordReasonLocked(&c.ineligibleReasons, &surfaceStats.ineligibleReasons, result.IneligibleReason)
		return result
	}

	c.eligible++
	surfaceStats.eligible++
	if item, ok := c.entries[key]; ok {
		if now.Sub(item.updatedAt) <= c.ttl {
			result.Reused = true
			c.reused++
			c.estimatedReadTokens += int64(result.PrefixTokens)
			surfaceStats.reused++
			surfaceStats.estimatedReadTokens += int64(result.PrefixTokens)
			c.entries[key] = entry{prefixHash: result.PrefixHash, prefixTokens: result.PrefixTokens, updatedAt: now}
			c.rememberDiagnosticLocked(in, result.PrefixHash, now)
			return result
		}
		delete(c.entries, key)
		result.MissReason = "ttl_expired"
	} else {
		c.pruneLocked(now)
	}
	if result.MissReason == "" {
		result.MissReason = c.missReasonLocked(in, key, result.PrefixHash)
	}
	c.recordReasonLocked(&c.missReasons, &surfaceStats.missReasons, result.MissReason)

	c.entries[key] = entry{prefixHash: result.PrefixHash, prefixTokens: result.PrefixTokens, updatedAt: now}
	c.estimatedWriteTokens += int64(result.PrefixTokens)
	surfaceStats.estimatedWriteTokens += int64(result.PrefixTokens)
	c.rememberDiagnosticLocked(in, result.PrefixHash, now)
	c.pruneLocked(now)
	return result
}

func (c *Cache) RecordActualUsage(surface string, hitTokens, missTokens int, hasHit, hasMiss bool) {
	if c == nil || (!hasHit && !hasMiss) {
		return
	}
	hitTokens = clampNonNegative(hitTokens)
	missTokens = clampNonNegative(missTokens)

	c.mu.Lock()
	defer c.mu.Unlock()
	surfaceStats := c.surfaceStatsLocked(surface)

	c.actualSamples++
	surfaceStats.actualSamples++
	if hasHit {
		c.actualHitTokens += int64(hitTokens)
		surfaceStats.actualHitTokens += int64(hitTokens)
	}
	if hasMiss {
		c.actualMissTokens += int64(missTokens)
		surfaceStats.actualMissTokens += int64(missTokens)
	}
}

func (c *Cache) Stats() Stats {
	if c == nil {
		return Stats{}
	}
	now := time.Now()
	c.mu.Lock()
	c.pruneLocked(now)
	out := Stats{
		Observed:             c.observed,
		Eligible:             c.eligible,
		Reused:               c.reused,
		EstimatedReadTokens:  c.estimatedReadTokens,
		EstimatedWriteTokens: c.estimatedWriteTokens,
		ActualSamples:        c.actualSamples,
		ActualHitTokens:      c.actualHitTokens,
		ActualMissTokens:     c.actualMissTokens,
		Entries:              int64(len(c.entries)),
		LastPrefixHash:       c.lastPrefixHash,
		LastHintPresent:      c.lastHintPresent,
		IneligibleReasons:    cloneCounts(c.ineligibleReasons),
		MissReasons:          cloneCounts(c.missReasons),
		BySurface:            c.surfaceSnapshotLocked(),
	}
	c.mu.Unlock()
	if out.Eligible > 0 {
		out.ReuseRate = round2(float64(out.Reused) * 100 / float64(out.Eligible))
	}
	if total := out.ActualHitTokens + out.ActualMissTokens; total > 0 {
		out.ActualHitRate = round2(float64(out.ActualHitTokens) * 100 / float64(total))
	}
	return out
}

func (c *Cache) surfaceStatsLocked(surface string) *surfaceCounters {
	surface = normalizeSurface(surface)
	if c.bySurface == nil {
		c.bySurface = map[string]*surfaceCounters{}
	}
	stats := c.bySurface[surface]
	if stats == nil {
		stats = &surfaceCounters{}
		c.bySurface[surface] = stats
	}
	return stats
}

func (c *Cache) surfaceSnapshotLocked() map[string]SurfaceStats {
	if len(c.bySurface) == 0 {
		return nil
	}
	out := make(map[string]SurfaceStats, len(c.bySurface))
	for surface, stats := range c.bySurface {
		if stats == nil {
			continue
		}
		item := SurfaceStats{
			Observed:             stats.observed,
			Eligible:             stats.eligible,
			Reused:               stats.reused,
			EstimatedReadTokens:  stats.estimatedReadTokens,
			EstimatedWriteTokens: stats.estimatedWriteTokens,
			ActualSamples:        stats.actualSamples,
			ActualHitTokens:      stats.actualHitTokens,
			ActualMissTokens:     stats.actualMissTokens,
			LastPrefixHash:       stats.lastPrefixHash,
			LastHintPresent:      stats.lastHintPresent,
			IneligibleReasons:    cloneCounts(stats.ineligibleReasons),
			MissReasons:          cloneCounts(stats.missReasons),
		}
		if item.Eligible > 0 {
			item.ReuseRate = round2(float64(item.Reused) * 100 / float64(item.Eligible))
		}
		if total := item.ActualHitTokens + item.ActualMissTokens; total > 0 {
			item.ActualHitRate = round2(float64(item.ActualHitTokens) * 100 / float64(total))
		}
		out[surface] = item
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func Key(in Observation) string {
	prefixHash := normalizeHash(in.PrefixHash)
	if in.Auth == nil || prefixHash == "" || !in.Eligible {
		return ""
	}
	caller := strings.TrimSpace(in.Auth.CallerID)
	actor := actor(in.Auth)
	surface := strings.ToLower(strings.TrimSpace(in.Surface))
	model := strings.ToLower(strings.TrimSpace(in.Model))
	if caller == "" || actor == "" || surface == "" || model == "" {
		return ""
	}

	h := sha256.New()
	writePart(h, "v1")
	writePart(h, caller)
	writePart(h, actor)
	writePart(h, surface)
	writePart(h, model)
	writePart(h, boolPart(in.Thinking))
	writePart(h, boolPart(in.Search))
	writePart(h, prefixHash)
	writePart(h, strings.TrimSpace(in.Hint))
	return hex.EncodeToString(h.Sum(nil))
}

func normalizeSurface(surface string) string {
	surface = strings.ToLower(strings.TrimSpace(surface))
	if surface == "" {
		return "unknown"
	}
	return surface
}

func (c *Cache) recordReasonLocked(aggregate, surface *map[string]int64, reason string) {
	reason = normalizeReason(reason)
	if *aggregate == nil {
		*aggregate = map[string]int64{}
	}
	if *surface == nil {
		*surface = map[string]int64{}
	}
	(*aggregate)[reason]++
	(*surface)[reason]++
}

func (c *Cache) missReasonLocked(in Observation, key, prefixHash string) string {
	if _, ok := c.expiredKeys[key]; ok {
		delete(c.expiredKeys, key)
		return "ttl_expired"
	}
	if _, ok := c.evictedKeys[key]; ok {
		delete(c.evictedKeys, key)
		return "evicted"
	}
	scope := diagnosticScope(in)
	if scope == "" {
		return "missing_isolation_key"
	}
	previous, ok := c.lastByScope[scope]
	if !ok {
		return "cold_start"
	}
	model := strings.ToLower(strings.TrimSpace(in.Model))
	hint := strings.TrimSpace(in.Hint)
	switch {
	case previous.model != model:
		return "model_changed"
	case previous.thinking != in.Thinking:
		return "thinking_changed"
	case previous.search != in.Search:
		return "search_changed"
	case previous.hint != hint:
		return "hint_changed"
	case previous.prefixHash != prefixHash:
		return "prefix_changed"
	default:
		return "cold_start"
	}
}

func (c *Cache) rememberDiagnosticLocked(in Observation, prefixHash string, now time.Time) {
	scope := diagnosticScope(in)
	if scope == "" {
		return
	}
	if c.lastByScope == nil {
		c.lastByScope = map[string]diagnosticEntry{}
	}
	c.lastByScope[scope] = diagnosticEntry{
		model:      strings.ToLower(strings.TrimSpace(in.Model)),
		thinking:   in.Thinking,
		search:     in.Search,
		hint:       strings.TrimSpace(in.Hint),
		prefixHash: prefixHash,
		updatedAt:  now,
	}
}

func diagnosticScope(in Observation) string {
	if in.Auth == nil {
		return ""
	}
	caller := strings.TrimSpace(in.Auth.CallerID)
	actor := actor(in.Auth)
	surface := normalizeSurface(in.Surface)
	if caller == "" || actor == "" || surface == "" {
		return ""
	}
	h := sha256.New()
	writePart(h, "scope:v1")
	writePart(h, caller)
	writePart(h, actor)
	writePart(h, surface)
	return hex.EncodeToString(h.Sum(nil))
}

func ineligibleReason(in Observation, key, prefixHash string) string {
	if reason := normalizeReason(in.Reason); reason != "unknown" {
		return reason
	}
	if !in.Eligible {
		return "no_stable_prefix"
	}
	if prefixHash == "" {
		return "missing_prefix_hash"
	}
	if key == "" {
		return "missing_isolation_key"
	}
	return "unknown"
}

func normalizeReason(reason string) string {
	reason = strings.ToLower(strings.TrimSpace(reason))
	if reason == "" {
		return "unknown"
	}
	var b strings.Builder
	previousUnderscore := false
	for _, r := range reason {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			previousUnderscore = false
			continue
		}
		if !previousUnderscore {
			b.WriteByte('_')
			previousUnderscore = true
		}
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		return "unknown"
	}
	return out
}

func (c *Cache) pruneLocked(now time.Time) {
	for key, item := range c.entries {
		if now.Sub(item.updatedAt) > c.ttl {
			delete(c.entries, key)
			if c.expiredKeys == nil {
				c.expiredKeys = map[string]time.Time{}
			}
			c.expiredKeys[key] = now
		}
	}
	for len(c.entries) > c.maxEntries {
		oldestKey := ""
		oldestAt := now
		for key, item := range c.entries {
			if oldestKey == "" || item.updatedAt.Before(oldestAt) {
				oldestKey = key
				oldestAt = item.updatedAt
			}
		}
		if oldestKey == "" {
			return
		}
		delete(c.entries, oldestKey)
		if c.evictedKeys == nil {
			c.evictedKeys = map[string]time.Time{}
		}
		c.evictedKeys[oldestKey] = now
	}
	for key, at := range c.evictedKeys {
		if now.Sub(at) > c.ttl {
			delete(c.evictedKeys, key)
		}
	}
	for key, at := range c.expiredKeys {
		if now.Sub(at) > c.ttl {
			delete(c.expiredKeys, key)
		}
	}
	for scope, item := range c.lastByScope {
		if now.Sub(item.updatedAt) > c.ttl {
			delete(c.lastByScope, scope)
		}
	}
}

func cloneCounts(in map[string]int64) map[string]int64 {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]int64, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func actor(a *auth.RequestAuth) string {
	if a == nil {
		return ""
	}
	if accountID := strings.TrimSpace(a.AccountID); accountID != "" {
		return "account:" + accountID
	}
	if token := strings.TrimSpace(a.DeepSeekToken); token != "" {
		sum := sha256.Sum256([]byte(token))
		return "direct:" + hex.EncodeToString(sum[:])[:16]
	}
	return ""
}

func normalizeHash(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) > 64 {
		value = value[:64]
	}
	return value
}

func clampNonNegative(value int) int {
	if value < 0 {
		return 0
	}
	return value
}

func boolPart(v bool) string {
	if v {
		return "1"
	}
	return "0"
}

func writePart(h interface {
	Write([]byte) (int, error)
}, value string) {
	_, _ = h.Write([]byte(value))
	_, _ = h.Write([]byte{0})
}

func round2(v float64) float64 {
	if v == 0 {
		return 0
	}
	if v > 0 {
		return float64(int(v*100+0.5)) / 100
	}
	return float64(int(v*100-0.5)) / 100
}
