package shared

import (
	"context"
	"strings"
	"time"

	"DeepSeek_Web_To_API/internal/auth"
	"DeepSeek_Web_To_API/internal/config"
	"DeepSeek_Web_To_API/internal/promptcompat"
	"DeepSeek_Web_To_API/internal/sessioncache"
)

type sessionCacheConfigReader interface {
	SessionCacheEnabled() bool
	SessionCacheTTL() time.Duration
	SessionCacheMaxEntries() int
}

type SessionResolution struct {
	ID        string
	Key       string
	FromCache bool
	Waited    bool
}

func ResolveDeepSeekSession(ctx context.Context, store ConfigReader, cache *sessioncache.Cache, ds DeepSeekCaller, a *auth.RequestAuth, stdReq promptcompat.StandardRequest, surface string) (SessionResolution, error) {
	if ds == nil {
		return SessionResolution{}, nil
	}
	if a == nil || cache == nil || !SessionCacheEnabled(store) || strings.TrimSpace(a.SessionKey) == "" {
		sessionID, err := ds.CreateSession(ctx, a, 3)
		return SessionResolution{ID: sessionID}, err
	}
	if store != nil && store.AutoDeleteMode() != "none" {
		cache.RecordDisabledAutoDelete()
		sessionID, err := ds.CreateSession(ctx, a, 3)
		return SessionResolution{ID: sessionID}, err
	}

	key := DeepSeekSessionCacheKey(a, stdReq, surface)
	if key == "" {
		sessionID, err := ds.CreateSession(ctx, a, 3)
		return SessionResolution{ID: sessionID}, err
	}
	value, result, err := cache.GetOrCreate(ctx, key, func(createCtx context.Context) (sessioncache.Value, error) {
		sessionID, createErr := ds.CreateSession(createCtx, a, 3)
		return sessioncache.Value{SessionID: sessionID, Actor: sessioncache.Actor(a)}, createErr
	})
	if err != nil {
		return SessionResolution{Key: key, FromCache: result.Hit, Waited: result.Waited}, err
	}
	if value.Actor != "" && value.Actor != sessioncache.Actor(a) {
		cache.Invalidate(key)
		sessionID, createErr := ds.CreateSession(ctx, a, 3)
		return SessionResolution{ID: sessionID, Key: key}, createErr
	}
	return SessionResolution{
		ID:        value.SessionID,
		Key:       key,
		FromCache: result.Hit,
		Waited:    result.Waited,
	}, nil
}

func StoreDeepSeekSession(cache *sessioncache.Cache, a *auth.RequestAuth, stdReq promptcompat.StandardRequest, surface, sessionID, previousKey string) {
	if cache == nil || strings.TrimSpace(previousKey) == "" {
		return
	}
	key := DeepSeekSessionCacheKey(a, stdReq, surface)
	value := sessioncache.Value{SessionID: sessionID, Actor: sessioncache.Actor(a)}
	if key != "" {
		cache.Store(key, value)
	}
	if previousKey != key {
		cache.Invalidate(previousKey)
	}
}

func DeepSeekSessionCacheKey(a *auth.RequestAuth, stdReq promptcompat.StandardRequest, surface string) string {
	model := strings.TrimSpace(stdReq.ResolvedModel)
	if model == "" {
		model = strings.TrimSpace(stdReq.RequestedModel)
	}
	modelType := "default"
	if resolvedType, ok := config.GetModelType(model); ok {
		modelType = resolvedType
	}
	return sessioncache.Key(sessioncache.KeyInput{
		Auth:      a,
		Surface:   surface,
		Model:     model,
		ModelType: modelType,
		Thinking:  stdReq.Thinking,
		Search:    stdReq.Search,
	})
}

func SessionCacheEnabled(store ConfigReader) bool {
	if store == nil {
		return true
	}
	reader, ok := store.(sessionCacheConfigReader)
	if !ok {
		return true
	}
	return reader.SessionCacheEnabled()
}
