package shared

import (
	"context"
	"net/http"
	"strings"

	"DeepSeek_Web_To_API/internal/auth"
	"DeepSeek_Web_To_API/internal/config"
)

const emptyOutputRetryAccountSwitchAttempts = 3

type EmptyRetryAccountSwitcher interface {
	SwitchAccount(ctx context.Context, a *auth.RequestAuth) bool
}

func ShouldWriteUpstreamEmptyOutputError(text string) bool {
	return text == ""
}

func UpstreamEmptyOutputDetail(contentFilter bool, text, thinking string) (int, string, string) {
	_ = text
	if contentFilter {
		return http.StatusBadRequest, "Upstream content filtered the response and returned no output.", "content_filter"
	}
	if thinking != "" {
		return http.StatusTooManyRequests, "Upstream account hit a rate limit and returned reasoning without visible output.", "upstream_empty_output"
	}
	return http.StatusTooManyRequests, "Upstream account hit a rate limit and returned empty output.", "upstream_empty_output"
}

func WriteUpstreamEmptyOutputError(w http.ResponseWriter, text, thinking string, contentFilter bool) bool {
	if !ShouldWriteUpstreamEmptyOutputError(text) {
		return false
	}
	status, message, code := UpstreamEmptyOutputDetail(contentFilter, text, thinking)
	WriteOpenAIErrorWithCode(w, status, message, code)
	return true
}

func PrepareEmptyOutputRetry(ctx context.Context, resolver any, ds DeepSeekCaller, a *auth.RequestAuth, basePayload, retryPayload map[string]any, originalPow, surface string, stream bool, retryAttempt int, bindAuth func(*auth.RequestAuth), activeSessionID *string) (string, bool) {
	if ds == nil {
		return originalPow, true
	}
	if switcher, ok := resolver.(EmptyRetryAccountSwitcher); ok && a != nil && a.UseConfigToken {
		oldAccountID := strings.TrimSpace(a.AccountID)
		for switchAttempt := 1; switchAttempt <= emptyOutputRetryAccountSwitchAttempts; switchAttempt++ {
			if !switcher.SwitchAccount(ctx, a) {
				break
			}
			if bindAuth != nil {
				bindAuth(a)
			}
			sessionID, retryPow, sessionErr, powErr := PrepareSessionAndPow(ctx, ds, a, 3)
			if sessionErr != nil {
				config.Logger.Warn("[openai_empty_retry] retry account session creation failed", "surface", surface, "stream", stream, "retry_attempt", retryAttempt, "switch_attempt", switchAttempt, "error", sessionErr)
				continue
			}
			sessionID = strings.TrimSpace(sessionID)
			if sessionID == "" {
				config.Logger.Warn("[openai_empty_retry] retry account returned empty session", "surface", surface, "stream", stream, "retry_attempt", retryAttempt, "switch_attempt", switchAttempt)
				continue
			}
			if powErr != nil {
				config.Logger.Warn("[openai_empty_retry] retry account PoW fetch failed", "surface", surface, "stream", stream, "retry_attempt", retryAttempt, "switch_attempt", switchAttempt, "error", powErr)
				continue
			}
			setEmptyRetrySessionID(basePayload, retryPayload, sessionID)
			if activeSessionID != nil {
				*activeSessionID = sessionID
			}
			config.Logger.Info("[openai_empty_retry] switched managed account for retry", "surface", surface, "stream", stream, "retry_attempt", retryAttempt, "switch_attempt", switchAttempt)
			return retryPow, true
		}
		if oldAccountID != "" && strings.TrimSpace(a.AccountID) != "" && strings.TrimSpace(a.AccountID) != oldAccountID {
			config.Logger.Warn("[openai_empty_retry] managed account switch exhausted before retry", "surface", surface, "stream", stream, "retry_attempt", retryAttempt)
			return "", false
		}
		config.Logger.Warn("[openai_empty_retry] no alternate managed account available; retrying current account", "surface", surface, "stream", stream, "retry_attempt", retryAttempt)
	}
	retryPow, powErr := ds.GetPow(ctx, a, 3)
	if powErr != nil {
		config.Logger.Warn("[openai_empty_retry] retry PoW fetch failed, falling back to original PoW", "surface", surface, "stream", stream, "retry_attempt", retryAttempt, "error", powErr)
		return originalPow, true
	}
	return retryPow, true
}

func setEmptyRetrySessionID(basePayload, retryPayload map[string]any, sessionID string) {
	if strings.TrimSpace(sessionID) == "" {
		return
	}
	if basePayload != nil {
		basePayload["chat_session_id"] = sessionID
	}
	if retryPayload != nil {
		retryPayload["chat_session_id"] = sessionID
	}
}
