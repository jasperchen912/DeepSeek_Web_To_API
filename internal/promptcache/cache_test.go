package promptcache

import (
	"testing"
	"time"

	"DeepSeek_Web_To_API/internal/auth"
)

func TestObserveTracksReuseAndTokenEstimates(t *testing.T) {
	t.Parallel()

	cache := New(Options{TTL: time.Hour, MaxEntries: 10})
	in := Observation{
		Auth:         &auth.RequestAuth{CallerID: "caller", AccountID: "acct-a"},
		Surface:      "chat.completions",
		Model:        "deepseek-v4-flash",
		PrefixHash:   "hash-a",
		PrefixTokens: 128,
		TailTokens:   12,
		Eligible:     true,
	}
	first := cache.Observe(in)
	second := cache.Observe(in)
	if first.Reused {
		t.Fatal("first observation should not be reused")
	}
	if !second.Reused {
		t.Fatal("second observation should be reused")
	}
	stats := cache.Stats()
	if stats.Observed != 2 || stats.Eligible != 2 || stats.Reused != 1 || stats.ReuseRate != 50 {
		t.Fatalf("unexpected stats: %#v", stats)
	}
	if stats.EstimatedWriteTokens != 128 || stats.EstimatedReadTokens != 128 {
		t.Fatalf("unexpected token estimates: %#v", stats)
	}
	chatStats := stats.BySurface["chat.completions"]
	if chatStats.Observed != 2 || chatStats.Eligible != 2 || chatStats.Reused != 1 || chatStats.ReuseRate != 50 {
		t.Fatalf("unexpected per-surface stats: %#v", stats.BySurface)
	}
	if chatStats.EstimatedWriteTokens != 128 || chatStats.EstimatedReadTokens != 128 {
		t.Fatalf("unexpected per-surface token estimates: %#v", chatStats)
	}
}

func TestKeyVariesByIsolationFields(t *testing.T) {
	t.Parallel()

	base := Observation{
		Auth:       &auth.RequestAuth{CallerID: "caller", AccountID: "acct-a"},
		Surface:    "chat.completions",
		Model:      "deepseek-v4-flash",
		PrefixHash: "hash-a",
		Eligible:   true,
	}
	baseKey := Key(base)
	if baseKey == "" {
		t.Fatal("expected base key")
	}
	cases := []Observation{
		{Auth: &auth.RequestAuth{CallerID: "caller-b", AccountID: "acct-a"}, Surface: base.Surface, Model: base.Model, PrefixHash: base.PrefixHash, Eligible: true},
		{Auth: &auth.RequestAuth{CallerID: "caller", AccountID: "acct-b"}, Surface: base.Surface, Model: base.Model, PrefixHash: base.PrefixHash, Eligible: true},
		{Auth: base.Auth, Surface: "responses", Model: base.Model, PrefixHash: base.PrefixHash, Eligible: true},
		{Auth: base.Auth, Surface: base.Surface, Model: "deepseek-v4-pro", PrefixHash: base.PrefixHash, Eligible: true},
		{Auth: base.Auth, Surface: base.Surface, Model: base.Model, Thinking: true, PrefixHash: base.PrefixHash, Eligible: true},
		{Auth: base.Auth, Surface: base.Surface, Model: base.Model, Search: true, PrefixHash: base.PrefixHash, Eligible: true},
		{Auth: base.Auth, Surface: base.Surface, Model: base.Model, PrefixHash: "hash-b", Eligible: true},
		{Auth: base.Auth, Surface: base.Surface, Model: base.Model, PrefixHash: base.PrefixHash, Hint: "hint-a", Eligible: true},
	}
	for i, tc := range cases {
		if got := Key(tc); got == "" || got == baseKey {
			t.Fatalf("case %d did not vary key: %q", i, got)
		}
	}
}

func TestObserveTTLAndMaxEntries(t *testing.T) {
	t.Parallel()

	cache := New(Options{TTL: time.Millisecond, MaxEntries: 2})
	base := Observation{
		Auth:         &auth.RequestAuth{CallerID: "caller", DeepSeekToken: "token"},
		Surface:      "chat.completions",
		Model:        "deepseek-v4-flash",
		PrefixTokens: 10,
		Eligible:     true,
	}
	a := base
	a.PrefixHash = "hash-a"
	cache.Observe(a)
	time.Sleep(5 * time.Millisecond)
	if got := cache.Observe(a); got.Reused {
		t.Fatal("expired observation should not be reused")
	}

	b := base
	b.PrefixHash = "hash-b"
	c := base
	c.PrefixHash = "hash-c"
	cache.Observe(b)
	cache.Observe(c)
	if stats := cache.Stats(); stats.Entries != 2 {
		t.Fatalf("expected max-entry pruning to keep two entries, got %#v", stats)
	}
}

func TestObserveIneligibleOnlyUpdatesObserved(t *testing.T) {
	t.Parallel()

	cache := New(Options{TTL: time.Hour, MaxEntries: 10})
	result := cache.Observe(Observation{
		Auth:         &auth.RequestAuth{CallerID: "caller", AccountID: "acct-a"},
		Surface:      "chat.completions",
		Model:        "deepseek-v4-flash",
		PrefixHash:   "hash-a",
		PrefixTokens: 10,
		Eligible:     false,
		Hint:         "hint",
	})
	if result.Reused || result.Eligible {
		t.Fatalf("unexpected result for ineligible request: %#v", result)
	}
	stats := cache.Stats()
	if stats.Observed != 1 || stats.Eligible != 0 || stats.Entries != 0 || !stats.LastHintPresent {
		t.Fatalf("unexpected stats: %#v", stats)
	}
	if stats.IneligibleReasons["no_stable_prefix"] != 1 {
		t.Fatalf("expected no_stable_prefix reason, got %#v", stats.IneligibleReasons)
	}
	chatStats := stats.BySurface["chat.completions"]
	if chatStats.Observed != 1 || chatStats.Eligible != 0 || !chatStats.LastHintPresent {
		t.Fatalf("unexpected per-surface stats: %#v", stats.BySurface)
	}
	if chatStats.IneligibleReasons["no_stable_prefix"] != 1 {
		t.Fatalf("expected per-surface no_stable_prefix reason, got %#v", chatStats.IneligibleReasons)
	}
}

func TestObserveTracksMissReasons(t *testing.T) {
	t.Parallel()

	cache := New(Options{TTL: time.Hour, MaxEntries: 10})
	base := Observation{
		Auth:         &auth.RequestAuth{CallerID: "caller", AccountID: "acct-a"},
		Surface:      "chat.completions",
		Model:        "deepseek-v4-flash",
		PrefixHash:   "hash-a",
		PrefixTokens: 10,
		Eligible:     true,
	}

	if got := cache.Observe(base); got.MissReason != "cold_start" {
		t.Fatalf("expected cold_start, got %#v", got)
	}
	modelChanged := base
	modelChanged.Model = "deepseek-v4-pro"
	if got := cache.Observe(modelChanged); got.MissReason != "model_changed" {
		t.Fatalf("expected model_changed, got %#v", got)
	}
	hintChanged := modelChanged
	hintChanged.Hint = "conversation-a"
	if got := cache.Observe(hintChanged); got.MissReason != "hint_changed" {
		t.Fatalf("expected hint_changed, got %#v", got)
	}
	prefixChanged := hintChanged
	prefixChanged.PrefixHash = "hash-b"
	if got := cache.Observe(prefixChanged); got.MissReason != "prefix_changed" {
		t.Fatalf("expected prefix_changed, got %#v", got)
	}

	stats := cache.Stats()
	for _, reason := range []string{"cold_start", "model_changed", "hint_changed", "prefix_changed"} {
		if stats.MissReasons[reason] != 1 {
			t.Fatalf("expected miss reason %s=1, got %#v", reason, stats.MissReasons)
		}
		if stats.BySurface["chat.completions"].MissReasons[reason] != 1 {
			t.Fatalf("expected surface miss reason %s=1, got %#v", reason, stats.BySurface["chat.completions"].MissReasons)
		}
	}
}

func TestObserveTracksTTLAndEvictedMissReasons(t *testing.T) {
	t.Parallel()

	expiring := New(Options{TTL: time.Millisecond, MaxEntries: 10})
	base := Observation{
		Auth:         &auth.RequestAuth{CallerID: "caller", AccountID: "acct-a"},
		Surface:      "chat.completions",
		Model:        "deepseek-v4-flash",
		PrefixHash:   "hash-a",
		PrefixTokens: 10,
		Eligible:     true,
	}
	expiring.Observe(base)
	time.Sleep(5 * time.Millisecond)
	if got := expiring.Observe(base); got.MissReason != "ttl_expired" {
		t.Fatalf("expected ttl_expired, got %#v", got)
	}
	if stats := expiring.Stats(); stats.MissReasons["ttl_expired"] != 1 {
		t.Fatalf("expected ttl_expired miss reason, got %#v", stats.MissReasons)
	}

	evicting := New(Options{TTL: time.Hour, MaxEntries: 1})
	evicting.Observe(base)
	other := base
	other.PrefixHash = "hash-b"
	evicting.Observe(other)
	if got := evicting.Observe(base); got.MissReason != "evicted" {
		t.Fatalf("expected evicted, got %#v", got)
	}
	if stats := evicting.Stats(); stats.MissReasons["evicted"] != 1 {
		t.Fatalf("expected evicted miss reason, got %#v", stats.MissReasons)
	}
}

func TestObserveSeparatesSurfaceStats(t *testing.T) {
	t.Parallel()

	cache := New(Options{TTL: time.Hour, MaxEntries: 10})
	base := Observation{
		Auth:         &auth.RequestAuth{CallerID: "caller", AccountID: "acct-a"},
		Model:        "deepseek-v4-flash",
		PrefixHash:   "hash-a",
		PrefixTokens: 20,
		Eligible:     true,
	}
	chat := base
	chat.Surface = "chat.completions"
	claude := base
	claude.Surface = "anthropic.messages"
	cache.Observe(chat)
	cache.Observe(chat)
	cache.Observe(claude)

	stats := cache.Stats()
	if stats.Reused != 1 {
		t.Fatalf("unexpected aggregate reuse: %#v", stats)
	}
	if got := stats.BySurface["chat.completions"]; got.Observed != 2 || got.Reused != 1 || got.ReuseRate != 50 {
		t.Fatalf("unexpected chat surface stats: %#v", got)
	}
	if got := stats.BySurface["anthropic.messages"]; got.Observed != 1 || got.Reused != 0 || got.ReuseRate != 0 {
		t.Fatalf("unexpected Claude surface stats: %#v", got)
	}
}

func TestRecordActualUsageTracksAggregateAndSurfaceHitRate(t *testing.T) {
	t.Parallel()

	cache := New(Options{TTL: time.Hour, MaxEntries: 10})
	cache.RecordActualUsage("anthropic.messages", 80, 20, true, true)
	cache.RecordActualUsage("chat.completions", 10, 90, true, true)

	stats := cache.Stats()
	if stats.ActualSamples != 2 || stats.ActualHitTokens != 90 || stats.ActualMissTokens != 110 || stats.ActualHitRate != 45 {
		t.Fatalf("unexpected aggregate actual usage stats: %#v", stats)
	}
	claudeStats := stats.BySurface["anthropic.messages"]
	if claudeStats.ActualSamples != 1 || claudeStats.ActualHitTokens != 80 || claudeStats.ActualMissTokens != 20 || claudeStats.ActualHitRate != 80 {
		t.Fatalf("unexpected Claude actual usage stats: %#v", claudeStats)
	}
}
