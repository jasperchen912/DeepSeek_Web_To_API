package history

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"DeepSeek_Web_To_API/internal/auth"
	"DeepSeek_Web_To_API/internal/promptcompat"
	"DeepSeek_Web_To_API/internal/util"
)

const (
	currentInputTargetTailChars = 32 * 1024
	currentInputMaxTailChars    = 64 * 1024
	currentInputPrefixTTL       = 30 * time.Minute
	currentInputPrefixMaxStates = 2048
)

type currentInputPrefixState struct {
	Key        string
	PrefixText string
	PrefixHash string
	FileID     string
	UpdatedAt  time.Time
}

type currentInputPrefixPlan struct {
	Key               string
	PrefixText        string
	PrefixHash        string
	FileID            string
	TailText          string
	Reused            bool
	CheckpointRefresh bool
}

var globalCurrentInputPrefixStore = &currentInputPrefixStore{states: map[string]currentInputPrefixState{}}

type currentInputPrefixStore struct {
	mu     sync.Mutex
	states map[string]currentInputPrefixState
}

func (s Service) applyCurrentInputStablePrefix(ctx context.Context, a *auth.RequestAuth, stdReq promptcompat.StandardRequest, fullText, modelType string) (promptcompat.StandardRequest, bool, error) {
	key := currentInputPrefixKey(a, stdReq, modelType)
	if key == "" {
		return stdReq, false, nil
	}

	plan, ok := globalCurrentInputPrefixStore.plan(key, fullText)
	if !ok {
		return stdReq, false, nil
	}
	if !plan.Reused {
		fileID, err := s.uploadCurrentInputFile(ctx, a, plan.PrefixText, modelType)
		if err != nil {
			return stdReq, true, err
		}
		plan.FileID = fileID
		globalCurrentInputPrefixStore.store(plan)
	}

	messages := []any{
		map[string]any{
			"role":    "user",
			"content": currentInputFilePromptWithTail(plan.TailText),
		},
	}

	stdReq.Messages = messages
	stdReq.HistoryText = fullText
	stdReq.CurrentInputFileApplied = true
	stdReq.CurrentInputPrefixHash = plan.PrefixHash
	stdReq.CurrentInputPrefixReused = plan.Reused
	stdReq.CurrentInputPrefixChars = len(plan.PrefixText)
	stdReq.CurrentInputTailChars = len(strings.TrimSpace(plan.TailText))
	stdReq.CurrentInputTailEntries = countTranscriptEntries(plan.TailText)
	stdReq.CurrentInputCheckpointRefresh = plan.CheckpointRefresh
	stdReq.RefFileIDs = prependUniqueRefFileID(stdReq.RefFileIDs, plan.FileID)
	stdReq.FinalPrompt, stdReq.ToolNames = promptcompat.BuildOpenAIPrompt(messages, stdReq.ToolsRaw, "", stdReq.ToolChoice, stdReq.Thinking)
	stdReq.RefFileTokens += util.CountPromptTokens(plan.PrefixText, stdReq.ResponseModel)
	stdReq.PromptTokenText = plan.PrefixText + "\n" + stdReq.FinalPrompt
	return stdReq, true, nil
}

func (s *currentInputPrefixStore) plan(key, fullText string) (currentInputPrefixPlan, bool) {
	fullText = strings.TrimSpace(fullText)
	if key == "" || fullText == "" {
		return currentInputPrefixPlan{}, false
	}
	fullText += "\n"
	now := time.Now()

	s.mu.Lock()
	s.pruneLocked(now)
	state, hasState := s.states[key]
	if hasState && now.Sub(state.UpdatedAt) > currentInputPrefixTTL {
		delete(s.states, key)
		hasState = false
	}
	if hasState && state.FileID != "" && state.PrefixText != "" && strings.HasPrefix(fullText, state.PrefixText) {
		tail := fullText[len(state.PrefixText):]
		if len(tail) <= currentInputMaxTailChars {
			state.UpdatedAt = now
			s.states[key] = state
			s.mu.Unlock()
			return currentInputPrefixPlan{
				Key:        key,
				PrefixText: state.PrefixText,
				PrefixHash: state.PrefixHash,
				FileID:     state.FileID,
				TailText:   tail,
				Reused:     true,
			}, true
		}
	}
	s.mu.Unlock()

	prefix, tail, ok := splitCurrentInputPrefixTail(fullText)
	if !ok {
		return currentInputPrefixPlan{}, false
	}
	return currentInputPrefixPlan{
		Key:               key,
		PrefixText:        prefix,
		PrefixHash:        currentInputTextHash(prefix),
		TailText:          tail,
		CheckpointRefresh: true,
	}, true
}

func ActiveCurrentInputPrefixStates() int64 {
	return globalCurrentInputPrefixStore.activeStates()
}

func (s *currentInputPrefixStore) activeStates() int64 {
	if s == nil {
		return 0
	}
	now := time.Now()
	s.mu.Lock()
	s.pruneLocked(now)
	count := int64(len(s.states))
	s.mu.Unlock()
	return count
}

func (s *currentInputPrefixStore) store(plan currentInputPrefixPlan) {
	if plan.Key == "" || plan.FileID == "" || plan.PrefixText == "" {
		return
	}
	now := time.Now()
	s.mu.Lock()
	s.pruneLocked(now)
	s.states[plan.Key] = currentInputPrefixState{
		Key:        plan.Key,
		PrefixText: plan.PrefixText,
		PrefixHash: plan.PrefixHash,
		FileID:     plan.FileID,
		UpdatedAt:  now,
	}
	s.mu.Unlock()
}

func (s *currentInputPrefixStore) pruneLocked(now time.Time) {
	for key, state := range s.states {
		if now.Sub(state.UpdatedAt) > currentInputPrefixTTL {
			delete(s.states, key)
		}
	}
	if len(s.states) <= currentInputPrefixMaxStates {
		return
	}
	oldestKey := ""
	oldestAt := now
	for key, state := range s.states {
		if oldestKey == "" || state.UpdatedAt.Before(oldestAt) {
			oldestKey = key
			oldestAt = state.UpdatedAt
		}
	}
	if oldestKey != "" {
		delete(s.states, oldestKey)
	}
}

func splitCurrentInputPrefixTail(fullText string) (string, string, bool) {
	fullText = strings.TrimSpace(fullText)
	if fullText == "" || len(fullText) <= currentInputTargetTailChars {
		return "", "", false
	}
	fullText += "\n"
	desiredTailStart := len(fullText) - currentInputTargetTailChars
	cut := transcriptBoundaryAtOrAfter(fullText, desiredTailStart)
	if cut < 0 {
		return "", "", false
	}
	prefix := fullText[:cut]
	tail := fullText[cut:]
	if len(prefix) < currentInputTargetTailChars/4 || strings.TrimSpace(tail) == "" || len(tail) > currentInputMaxTailChars {
		return "", "", false
	}
	return prefix, tail, true
}

func transcriptBoundaryAtOrAfter(text string, start int) int {
	if start < 0 {
		start = 0
	}
	if start >= len(text) {
		return -1
	}
	idx := strings.Index(text[start:], "\n=== ")
	if idx < 0 {
		return -1
	}
	return start + idx + 1
}

func countTranscriptEntries(text string) int {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0
	}
	count := 0
	if strings.HasPrefix(text, "=== ") {
		count++
	}
	count += strings.Count(text, "\n=== ")
	return count
}

func currentInputFilePromptWithTail(tailText string) string {
	tailText = strings.TrimSpace(tailText)
	if tailText == "" {
		return currentInputFilePrompt()
	}
	return "Continue from the stable prefix in the attached DEEPSEEK_WEB_TO_API_HISTORY.txt context, then apply the recent conversation tail below. Answer the latest user request directly.\n\nRecent conversation tail after the attached prefix:\n" + tailText
}

func currentInputPrefixKey(a *auth.RequestAuth, stdReq promptcompat.StandardRequest, modelType string) string {
	if a == nil || strings.TrimSpace(a.SessionKey) == "" {
		return ""
	}
	actor := strings.TrimSpace(a.AccountID)
	if actor == "" && strings.TrimSpace(a.DeepSeekToken) != "" {
		actor = "direct:" + currentInputTextHash(a.DeepSeekToken)
	}
	if actor == "" {
		return ""
	}
	model := strings.TrimSpace(stdReq.ResolvedModel)
	if model == "" {
		model = strings.TrimSpace(stdReq.RequestedModel)
	}
	return strings.Join([]string{
		strings.TrimSpace(a.CallerID),
		strings.TrimSpace(a.SessionKey),
		actor,
		model,
		strings.TrimSpace(modelType),
		fmt.Sprintf("thinking=%t", stdReq.Thinking),
		fmt.Sprintf("search=%t", stdReq.Search),
	}, "|")
}

func currentInputTextHash(text string) string {
	if text == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])[:16]
}
