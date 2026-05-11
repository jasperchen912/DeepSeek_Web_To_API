package sessioncache

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"DeepSeek_Web_To_API/internal/auth"
	dsclient "DeepSeek_Web_To_API/internal/deepseek/client"
)

func TestKeySeparatesActorModelAndFlags(t *testing.T) {
	baseAuth := &auth.RequestAuth{CallerID: "caller", SessionKey: "session", AccountID: "account-a"}
	base := Key(KeyInput{Auth: baseAuth, Surface: "chat", Model: "deepseek-v4-flash", ModelType: "default", Thinking: true})
	if base == "" {
		t.Fatal("expected non-empty key")
	}
	tests := []KeyInput{
		{Auth: &auth.RequestAuth{CallerID: "caller", SessionKey: "session", AccountID: "account-b"}, Surface: "chat", Model: "deepseek-v4-flash", ModelType: "default", Thinking: true},
		{Auth: &auth.RequestAuth{CallerID: "caller", SessionKey: "other", AccountID: "account-a"}, Surface: "chat", Model: "deepseek-v4-flash", ModelType: "default", Thinking: true},
		{Auth: baseAuth, Surface: "responses", Model: "deepseek-v4-flash", ModelType: "default", Thinking: true},
		{Auth: baseAuth, Surface: "chat", Model: "deepseek-v4-pro", ModelType: "expert", Thinking: true},
		{Auth: baseAuth, Surface: "chat", Model: "deepseek-v4-flash", ModelType: "default", Thinking: false},
		{Auth: baseAuth, Surface: "chat", Model: "deepseek-v4-flash", ModelType: "default", Thinking: true, Search: true},
	}
	for _, input := range tests {
		if got := Key(input); got == base {
			t.Fatalf("expected key to differ for %#v", input)
		}
	}
}

func TestKeySeparatesDirectTokens(t *testing.T) {
	a := &auth.RequestAuth{CallerID: "caller", SessionKey: "session", DeepSeekToken: "token-a"}
	b := &auth.RequestAuth{CallerID: "caller", SessionKey: "session", DeepSeekToken: "token-b"}
	keyA := Key(KeyInput{Auth: a, Surface: "chat", Model: "deepseek-v4-flash"})
	keyB := Key(KeyInput{Auth: b, Surface: "chat", Model: "deepseek-v4-flash"})
	if keyA == "" || keyB == "" || keyA == keyB {
		t.Fatalf("expected direct token keys to differ: %q %q", keyA, keyB)
	}
}

func TestGetOrCreateCachesAndCollapsesInflight(t *testing.T) {
	cache := New(Options{TTL: time.Hour, MaxEntries: 10})
	var calls int64
	key := "key"
	create := func(ctx context.Context) (Value, error) {
		_ = ctx
		atomic.AddInt64(&calls, 1)
		time.Sleep(20 * time.Millisecond)
		return Value{SessionID: "session-id", Actor: "account:a"}, nil
	}

	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			value, _, err := cache.GetOrCreate(context.Background(), key, create)
			if err != nil {
				errs <- err
				return
			}
			if value.SessionID != "session-id" {
				errs <- errors.New("unexpected session id")
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("create calls=%d want=1", calls)
	}
	stats := cache.Stats()
	if stats.Misses != 1 || stats.Stores != 1 || stats.InflightWaits == 0 {
		t.Fatalf("unexpected stats: %#v", stats)
	}

	value, result, err := cache.GetOrCreate(context.Background(), key, create)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Hit || value.SessionID != "session-id" {
		t.Fatalf("expected cache hit, got result=%#v value=%#v", result, value)
	}
}

func TestGetOrCreateRefreshesTTLOnHit(t *testing.T) {
	cache := New(Options{TTL: time.Minute, MaxEntries: 10})
	now := time.Unix(1000, 0)
	cache.now = func() time.Time { return now }
	creates := 0
	create := func(ctx context.Context) (Value, error) {
		_ = ctx
		creates++
		return Value{SessionID: "session-id", Actor: "account:a"}, nil
	}

	if _, result, err := cache.GetOrCreate(context.Background(), "key", create); err != nil || result.Hit {
		t.Fatalf("expected initial miss, got result=%#v err=%v", result, err)
	}
	now = now.Add(30 * time.Second)
	if _, result, err := cache.GetOrCreate(context.Background(), "key", create); err != nil || !result.Hit {
		t.Fatalf("expected hit to refresh TTL, got result=%#v err=%v", result, err)
	}
	now = now.Add(45 * time.Second)
	if _, result, err := cache.GetOrCreate(context.Background(), "key", create); err != nil || !result.Hit {
		t.Fatalf("expected refreshed entry to remain valid, got result=%#v err=%v", result, err)
	}
	if creates != 1 {
		t.Fatalf("creates=%d want=1 before refreshed TTL expires", creates)
	}
	now = now.Add(61 * time.Second)
	if _, result, err := cache.GetOrCreate(context.Background(), "key", create); err != nil || result.Hit {
		t.Fatalf("expected miss after refreshed TTL expires, got result=%#v err=%v", result, err)
	}
	if creates != 2 {
		t.Fatalf("creates=%d want=2 after refreshed TTL expires", creates)
	}
}

func TestInvalidateSessionIDRemovesMatchingEntries(t *testing.T) {
	cache := New(Options{TTL: time.Hour, MaxEntries: 10})
	cache.Store("a", Value{SessionID: "session-id", Actor: "account:a"})
	cache.Store("b", Value{SessionID: "other", Actor: "account:b"})

	cache.InvalidateSessionID("session-id")

	if stats := cache.Stats(); stats.Entries != 1 || stats.Invalidations != 1 {
		t.Fatalf("unexpected stats: %#v", stats)
	}
}

func TestRecordInvalidSessionStartsCooldownAfterRepeatedErrors(t *testing.T) {
	cache := New(Options{TTL: time.Hour, MaxEntries: 10, InvalidCooldown: time.Minute})
	now := time.Unix(1000, 0)
	cache.now = func() time.Time { return now }
	key := "key"
	cache.Store(key, Value{SessionID: "session-a", Actor: "account:a"})

	cache.RecordInvalidSession(key)
	cache.Store(key, Value{SessionID: "session-b", Actor: "account:a"})
	cache.RecordInvalidSession(key)

	calls := 0
	create := func(ctx context.Context) (Value, error) {
		_ = ctx
		calls++
		return Value{SessionID: "fresh", Actor: "account:a"}, nil
	}
	value, result, err := cache.GetOrCreate(context.Background(), key, create)
	if err != nil {
		t.Fatal(err)
	}
	if result.Hit || value.SessionID != "fresh" || calls != 1 {
		t.Fatalf("expected cooldown bypass create, result=%#v value=%#v calls=%d", result, value, calls)
	}
	cache.Store(key, Value{SessionID: "should-not-store", Actor: "account:a"})
	stats := cache.Stats()
	if stats.InvalidSessionErrors != 2 || stats.InvalidSessionCooldowns != 1 || stats.CooldownBypasses != 1 || stats.CooldownEntries != 1 || stats.Entries != 0 {
		t.Fatalf("unexpected cooldown stats: %#v", stats)
	}

	now = now.Add(time.Minute + time.Second)
	value, result, err = cache.GetOrCreate(context.Background(), key, create)
	if err != nil {
		t.Fatal(err)
	}
	if result.Hit || value.SessionID != "fresh" || calls != 2 {
		t.Fatalf("expected post-cooldown create, result=%#v value=%#v calls=%d", result, value, calls)
	}
	if stats := cache.Stats(); stats.CooldownEntries != 0 || stats.Entries != 1 {
		t.Fatalf("expected cooldown to expire and cache to store again, got %#v", stats)
	}
}

func TestInvalidatesCompletionErrorOnlyForSessionFailures(t *testing.T) {
	sessionErr := &dsclient.RequestFailure{
		Kind:       dsclient.FailureUpstreamStatus,
		StatusCode: http.StatusNotFound,
		Message:    "chat_session_id not found",
	}
	if !InvalidatesCompletionError(sessionErr) {
		t.Fatal("expected session not found to invalidate")
	}
	authErr := &dsclient.RequestFailure{
		Kind:    dsclient.FailureDirectUnauthorized,
		Message: "session expired because token expired",
	}
	if InvalidatesCompletionError(authErr) {
		t.Fatal("auth failure should not be treated as stale cached session")
	}
	networkErr := &dsclient.RequestFailure{
		Kind:    dsclient.FailureUpstreamNetwork,
		Message: "temporary network error",
	}
	if InvalidatesCompletionError(networkErr) {
		t.Fatal("network failure should not invalidate session cache")
	}
}
