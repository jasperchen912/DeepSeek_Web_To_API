package claude

import (
	"context"
	"strings"

	"DeepSeek_Web_To_API/internal/auth"
	openaishared "DeepSeek_Web_To_API/internal/httpapi/openai/shared"
	"DeepSeek_Web_To_API/internal/promptcompat"
	"DeepSeek_Web_To_API/internal/sessioncache"
)

type claudeSessionCacheConfigReader interface {
	SessionCacheEnabled() bool
	AutoDeleteMode() string
}

func (h *Handler) resolveClaudeDeepSeekSession(ctx context.Context, a *auth.RequestAuth, stdReq promptcompat.StandardRequest) (openaishared.SessionResolution, error) {
	if h == nil || h.DS == nil {
		return openaishared.SessionResolution{}, nil
	}
	if a == nil || h.SessionCache == nil || !claudeSessionCacheEnabled(h.Store) || strings.TrimSpace(a.SessionKey) == "" {
		sessionID, err := h.DS.CreateSession(ctx, a, 3)
		return openaishared.SessionResolution{ID: sessionID}, err
	}
	if claudeAutoDeleteMode(h.Store) != "none" {
		h.SessionCache.RecordDisabledAutoDelete()
		sessionID, err := h.DS.CreateSession(ctx, a, 3)
		return openaishared.SessionResolution{ID: sessionID}, err
	}

	key := openaishared.DeepSeekSessionCacheKey(a, stdReq, "anthropic.messages")
	if key == "" {
		sessionID, err := h.DS.CreateSession(ctx, a, 3)
		return openaishared.SessionResolution{ID: sessionID}, err
	}
	value, result, err := h.SessionCache.GetOrCreate(ctx, key, func(createCtx context.Context) (sessioncache.Value, error) {
		sessionID, createErr := h.DS.CreateSession(createCtx, a, 3)
		return sessioncache.Value{SessionID: sessionID, Actor: sessioncache.Actor(a)}, createErr
	})
	if err != nil {
		return openaishared.SessionResolution{Key: key, FromCache: result.Hit, Waited: result.Waited}, err
	}
	if value.Actor != "" && value.Actor != sessioncache.Actor(a) {
		h.SessionCache.Invalidate(key)
		sessionID, createErr := h.DS.CreateSession(ctx, a, 3)
		return openaishared.SessionResolution{ID: sessionID, Key: key}, createErr
	}
	return openaishared.SessionResolution{
		ID:        value.SessionID,
		Key:       key,
		FromCache: result.Hit,
		Waited:    result.Waited,
	}, nil
}

func claudeSessionCacheEnabled(store ConfigReader) bool {
	if store == nil {
		return true
	}
	reader, ok := store.(claudeSessionCacheConfigReader)
	if !ok {
		return true
	}
	return reader.SessionCacheEnabled()
}

func claudeAutoDeleteMode(store ConfigReader) string {
	if store == nil {
		return "none"
	}
	reader, ok := store.(claudeSessionCacheConfigReader)
	if !ok {
		return "none"
	}
	mode := strings.TrimSpace(reader.AutoDeleteMode())
	if mode == "" {
		return "none"
	}
	return mode
}
