package historycapture

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"DeepSeek_Web_To_API/internal/auth"
	"DeepSeek_Web_To_API/internal/chathistory"
	"DeepSeek_Web_To_API/internal/config"
	openaifmt "DeepSeek_Web_To_API/internal/format/openai"
	"DeepSeek_Web_To_API/internal/prompt"
	"DeepSeek_Web_To_API/internal/promptcompat"
)

const (
	AdminWebUISourceHeader       = "X-DeepSeek-Web-To-API-Source"
	LegacyAdminWebUISourceHeader = "X-Ds2-Source"
	AdminWebUISourceValue        = "admin-webui-api-tester"
)

type Session struct {
	store       *chathistory.Store
	entryID     string
	startedAt   time.Time
	lastPersist time.Time
	finalPrompt string
	startParams chathistory.StartParams
	disabled    bool
}

func Start(store *chathistory.Store, r *http.Request, a *auth.RequestAuth, stdReq promptcompat.StandardRequest) *Session {
	return StartWithStatus(store, r, a, stdReq, "streaming")
}

func StartWithStatus(store *chathistory.Store, r *http.Request, a *auth.RequestAuth, stdReq promptcompat.StandardRequest, status string) *Session {
	if store == nil || r == nil || a == nil {
		return nil
	}
	if !store.Enabled() || !ShouldCapture(r) {
		return nil
	}
	startParams := chathistory.StartParams{
		CallerID:    strings.TrimSpace(a.CallerID),
		AccountID:   strings.TrimSpace(a.AccountID),
		Status:      strings.TrimSpace(status),
		Model:       strings.TrimSpace(stdReq.ResponseModel),
		Stream:      stdReq.Stream,
		UserInput:   extractSingleUserInput(stdReq.Messages),
		Messages:    extractAllMessages(stdReq.Messages),
		HistoryText: stdReq.HistoryText,
		FinalPrompt: stdReq.FinalPrompt,
	}
	entry, err := store.Start(startParams)
	if err != nil && entry.ID == "" {
		if !errors.Is(err, chathistory.ErrDisabled) {
			config.Logger.Warn("[chat_history] start failed", "error", err)
		}
		return nil
	}
	if err != nil {
		config.Logger.Warn("[chat_history] start persisted in memory after write failure", "error", err)
	}
	now := time.Now()
	return &Session{
		store:       store,
		entryID:     entry.ID,
		startedAt:   now,
		lastPersist: now,
		finalPrompt: stdReq.FinalPrompt,
		startParams: startParams,
	}
}

func ShouldCapture(r *http.Request) bool {
	if r == nil {
		return false
	}
	source := strings.TrimSpace(r.Header.Get(AdminWebUISourceHeader))
	if source == "" {
		source = strings.TrimSpace(r.Header.Get(LegacyAdminWebUISourceHeader))
	}
	return source != AdminWebUISourceValue
}

func (s *Session) BindAuth(a *auth.RequestAuth) {
	if s == nil || s.store == nil || s.disabled || a == nil {
		return
	}
	callerID := strings.TrimSpace(a.CallerID)
	accountID := strings.TrimSpace(a.AccountID)
	if callerID == "" && accountID == "" {
		return
	}
	s.startParams.CallerID = callerID
	s.startParams.AccountID = accountID
	s.startParams.Status = "streaming"
	s.persistUpdate(chathistory.UpdateParams{
		CallerID:  callerID,
		AccountID: accountID,
		Status:    "streaming",
		ElapsedMs: time.Since(s.startedAt).Milliseconds(),
	})
}

func (s *Session) UpdateRequestSnapshot(stdReq promptcompat.StandardRequest) {
	if s == nil || s.store == nil || s.disabled {
		return
	}
	historyText := stdReq.HistoryText
	finalPrompt := stdReq.FinalPrompt
	if strings.TrimSpace(historyText) == "" && strings.TrimSpace(finalPrompt) == "" {
		return
	}
	if strings.TrimSpace(finalPrompt) != "" {
		s.finalPrompt = finalPrompt
	}
	if strings.TrimSpace(historyText) != "" {
		s.startParams.HistoryText = historyText
	}
	if strings.TrimSpace(finalPrompt) != "" {
		s.startParams.FinalPrompt = finalPrompt
	}
	s.persistUpdate(chathistory.UpdateParams{
		HistoryText: historyText,
		FinalPrompt: finalPrompt,
		ElapsedMs:   time.Since(s.startedAt).Milliseconds(),
	})
}

func (s *Session) Progress(thinking, content string) {
	if s == nil || s.store == nil || s.disabled {
		return
	}
	now := time.Now()
	if now.Sub(s.lastPersist) < 250*time.Millisecond {
		return
	}
	s.lastPersist = now
	s.persistUpdate(chathistory.UpdateParams{
		Status:           "streaming",
		ReasoningContent: thinking,
		Content:          content,
		StatusCode:       http.StatusOK,
		ElapsedMs:        time.Since(s.startedAt).Milliseconds(),
	})
}

func (s *Session) Success(statusCode int, thinking, content, finishReason string, usage map[string]any) {
	if s == nil || s.store == nil || s.disabled {
		return
	}
	s.persistUpdate(chathistory.UpdateParams{
		Status:           "success",
		ReasoningContent: thinking,
		Content:          content,
		StatusCode:       statusCode,
		ElapsedMs:        time.Since(s.startedAt).Milliseconds(),
		FinishReason:     finishReason,
		Usage:            usage,
		Completed:        true,
	})
}

func (s *Session) Error(statusCode int, message, finishReason, thinking, content string) {
	if s == nil || s.store == nil || s.disabled {
		return
	}
	s.persistUpdate(chathistory.UpdateParams{
		Status:           "error",
		ReasoningContent: thinking,
		Content:          content,
		Error:            message,
		StatusCode:       statusCode,
		ElapsedMs:        time.Since(s.startedAt).Milliseconds(),
		FinishReason:     finishReason,
		Completed:        true,
	})
}

func (s *Session) Stopped(thinking, content, finishReason string) {
	if s == nil || s.store == nil || s.disabled {
		return
	}
	s.persistUpdate(chathistory.UpdateParams{
		Status:           "stopped",
		ReasoningContent: thinking,
		Content:          content,
		StatusCode:       http.StatusOK,
		ElapsedMs:        time.Since(s.startedAt).Milliseconds(),
		FinishReason:     finishReason,
		Usage:            openaifmt.BuildChatUsage(s.finalPrompt, thinking, content),
		Completed:        true,
	})
}

func (s *Session) retryMissingEntry() bool {
	if s == nil || s.store == nil || s.disabled {
		return false
	}
	entry, err := s.store.Start(s.startParams)
	if errors.Is(err, chathistory.ErrDisabled) {
		s.disabled = true
		return false
	}
	if entry.ID == "" {
		if err != nil {
			config.Logger.Warn("[chat_history] recreate missing entry failed", "error", err)
		}
		return false
	}
	s.entryID = entry.ID
	if err != nil {
		config.Logger.Warn("[chat_history] recreate missing entry persisted in memory after write failure", "error", err)
	}
	return true
}

func (s *Session) persistUpdate(params chathistory.UpdateParams) {
	if s == nil || s.store == nil || s.disabled {
		return
	}
	if _, err := s.store.Update(s.entryID, params); err != nil {
		s.handlePersistError(params, err)
	}
}

func (s *Session) handlePersistError(params chathistory.UpdateParams, err error) {
	if err == nil || s == nil {
		return
	}
	if errors.Is(err, chathistory.ErrDisabled) {
		s.disabled = true
		return
	}
	if isMissingEntryError(err) {
		if s.retryMissingEntry() {
			if _, retryErr := s.store.Update(s.entryID, params); retryErr != nil {
				if errors.Is(retryErr, chathistory.ErrDisabled) || isMissingEntryError(retryErr) {
					s.disabled = true
					return
				}
				config.Logger.Warn("[chat_history] retry after missing entry failed", "error", retryErr)
			}
			return
		}
		s.disabled = true
		return
	}
	config.Logger.Warn("[chat_history] update failed", "error", err)
}

func extractSingleUserInput(messages []any) string {
	for i := len(messages) - 1; i >= 0; i-- {
		msg, ok := messages[i].(map[string]any)
		if !ok {
			continue
		}
		role := strings.ToLower(strings.TrimSpace(historyString(msg["role"])))
		if role != "user" {
			continue
		}
		if normalized := strings.TrimSpace(prompt.NormalizeContent(msg["content"])); normalized != "" {
			return normalized
		}
	}
	return ""
}

func extractAllMessages(messages []any) []chathistory.Message {
	out := make([]chathistory.Message, 0, len(messages))
	for _, raw := range messages {
		msg, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		role := strings.ToLower(strings.TrimSpace(historyString(msg["role"])))
		content := strings.TrimSpace(prompt.NormalizeContent(msg["content"]))
		if role == "" || content == "" {
			continue
		}
		out = append(out, chathistory.Message{
			Role:    role,
			Content: content,
		})
	}
	return out
}

func historyString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case nil:
		return ""
	default:
		return fmt.Sprint(x)
	}
}

func isMissingEntryError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "not found")
}
