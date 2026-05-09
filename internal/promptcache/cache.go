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
}

type Result struct {
	Eligible     bool
	Reused       bool
	PrefixHash   string
	PrefixTokens int
	TailTokens   int
	HintPresent  bool
}

type Stats struct {
	Observed             int64   `json:"observed"`
	Eligible             int64   `json:"eligible"`
	Reused               int64   `json:"reused"`
	ReuseRate            float64 `json:"reuse_rate"`
	EstimatedReadTokens  int64   `json:"estimated_read_tokens"`
	EstimatedWriteTokens int64   `json:"estimated_write_tokens"`
	Entries              int64   `json:"entries"`
	LastPrefixHash       string  `json:"last_prefix_hash,omitempty"`
	LastHintPresent      bool    `json:"last_hint_present"`
}

type entry struct {
	prefixHash   string
	prefixTokens int
	updatedAt    time.Time
}

type Cache struct {
	mu         sync.Mutex
	ttl        time.Duration
	maxEntries int
	entries    map[string]entry

	observed             int64
	eligible             int64
	reused               int64
	estimatedReadTokens  int64
	estimatedWriteTokens int64
	lastPrefixHash       string
	lastHintPresent      bool
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
		ttl:        ttl,
		maxEntries: maxEntries,
		entries:    map[string]entry{},
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

	c.observed++
	c.lastPrefixHash = result.PrefixHash
	c.lastHintPresent = result.HintPresent
	if !in.Eligible || key == "" || result.PrefixHash == "" {
		return result
	}

	c.pruneLocked(now)
	c.eligible++
	if item, ok := c.entries[key]; ok && now.Sub(item.updatedAt) <= c.ttl {
		result.Reused = true
		c.reused++
		c.estimatedReadTokens += int64(result.PrefixTokens)
		c.entries[key] = entry{prefixHash: result.PrefixHash, prefixTokens: result.PrefixTokens, updatedAt: now}
		return result
	}

	c.entries[key] = entry{prefixHash: result.PrefixHash, prefixTokens: result.PrefixTokens, updatedAt: now}
	c.estimatedWriteTokens += int64(result.PrefixTokens)
	c.pruneLocked(now)
	return result
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
		Entries:              int64(len(c.entries)),
		LastPrefixHash:       c.lastPrefixHash,
		LastHintPresent:      c.lastHintPresent,
	}
	c.mu.Unlock()
	if out.Eligible > 0 {
		out.ReuseRate = round2(float64(out.Reused) * 100 / float64(out.Eligible))
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

func (c *Cache) pruneLocked(now time.Time) {
	for key, item := range c.entries {
		if now.Sub(item.updatedAt) > c.ttl {
			delete(c.entries, key)
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
	}
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
