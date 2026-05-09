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
}
