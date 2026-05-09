package sessioncache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"DeepSeek_Web_To_API/internal/auth"
	"DeepSeek_Web_To_API/internal/config"
	dsclient "DeepSeek_Web_To_API/internal/deepseek/client"
)

const (
	defaultTTL        = 2 * time.Hour
	defaultMaxEntries = 50000
)

type Options struct {
	TTL        time.Duration
	MaxEntries int
}

type Stats struct {
	Hits               int64 `json:"hits"`
	Misses             int64 `json:"misses"`
	Stores             int64 `json:"stores"`
	InflightWaits      int64 `json:"inflight_waits"`
	Invalidations      int64 `json:"invalidations"`
	Entries            int64 `json:"entries"`
	DisabledAutoDelete int64 `json:"disabled_auto_delete"`
}

type Value struct {
	SessionID string
	Actor     string
}

type Result struct {
	Hit    bool
	Waited bool
	Key    string
}

type KeyInput struct {
	Auth      *auth.RequestAuth
	Surface   string
	Model     string
	ModelType string
	Thinking  bool
	Search    bool
}

type entry struct {
	value     Value
	updatedAt time.Time
}

type call struct {
	done  chan struct{}
	value Value
	err   error
}

type Cache struct {
	mu       sync.Mutex
	ttl      time.Duration
	maxItems int
	entries  map[string]entry
	inflight map[string]*call

	hits               int64
	misses             int64
	stores             int64
	inflightWaits      int64
	invalidations      int64
	disabledAutoDelete int64
}

func New(opts Options) *Cache {
	ttl := opts.TTL
	if ttl <= 0 {
		ttl = defaultTTL
	}
	maxItems := opts.MaxEntries
	if maxItems <= 0 {
		maxItems = defaultMaxEntries
	}
	return &Cache{
		ttl:      ttl,
		maxItems: maxItems,
		entries:  map[string]entry{},
		inflight: map[string]*call{},
	}
}

func (c *Cache) GetOrCreate(ctx context.Context, key string, create func(context.Context) (Value, error)) (Value, Result, error) {
	key = strings.TrimSpace(key)
	if create == nil {
		return Value{}, Result{Key: key}, errors.New("session cache create function is nil")
	}
	if c == nil || key == "" {
		value, err := create(ctx)
		return normalizeValue(value), Result{Key: key}, err
	}

	now := time.Now()
	c.mu.Lock()
	c.pruneLocked(now)
	if item, ok := c.entries[key]; ok && now.Sub(item.updatedAt) <= c.ttl && validValue(item.value) {
		c.hits++
		value := item.value
		c.mu.Unlock()
		return value, Result{Hit: true, Key: key}, nil
	}
	if existing := c.inflight[key]; existing != nil {
		c.inflightWaits++
		c.mu.Unlock()
		select {
		case <-existing.done:
			return existing.value, Result{Hit: existing.err == nil && validValue(existing.value), Waited: true, Key: key}, existing.err
		case <-ctx.Done():
			return Value{}, Result{Waited: true, Key: key}, ctx.Err()
		}
	}
	c.misses++
	current := &call{done: make(chan struct{})}
	c.inflight[key] = current
	c.mu.Unlock()

	value, err := create(ctx)
	value = normalizeValue(value)

	c.mu.Lock()
	if err == nil && validValue(value) {
		c.entries[key] = entry{value: value, updatedAt: time.Now()}
		c.stores++
		c.pruneLocked(time.Now())
	}
	current.value = value
	current.err = err
	delete(c.inflight, key)
	close(current.done)
	c.mu.Unlock()

	return value, Result{Key: key}, err
}

func (c *Cache) Store(key string, value Value) {
	key = strings.TrimSpace(key)
	value = normalizeValue(value)
	if c == nil || key == "" || !validValue(value) {
		return
	}
	c.mu.Lock()
	c.entries[key] = entry{value: value, updatedAt: time.Now()}
	c.stores++
	c.pruneLocked(time.Now())
	c.mu.Unlock()
}

func (c *Cache) Invalidate(key string) {
	key = strings.TrimSpace(key)
	if c == nil || key == "" {
		return
	}
	c.mu.Lock()
	if _, ok := c.entries[key]; ok {
		delete(c.entries, key)
		c.invalidations++
	}
	c.mu.Unlock()
}

func (c *Cache) InvalidateSessionID(sessionID string) {
	sessionID = strings.TrimSpace(sessionID)
	if c == nil || sessionID == "" {
		return
	}
	c.mu.Lock()
	deleted := int64(0)
	for key, item := range c.entries {
		if strings.TrimSpace(item.value.SessionID) == sessionID {
			delete(c.entries, key)
			deleted++
		}
	}
	c.invalidations += deleted
	c.mu.Unlock()
}

func (c *Cache) InvalidateActor(actor string) {
	actor = strings.TrimSpace(actor)
	if c == nil || actor == "" {
		return
	}
	c.mu.Lock()
	deleted := int64(0)
	for key, item := range c.entries {
		if strings.TrimSpace(item.value.Actor) == actor {
			delete(c.entries, key)
			deleted++
		}
	}
	c.invalidations += deleted
	c.mu.Unlock()
}

func (c *Cache) RecordDisabledAutoDelete() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.disabledAutoDelete++
	c.mu.Unlock()
}

func (c *Cache) Stats() Stats {
	if c == nil {
		return Stats{}
	}
	c.mu.Lock()
	c.pruneLocked(time.Now())
	out := Stats{
		Hits:               c.hits,
		Misses:             c.misses,
		Stores:             c.stores,
		InflightWaits:      c.inflightWaits,
		Invalidations:      c.invalidations,
		Entries:            int64(len(c.entries)),
		DisabledAutoDelete: c.disabledAutoDelete,
	}
	c.mu.Unlock()
	return out
}

func (c *Cache) pruneLocked(now time.Time) {
	for key, item := range c.entries {
		if now.Sub(item.updatedAt) > c.ttl {
			delete(c.entries, key)
		}
	}
	for len(c.entries) > c.maxItems {
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

func Key(in KeyInput) string {
	if in.Auth == nil {
		return ""
	}
	caller := strings.TrimSpace(in.Auth.CallerID)
	sessionKey := strings.TrimSpace(in.Auth.SessionKey)
	actor := Actor(in.Auth)
	if caller == "" || sessionKey == "" || actor == "" {
		return ""
	}
	model := strings.ToLower(strings.TrimSpace(in.Model))
	modelType := strings.ToLower(strings.TrimSpace(in.ModelType))
	if modelType == "" {
		modelType = "default"
		if resolvedType, ok := config.GetModelType(model); ok {
			modelType = resolvedType
		}
	}

	h := sha256.New()
	writePart(h, "v1")
	writePart(h, caller)
	writePart(h, sessionKey)
	writePart(h, actor)
	writePart(h, strings.ToLower(strings.TrimSpace(in.Surface)))
	writePart(h, model)
	writePart(h, modelType)
	writePart(h, boolPart(in.Thinking))
	writePart(h, boolPart(in.Search))
	return hex.EncodeToString(h.Sum(nil))
}

func Actor(a *auth.RequestAuth) string {
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

func InvalidatesCompletionError(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	status := 0
	var failure *dsclient.RequestFailure
	if errors.As(err, &failure) {
		switch failure.Kind {
		case dsclient.FailureDirectUnauthorized, dsclient.FailureManagedUnauthorized,
			dsclient.FailureClientCancelled, dsclient.FailureUpstreamTimeout,
			dsclient.FailureUpstreamNetwork:
			return false
		}
		status = failure.StatusCode
		message += " " + failure.Message
	}

	lower := strings.ToLower(strings.TrimSpace(message))
	if !mentionsSession(lower) {
		return false
	}
	if status == http.StatusNotFound {
		return true
	}
	for _, keyword := range []string{
		"invalid",
		"expired",
		"not found",
		"not_found",
		"not exist",
		"does not exist",
		"missing",
		"deleted",
		"closed",
		"unknown",
		"不存在",
		"找不到",
		"过期",
		"失效",
		"无效",
		"删除",
	} {
		if strings.Contains(lower, keyword) {
			return true
		}
	}
	return false
}

func mentionsSession(message string) bool {
	return strings.Contains(message, "session") ||
		strings.Contains(message, "chat_session") ||
		strings.Contains(message, "chat session") ||
		strings.Contains(message, "会话")
}

func normalizeValue(value Value) Value {
	value.SessionID = strings.TrimSpace(value.SessionID)
	value.Actor = strings.TrimSpace(value.Actor)
	return value
}

func validValue(value Value) bool {
	return strings.TrimSpace(value.SessionID) != "" && strings.TrimSpace(value.Actor) != ""
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
