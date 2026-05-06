package openai

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"DeepSeek_Web_To_API/internal/auth"
	dsclient "DeepSeek_Web_To_API/internal/deepseek/client"
	openaishared "DeepSeek_Web_To_API/internal/httpapi/openai/shared"
	"DeepSeek_Web_To_API/internal/promptcompat"
	"DeepSeek_Web_To_API/internal/util"
)

func historySplitTestMessages() []any {
	toolCalls := []any{
		map[string]any{
			"name":      "search",
			"arguments": map[string]any{"query": "docs"},
		},
	}
	return []any{
		map[string]any{"role": "system", "content": "system instructions"},
		map[string]any{"role": "user", "content": "first user turn"},
		map[string]any{
			"role":              "assistant",
			"content":           "",
			"reasoning_content": "hidden reasoning",
			"tool_calls":        toolCalls,
		},
		map[string]any{
			"role":         "tool",
			"name":         "search",
			"tool_call_id": "call-1",
			"content":      "tool result",
		},
		map[string]any{"role": "user", "content": "latest user turn"},
	}
}

type streamStatusManagedAuthStub struct{}

func (streamStatusManagedAuthStub) Determine(_ *http.Request) (*auth.RequestAuth, error) {
	return &auth.RequestAuth{
		UseConfigToken: true,
		DeepSeekToken:  "managed-token",
		CallerID:       "caller:test",
		AccountID:      "acct:test",
		TriedAccounts:  map[string]bool{},
	}, nil
}

func (streamStatusManagedAuthStub) DetermineCaller(_ *http.Request) (*auth.RequestAuth, error) {
	return (&streamStatusManagedAuthStub{}).Determine(nil)
}

func (s streamStatusManagedAuthStub) DetermineWithSession(req *http.Request, _ []byte) (*auth.RequestAuth, error) {
	return s.Determine(req)
}

func (streamStatusManagedAuthStub) Release(_ *auth.RequestAuth) {}

func TestBuildOpenAICurrentInputContextTranscriptUsesPlainTranscript(t *testing.T) {
	_, historyMessages := splitOpenAIHistoryMessages(historySplitTestMessages(), 1)
	transcript := buildOpenAICurrentInputContextTranscript(historyMessages)

	if strings.Contains(transcript, "[file content end]") || strings.Contains(transcript, "[file name]:") || strings.Contains(transcript, "[file content begin]") {
		t.Fatalf("expected plain transcript without injected file wrapper, got %q", transcript)
	}
	if !strings.Contains(transcript, "# DEEPSEEK_WEB_TO_API_HISTORY.txt") {
		t.Fatalf("expected history transcript header, got %q", transcript)
	}
	if !strings.Contains(transcript, "Prior conversation history and tool progress.") {
		t.Fatalf("expected history transcript description, got %q", transcript)
	}
	if !strings.Contains(transcript, "[reasoning_content]") || !strings.Contains(transcript, "hidden reasoning") {
		t.Fatalf("expected reasoning block preserved, got %q", transcript)
	}
	if !strings.Contains(transcript, "<|DSML|tool_calls>") {
		t.Fatalf("expected tool calls preserved, got %q", transcript)
	}
}

func TestSplitOpenAIHistoryMessagesUsesLatestUserTurn(t *testing.T) {
	messages := []any{
		map[string]any{"role": "system", "content": "system instructions"},
		map[string]any{"role": "user", "content": "first user turn"},
		map[string]any{"role": "assistant", "content": "first assistant turn"},
		map[string]any{"role": "user", "content": "middle user turn"},
		map[string]any{"role": "assistant", "content": "middle assistant turn"},
		map[string]any{"role": "user", "content": "latest user turn"},
	}

	promptMessages, historyMessages := splitOpenAIHistoryMessages(messages, 1)
	if len(promptMessages) == 0 || len(historyMessages) == 0 {
		t.Fatalf("expected both prompt and history messages, got prompt=%d history=%d", len(promptMessages), len(historyMessages))
	}

	promptText, _ := promptcompat.BuildOpenAIPrompt(promptMessages, nil, "", defaultToolChoicePolicy(), true)
	if !strings.Contains(promptText, "latest user turn") {
		t.Fatalf("expected latest user turn in prompt, got %s", promptText)
	}
	if strings.Contains(promptText, "middle user turn") {
		t.Fatalf("expected middle user turn to be moved into history, got %s", promptText)
	}

	historyText := buildOpenAICurrentInputContextTranscript(historyMessages)
	if !strings.Contains(historyText, "middle user turn") {
		t.Fatalf("expected middle user turn in split history, got %s", historyText)
	}
	if strings.Contains(historyText, "latest user turn") {
		t.Fatalf("expected latest user turn to remain live, got %s", historyText)
	}
}

func TestApplyCurrentInputFileSkipsShortInputWhenThresholdNotReached(t *testing.T) {
	ds := &inlineUploadDSStub{}
	h := &openAITestSurface{
		Store: mockOpenAIConfig{
			wideInput:           true,
			currentInputEnabled: true,
			currentInputMin:     10,
		},
		DS: ds,
	}
	req := map[string]any{
		"model": "deepseek-v4-flash",
		"messages": []any{
			map[string]any{"role": "user", "content": "hello"},
		},
	}
	stdReq, err := promptcompat.NormalizeOpenAIChatRequest(h.Store, req, "")
	if err != nil {
		t.Fatalf("normalize failed: %v", err)
	}

	out, err := h.applyCurrentInputFile(context.Background(), &auth.RequestAuth{DeepSeekToken: "token"}, stdReq)
	if err != nil {
		t.Fatalf("apply current input file failed: %v", err)
	}
	if len(ds.uploadCalls) != 0 {
		t.Fatalf("expected no upload on first turn, got %d", len(ds.uploadCalls))
	}
	if out.FinalPrompt != stdReq.FinalPrompt {
		t.Fatalf("expected prompt unchanged on first turn")
	}
}

func TestApplyThinkingInjectionAppendsLatestUserPrompt(t *testing.T) {
	ds := &inlineUploadDSStub{}
	h := &openAITestSurface{
		Store: mockOpenAIConfig{
			wideInput:         true,
			thinkingInjection: boolPtr(true),
		},
		DS: ds,
	}
	req := map[string]any{
		"model": "deepseek-v4-flash",
		"messages": []any{
			map[string]any{"role": "user", "content": "hello"},
		},
	}
	stdReq, err := promptcompat.NormalizeOpenAIChatRequest(h.Store, req, "")
	if err != nil {
		t.Fatalf("normalize failed: %v", err)
	}

	out, err := h.applyCurrentInputFile(context.Background(), &auth.RequestAuth{DeepSeekToken: "token"}, stdReq)
	if err != nil {
		t.Fatalf("apply thinking injection failed: %v", err)
	}
	if len(ds.uploadCalls) != 0 {
		t.Fatalf("expected no upload for first short turn, got %d", len(ds.uploadCalls))
	}
	if !strings.Contains(out.FinalPrompt, "hello\n\n"+promptcompat.ThinkingInjectionMarker) {
		t.Fatalf("expected thinking injection after latest user message, got %s", out.FinalPrompt)
	}
}

func TestApplyThinkingInjectionUsesCustomPrompt(t *testing.T) {
	ds := &inlineUploadDSStub{}
	h := &openAITestSurface{
		Store: mockOpenAIConfig{
			wideInput:         true,
			thinkingInjection: boolPtr(true),
			thinkingPrompt:    "custom thinking format",
		},
		DS: ds,
	}
	req := map[string]any{
		"model": "deepseek-v4-flash",
		"messages": []any{
			map[string]any{"role": "user", "content": "hello"},
		},
	}
	stdReq, err := promptcompat.NormalizeOpenAIChatRequest(h.Store, req, "")
	if err != nil {
		t.Fatalf("normalize failed: %v", err)
	}

	out, err := h.applyCurrentInputFile(context.Background(), &auth.RequestAuth{DeepSeekToken: "token"}, stdReq)
	if err != nil {
		t.Fatalf("apply thinking injection failed: %v", err)
	}
	if !strings.Contains(out.FinalPrompt, "hello\n\ncustom thinking format") {
		t.Fatalf("expected custom thinking injection after latest user message, got %s", out.FinalPrompt)
	}
}

func TestApplyCurrentInputFileDisabledPassThrough(t *testing.T) {
	ds := &inlineUploadDSStub{}
	h := &openAITestSurface{
		Store: mockOpenAIConfig{
			wideInput:           true,
			currentInputEnabled: false,
		},
		DS: ds,
	}
	req := map[string]any{
		"model":    "deepseek-v4-vision",
		"messages": historySplitTestMessages(),
	}
	stdReq, err := promptcompat.NormalizeOpenAIChatRequest(h.Store, req, "")
	if err != nil {
		t.Fatalf("normalize failed: %v", err)
	}

	out, err := h.applyCurrentInputFile(context.Background(), &auth.RequestAuth{DeepSeekToken: "token"}, stdReq)
	if err != nil {
		t.Fatalf("apply current input file failed: %v", err)
	}
	if len(ds.uploadCalls) != 0 {
		t.Fatalf("expected no uploads when both split modes are disabled, got %d", len(ds.uploadCalls))
	}
	if out.CurrentInputFileApplied || out.HistoryText != "" {
		t.Fatalf("expected direct pass-through, got current_input=%v history=%q", out.CurrentInputFileApplied, out.HistoryText)
	}
	if !strings.Contains(out.FinalPrompt, "first user turn") || !strings.Contains(out.FinalPrompt, "latest user turn") {
		t.Fatalf("expected original prompt context to stay inline, got %s", out.FinalPrompt)
	}
}

func TestApplyCurrentInputFileUploadsFirstTurnAsPlainTranscript(t *testing.T) {
	ds := &inlineUploadDSStub{}
	h := &openAITestSurface{
		Store: mockOpenAIConfig{
			wideInput:           true,
			currentInputEnabled: true,
			currentInputMin:     10,
			thinkingInjection:   boolPtr(true),
		},
		DS: ds,
	}
	req := map[string]any{
		"model": "deepseek-v4-flash",
		"messages": []any{
			map[string]any{"role": "user", "content": "first turn content that is long enough"},
		},
	}
	stdReq, err := promptcompat.NormalizeOpenAIChatRequest(h.Store, req, "")
	if err != nil {
		t.Fatalf("normalize failed: %v", err)
	}

	out, err := h.applyCurrentInputFile(context.Background(), &auth.RequestAuth{DeepSeekToken: "token"}, stdReq)
	if err != nil {
		t.Fatalf("apply current input file failed: %v", err)
	}
	if len(ds.uploadCalls) != 1 {
		t.Fatalf("expected 1 current input upload, got %d", len(ds.uploadCalls))
	}
	upload := ds.uploadCalls[0]
	if upload.Filename != "DEEPSEEK_WEB_TO_API_HISTORY.txt" {
		t.Fatalf("unexpected upload filename: %q", upload.Filename)
	}
	uploadedText := string(upload.Data)
	if strings.Contains(uploadedText, "[file content end]") || strings.Contains(uploadedText, "[file name]:") || strings.Contains(uploadedText, "[file content begin]") {
		t.Fatalf("expected plain transcript without injected file wrapper, got %q", uploadedText)
	}
	for _, want := range []string{
		"# DEEPSEEK_WEB_TO_API_HISTORY.txt",
		"=== 1. USER ===",
		"first turn content that is long enough",
	} {
		if !strings.Contains(uploadedText, want) {
			t.Fatalf("expected uploaded transcript to contain %q, got %q", want, uploadedText)
		}
	}
	if !strings.Contains(uploadedText, promptcompat.ThinkingInjectionMarker) {
		t.Fatalf("expected thinking injection in current input file, got %q", uploadedText)
	}
	if strings.Contains(out.FinalPrompt, "first turn content that is long enough") {
		t.Fatalf("expected current input text to be replaced in live prompt, got %s", out.FinalPrompt)
	}
	if strings.Contains(out.FinalPrompt, "CURRENT_USER_INPUT.txt") || strings.Contains(out.FinalPrompt, "Read that file") {
		t.Fatalf("expected live prompt not to instruct file reads, got %s", out.FinalPrompt)
	}
	if !strings.Contains(out.FinalPrompt, "Continue from the latest state in the attached DEEPSEEK_WEB_TO_API_HISTORY.txt context.") {
		t.Fatalf("expected continuation-oriented prompt in live prompt, got %s", out.FinalPrompt)
	}
	if len(out.RefFileIDs) != 1 || out.RefFileIDs[0] != "file-inline-1" {
		t.Fatalf("expected current input file id in ref_file_ids, got %#v", out.RefFileIDs)
	}
	if !strings.Contains(out.PromptTokenText, "first turn content that is long enough") {
		t.Fatalf("expected prompt token text to preserve original full context, got %q", out.PromptTokenText)
	}
	if !strings.Contains(out.PromptTokenText, "# DEEPSEEK_WEB_TO_API_HISTORY.txt") || !strings.Contains(out.PromptTokenText, "=== 1. USER ===") {
		t.Fatalf("expected prompt token text to include numbered history transcript, got %q", out.PromptTokenText)
	}
}

func TestApplyCurrentInputFileTriggersOnLargeAccumulatedTranscript(t *testing.T) {
	ds := &inlineUploadDSStub{}
	h := &openAITestSurface{
		Store: mockOpenAIConfig{
			wideInput:           true,
			currentInputEnabled: true,
			currentInputMin:     80,
			thinkingInjection:   boolPtr(true),
		},
		DS: ds,
	}
	req := map[string]any{
		"model": "deepseek-v4-pro",
		"messages": []any{
			map[string]any{"role": "system", "content": strings.Repeat("large prior context ", 8)},
			map[string]any{"role": "user", "content": "short latest"},
		},
	}
	stdReq, err := promptcompat.NormalizeOpenAIChatRequest(h.Store, req, "")
	if err != nil {
		t.Fatalf("normalize failed: %v", err)
	}

	out, err := h.applyCurrentInputFile(context.Background(), &auth.RequestAuth{DeepSeekToken: "token"}, stdReq)
	if err != nil {
		t.Fatalf("apply current input file failed: %v", err)
	}
	if !out.CurrentInputFileApplied {
		t.Fatal("expected current input file to apply for large accumulated transcript")
	}
	if len(ds.uploadCalls) != 1 {
		t.Fatalf("expected one current input upload, got %d", len(ds.uploadCalls))
	}
	if strings.Contains(out.FinalPrompt, "large prior context") || strings.Contains(out.FinalPrompt, "short latest") {
		t.Fatalf("expected large transcript to be removed from live prompt, got %q", out.FinalPrompt)
	}
	if uploaded := string(ds.uploadCalls[0].Data); !strings.Contains(uploaded, "large prior context") || !strings.Contains(uploaded, "short latest") {
		t.Fatalf("expected upload to contain full transcript, got %q", uploaded)
	}
}

func TestApplyCurrentInputFilePreservesFullContextPromptForTokenCounting(t *testing.T) {
	ds := &inlineUploadDSStub{}
	h := &openAITestSurface{
		Store: mockOpenAIConfig{
			wideInput:           true,
			currentInputEnabled: true,
			currentInputMin:     0,
			thinkingInjection:   boolPtr(true),
		},
		DS: ds,
	}
	req := map[string]any{
		"model":    "deepseek-v4-vision",
		"messages": historySplitTestMessages(),
	}
	stdReq, err := promptcompat.NormalizeOpenAIChatRequest(h.Store, req, "")
	if err != nil {
		t.Fatalf("normalize failed: %v", err)
	}

	out, err := h.applyCurrentInputFile(context.Background(), &auth.RequestAuth{DeepSeekToken: "token"}, stdReq)
	if err != nil {
		t.Fatalf("apply current input file failed: %v", err)
	}
	if out.FinalPrompt == stdReq.FinalPrompt {
		t.Fatalf("expected live prompt to be rewritten after current input file")
	}
	// PromptTokenText must include the uploaded file content (which contains the full context)
	// plus the neutral live prompt — reflecting the actual downstream token cost.
	if !strings.Contains(out.PromptTokenText, "first user turn") || !strings.Contains(out.PromptTokenText, "latest user turn") {
		t.Fatalf("expected prompt token text to contain file context with full conversation, got %q", out.PromptTokenText)
	}
	if strings.Contains(out.PromptTokenText, "[file content end]") || strings.Contains(out.PromptTokenText, "[file name]:") {
		t.Fatalf("expected prompt token text to omit file wrapper tags, got %q", out.PromptTokenText)
	}
	if !strings.Contains(out.PromptTokenText, "# DEEPSEEK_WEB_TO_API_HISTORY.txt") || !strings.Contains(out.PromptTokenText, "=== 1. SYSTEM ===") {
		t.Fatalf("expected prompt token text to include numbered history transcript, got %q", out.PromptTokenText)
	}
	if !strings.Contains(out.PromptTokenText, "Continue from the latest state in the attached DEEPSEEK_WEB_TO_API_HISTORY.txt context.") {
		t.Fatalf("expected prompt token text to also include continuation prompt, got %q", out.PromptTokenText)
	}
	if strings.Contains(out.FinalPrompt, "first user turn") || strings.Contains(out.FinalPrompt, "latest user turn") {
		t.Fatalf("expected live prompt to hide original turns, got %q", out.FinalPrompt)
	}
}

func TestApplyCurrentInputFileUploadsFullContextFile(t *testing.T) {
	ds := &inlineUploadDSStub{}
	h := &openAITestSurface{
		Store: mockOpenAIConfig{
			wideInput:           true,
			currentInputEnabled: true,
			currentInputMin:     0,
			thinkingInjection:   boolPtr(true),
		},
		DS: ds,
	}
	req := map[string]any{
		"model":    "deepseek-v4-vision",
		"messages": historySplitTestMessages(),
	}
	stdReq, err := promptcompat.NormalizeOpenAIChatRequest(h.Store, req, "")
	if err != nil {
		t.Fatalf("normalize failed: %v", err)
	}

	out, err := h.applyCurrentInputFile(context.Background(), &auth.RequestAuth{DeepSeekToken: "token"}, stdReq)
	if err != nil {
		t.Fatalf("apply current input file failed: %v", err)
	}
	if !out.CurrentInputFileApplied {
		t.Fatalf("expected current input file to apply")
	}
	if len(ds.uploadCalls) != 1 {
		t.Fatalf("expected one current input upload, got %d", len(ds.uploadCalls))
	}
	upload := ds.uploadCalls[0]
	if upload.Filename != "DEEPSEEK_WEB_TO_API_HISTORY.txt" {
		t.Fatalf("expected DEEPSEEK_WEB_TO_API_HISTORY.txt upload, got %q", upload.Filename)
	}
	if upload.ModelType != "vision" {
		t.Fatalf("expected vision model type for vision request, got %q", upload.ModelType)
	}
	uploadedText := string(upload.Data)
	for _, want := range []string{"# DEEPSEEK_WEB_TO_API_HISTORY.txt", "=== 1. SYSTEM ===", "=== 2. USER ===", "=== 3. ASSISTANT ===", "=== 4. TOOL ===", "=== 5. USER ===", "system instructions", "first user turn", "hidden reasoning", "tool result", "latest user turn", promptcompat.ThinkingInjectionMarker} {
		if !strings.Contains(uploadedText, want) {
			t.Fatalf("expected full context file to contain %q, got %q", want, uploadedText)
		}
	}
	if strings.Contains(out.FinalPrompt, "first user turn") || strings.Contains(out.FinalPrompt, "latest user turn") || strings.Contains(out.FinalPrompt, "CURRENT_USER_INPUT.txt") || strings.Contains(out.FinalPrompt, "Read that file") {
		t.Fatalf("expected live prompt to use only a continuation instruction, got %s", out.FinalPrompt)
	}
	if !strings.Contains(out.FinalPrompt, "Continue from the latest state in the attached DEEPSEEK_WEB_TO_API_HISTORY.txt context.") {
		t.Fatalf("expected continuation-oriented prompt in live prompt, got %s", out.FinalPrompt)
	}
}

func TestApplyCurrentInputFileCarriesHistoryText(t *testing.T) {
	ds := &inlineUploadDSStub{}
	h := &openAITestSurface{
		Store: mockOpenAIConfig{
			wideInput:           true,
			currentInputEnabled: true,
		},
		DS: ds,
	}
	req := map[string]any{
		"model":    "deepseek-v4-flash",
		"messages": historySplitTestMessages(),
	}
	stdReq, err := promptcompat.NormalizeOpenAIChatRequest(h.Store, req, "")
	if err != nil {
		t.Fatalf("normalize failed: %v", err)
	}

	out, err := h.applyCurrentInputFile(context.Background(), &auth.RequestAuth{DeepSeekToken: "token"}, stdReq)
	if err != nil {
		t.Fatalf("apply current input file failed: %v", err)
	}
	if len(ds.uploadCalls) != 1 {
		t.Fatalf("expected 1 upload call, got %d", len(ds.uploadCalls))
	}
	if out.HistoryText != string(ds.uploadCalls[0].Data) {
		t.Fatalf("expected current input file flow to preserve uploaded text in history, got %q", out.HistoryText)
	}
	if !strings.Contains(out.HistoryText, "# DEEPSEEK_WEB_TO_API_HISTORY.txt") || !strings.Contains(out.HistoryText, "=== 1. SYSTEM ===") {
		t.Fatalf("expected history text to use numbered transcript format, got %q", out.HistoryText)
	}
}

func TestChatCompletionsCurrentInputFileUploadsContextAndKeepsNeutralPrompt(t *testing.T) {
	ds := &inlineUploadDSStub{}
	h := &openAITestSurface{
		Store: mockOpenAIConfig{
			wideInput:           true,
			currentInputEnabled: true,
		},
		Auth: streamStatusAuthStub{},
		DS:   ds,
	}
	reqBody, _ := json.Marshal(map[string]any{
		"model":    "deepseek-v4-flash",
		"messages": historySplitTestMessages(),
		"stream":   false,
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(reqBody)))
	req.Header.Set("Authorization", "Bearer direct-token")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.ChatCompletions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if len(ds.uploadCalls) != 1 {
		t.Fatalf("expected 1 upload call, got %d", len(ds.uploadCalls))
	}
	upload := ds.uploadCalls[0]
	if upload.Filename != "DEEPSEEK_WEB_TO_API_HISTORY.txt" {
		t.Fatalf("unexpected upload filename: %q", upload.Filename)
	}
	if upload.Purpose != "assistants" {
		t.Fatalf("unexpected purpose: %q", upload.Purpose)
	}
	historyText := string(upload.Data)
	if strings.Contains(historyText, "[file content end]") || strings.Contains(historyText, "[file name]:") || strings.Contains(historyText, "[file content begin]") {
		t.Fatalf("expected plain transcript without injected IGNORE wrapper, got %s", historyText)
	}
	if !strings.Contains(historyText, "latest user turn") {
		t.Fatalf("expected full context to include latest turn, got %s", historyText)
	}
	if ds.completionReq == nil {
		t.Fatal("expected completion payload to be captured")
	}
	promptText, _ := ds.completionReq["prompt"].(string)
	if !strings.Contains(promptText, "Continue from the latest state in the attached DEEPSEEK_WEB_TO_API_HISTORY.txt context.") {
		t.Fatalf("expected continuation-oriented prompt, got %s", promptText)
	}
	if strings.Contains(promptText, "first user turn") || strings.Contains(promptText, "latest user turn") {
		t.Fatalf("expected prompt to hide original turns, got %s", promptText)
	}
	refIDs, _ := ds.completionReq["ref_file_ids"].([]any)
	if len(refIDs) == 0 || refIDs[0] != "file-inline-1" {
		t.Fatalf("expected uploaded current input file to be first ref_file_id, got %#v", ds.completionReq["ref_file_ids"])
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response failed: %v", err)
	}
	usage, _ := body["usage"].(map[string]any)
	promptTokens := int(usage["prompt_tokens"].(float64))
	neutralCount := util.CountPromptTokens(promptText, "deepseek-v4-flash")
	if promptTokens <= neutralCount {
		t.Fatalf("expected prompt_tokens to exceed neutral live prompt count (includes file context), got=%d neutral=%d", promptTokens, neutralCount)
	}
}

func TestChatCompletionsSkippedCurrentInputFileKeepsLivePrompt(t *testing.T) {
	ds := &inlineUploadDSStub{}
	h := &openAITestSurface{
		Store: mockOpenAIConfig{
			wideInput:           true,
			currentInputEnabled: true,
		},
		Auth: streamStatusAuthStub{},
		DS:   ds,
	}
	reqBody, _ := json.Marshal(map[string]any{
		"model":    "deepseek-v4-flash",
		"messages": historySplitTestMessages(),
		"stream":   false,
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(reqBody)))
	req = req.WithContext(openaishared.WithCurrentInputFileSkipped(req.Context()))
	req.Header.Set("Authorization", "Bearer direct-token")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.ChatCompletions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if len(ds.uploadCalls) != 0 {
		t.Fatalf("expected no current input upload, got %d", len(ds.uploadCalls))
	}
	if ds.completionReq == nil {
		t.Fatal("expected completion payload to be captured")
	}
	promptText, _ := ds.completionReq["prompt"].(string)
	if !strings.Contains(promptText, "latest user turn") || !strings.Contains(promptText, "tool result") {
		t.Fatalf("expected live prompt to keep original conversation context, got %s", promptText)
	}
	if strings.Contains(promptText, "Answer the latest user request directly.") {
		t.Fatalf("expected live prompt to avoid neutral current input prompt, got %s", promptText)
	}
	if refIDs, ok := ds.completionReq["ref_file_ids"].([]any); ok && len(refIDs) > 0 {
		t.Fatalf("expected no current input file ref, got %#v", ds.completionReq["ref_file_ids"])
	}
}

func TestResponsesCurrentInputFileUploadsContextAndKeepsNeutralPrompt(t *testing.T) {
	ds := &inlineUploadDSStub{}
	h := &openAITestSurface{
		Store: mockOpenAIConfig{
			wideInput:           true,
			currentInputEnabled: true,
		},
		Auth: streamStatusAuthStub{},
		DS:   ds,
	}
	r := chi.NewRouter()
	registerOpenAITestRoutes(r, h)
	reqBody, _ := json.Marshal(map[string]any{
		"model":    "deepseek-v4-flash",
		"messages": historySplitTestMessages(),
		"stream":   false,
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(string(reqBody)))
	req.Header.Set("Authorization", "Bearer direct-token")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if len(ds.uploadCalls) != 1 {
		t.Fatalf("expected 1 upload call, got %d", len(ds.uploadCalls))
	}
	historyText := string(ds.uploadCalls[0].Data)
	if !strings.Contains(historyText, "# DEEPSEEK_WEB_TO_API_HISTORY.txt") || !strings.Contains(historyText, "=== 1. SYSTEM ===") {
		t.Fatalf("expected uploaded history text to use numbered transcript format, got %s", historyText)
	}
	if ds.completionReq == nil {
		t.Fatal("expected completion payload to be captured")
	}
	promptText, _ := ds.completionReq["prompt"].(string)
	if !strings.Contains(promptText, "Continue from the latest state in the attached DEEPSEEK_WEB_TO_API_HISTORY.txt context.") {
		t.Fatalf("expected continuation-oriented prompt, got %s", promptText)
	}
	if strings.Contains(promptText, "first user turn") || strings.Contains(promptText, "latest user turn") {
		t.Fatalf("expected prompt to hide original turns, got %s", promptText)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response failed: %v", err)
	}
	usage, _ := body["usage"].(map[string]any)
	inputTokens := int(usage["input_tokens"].(float64))
	neutralCount := util.CountPromptTokens(promptText, "deepseek-v4-flash")
	if inputTokens <= neutralCount {
		t.Fatalf("expected input_tokens to exceed neutral live prompt count (includes file context), got=%d neutral=%d", inputTokens, neutralCount)
	}
}

func TestChatCompletionsCurrentInputFileMapsManagedAuthFailureTo401(t *testing.T) {
	ds := &inlineUploadDSStub{
		uploadErr: &dsclient.RequestFailure{Op: "upload file", Kind: dsclient.FailureManagedUnauthorized, Message: "expired token"},
	}
	h := &openAITestSurface{
		Store: mockOpenAIConfig{
			wideInput:           true,
			currentInputEnabled: true,
		},
		Auth: streamStatusManagedAuthStub{},
		DS:   ds,
	}
	reqBody, _ := json.Marshal(map[string]any{
		"model":    "deepseek-v4-flash",
		"messages": historySplitTestMessages(),
		"stream":   false,
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(reqBody)))
	req.Header.Set("Authorization", "Bearer managed-key")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.ChatCompletions(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Please re-login the account in admin") {
		t.Fatalf("expected managed auth error message, got %s", rec.Body.String())
	}
}

func TestResponsesCurrentInputFileMapsDirectAuthFailureTo401(t *testing.T) {
	ds := &inlineUploadDSStub{
		uploadErr: &dsclient.RequestFailure{Op: "upload file", Kind: dsclient.FailureDirectUnauthorized, Message: "invalid token"},
	}
	h := &openAITestSurface{
		Store: mockOpenAIConfig{
			wideInput:           true,
			currentInputEnabled: true,
		},
		Auth: streamStatusAuthStub{},
		DS:   ds,
	}
	r := chi.NewRouter()
	registerOpenAITestRoutes(r, h)
	reqBody, _ := json.Marshal(map[string]any{
		"model":    "deepseek-v4-flash",
		"messages": historySplitTestMessages(),
		"stream":   false,
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(string(reqBody)))
	req.Header.Set("Authorization", "Bearer direct-token")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Invalid token") {
		t.Fatalf("expected direct auth error message, got %s", rec.Body.String())
	}
}

func TestChatCompletionsCurrentInputFileUploadFailureReturnsInternalServerError(t *testing.T) {
	ds := &inlineUploadDSStub{uploadErr: errors.New("boom")}
	h := &openAITestSurface{
		Store: mockOpenAIConfig{
			wideInput:           true,
			currentInputEnabled: true,
		},
		Auth: streamStatusAuthStub{},
		DS:   ds,
	}
	reqBody, _ := json.Marshal(map[string]any{
		"model":    "deepseek-v4-flash",
		"messages": historySplitTestMessages(),
		"stream":   false,
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(reqBody)))
	req.Header.Set("Authorization", "Bearer direct-token")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.ChatCompletions(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCurrentInputFileWorksAcrossAutoDeleteModes(t *testing.T) {
	for _, mode := range []string{"none", "single", "all"} {
		t.Run(mode, func(t *testing.T) {
			ds := &inlineUploadDSStub{}
			h := &openAITestSurface{
				Store: mockOpenAIConfig{
					wideInput:           true,
					autoDeleteMode:      mode,
					currentInputEnabled: true,
				},
				Auth: streamStatusAuthStub{},
				DS:   ds,
			}
			reqBody, _ := json.Marshal(map[string]any{
				"model":    "deepseek-v4-flash",
				"messages": historySplitTestMessages(),
				"stream":   false,
			})
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(reqBody)))
			req.Header.Set("Authorization", "Bearer direct-token")
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()

			h.ChatCompletions(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
			}
			if len(ds.uploadCalls) != 1 {
				t.Fatalf("expected current input upload for mode=%s, got %d", mode, len(ds.uploadCalls))
			}
			historyText := string(ds.uploadCalls[0].Data)
			if !strings.Contains(historyText, "# DEEPSEEK_WEB_TO_API_HISTORY.txt") || !strings.Contains(historyText, "=== 1. SYSTEM ===") {
				t.Fatalf("expected uploaded history text to use numbered transcript format, got %s", historyText)
			}
			if ds.completionReq == nil {
				t.Fatalf("expected completion payload for mode=%s", mode)
			}
			promptText, _ := ds.completionReq["prompt"].(string)
			if !strings.Contains(promptText, "Continue from the latest state in the attached DEEPSEEK_WEB_TO_API_HISTORY.txt context.") || strings.Contains(promptText, "first user turn") || strings.Contains(promptText, "latest user turn") {
				t.Fatalf("unexpected prompt for mode=%s: %s", mode, promptText)
			}
		})
	}
}

func defaultToolChoicePolicy() promptcompat.ToolChoicePolicy {
	return promptcompat.DefaultToolChoicePolicy()
}

func boolPtr(v bool) *bool {
	return &v
}

func TestApplyCurrentInputFileReusesStablePrefixForRollingTail(t *testing.T) {
	ds := &inlineUploadDSStub{}
	h := &openAITestSurface{
		Store: mockOpenAIConfig{
			wideInput:           true,
			currentInputEnabled: true,
			currentInputMin:     80,
		},
		DS: ds,
	}
	a := &auth.RequestAuth{
		DeepSeekToken: "token",
		CallerID:      "caller:p3c-test",
		AccountID:     "acct:p3c-test",
		SessionKey:    "session:p3c-test",
	}
	stablePrefix := strings.Repeat("stable prefix context ", 4200)
	firstMessages := []any{
		map[string]any{"role": "system", "content": stablePrefix},
		map[string]any{"role": "user", "content": "first latest request"},
	}
	secondMessages := []any{
		map[string]any{"role": "system", "content": stablePrefix},
		map[string]any{"role": "user", "content": "first latest request"},
		map[string]any{"role": "assistant", "content": "first answer"},
		map[string]any{"role": "user", "content": "second latest request"},
	}

	firstReq := map[string]any{"model": "deepseek-v4-pro-nothinking", "messages": firstMessages}
	firstStdReq, err := promptcompat.NormalizeOpenAIChatRequest(h.Store, firstReq, "")
	if err != nil {
		t.Fatalf("normalize first request failed: %v", err)
	}
	firstOut, err := h.applyCurrentInputFile(context.Background(), a, firstStdReq)
	if err != nil {
		t.Fatalf("apply first current input file failed: %v", err)
	}
	if !firstOut.CurrentInputFileApplied || !firstOut.CurrentInputCheckpointRefresh || firstOut.CurrentInputPrefixReused {
		t.Fatalf("expected first request to create a prefix checkpoint, got applied=%v refresh=%v reused=%v", firstOut.CurrentInputFileApplied, firstOut.CurrentInputCheckpointRefresh, firstOut.CurrentInputPrefixReused)
	}
	if firstOut.CurrentInputPrefixHash == "" || firstOut.CurrentInputPrefixChars == 0 || firstOut.CurrentInputTailChars == 0 {
		t.Fatalf("expected first prefix metrics, got hash=%q prefix=%d tail=%d", firstOut.CurrentInputPrefixHash, firstOut.CurrentInputPrefixChars, firstOut.CurrentInputTailChars)
	}
	if len(ds.uploadCalls) != 1 {
		t.Fatalf("expected one prefix upload on first request, got %d", len(ds.uploadCalls))
	}
	firstUpload := string(ds.uploadCalls[0].Data)
	if !strings.Contains(firstUpload, "stable prefix context") {
		t.Fatalf("expected upload to contain stable prefix, got %q", firstUpload[:min(len(firstUpload), 200)])
	}
	if strings.Contains(firstUpload, "first latest request") {
		t.Fatalf("expected first latest request to stay in live tail, not prefix upload")
	}
	if !strings.Contains(firstOut.FinalPrompt, "first latest request") || strings.Contains(firstOut.FinalPrompt, "stable prefix context") {
		t.Fatalf("expected live prompt to contain tail but not stable prefix, got %q", firstOut.FinalPrompt[:min(len(firstOut.FinalPrompt), 500)])
	}

	secondReq := map[string]any{"model": "deepseek-v4-pro-nothinking", "messages": secondMessages}
	secondStdReq, err := promptcompat.NormalizeOpenAIChatRequest(h.Store, secondReq, "")
	if err != nil {
		t.Fatalf("normalize second request failed: %v", err)
	}
	secondOut, err := h.applyCurrentInputFile(context.Background(), a, secondStdReq)
	if err != nil {
		t.Fatalf("apply second current input file failed: %v", err)
	}
	if !secondOut.CurrentInputPrefixReused || secondOut.CurrentInputCheckpointRefresh {
		t.Fatalf("expected second request to reuse prefix without refresh, got reused=%v refresh=%v", secondOut.CurrentInputPrefixReused, secondOut.CurrentInputCheckpointRefresh)
	}
	if secondOut.CurrentInputPrefixHash != firstOut.CurrentInputPrefixHash {
		t.Fatalf("expected stable prefix hash reuse, first=%q second=%q", firstOut.CurrentInputPrefixHash, secondOut.CurrentInputPrefixHash)
	}
	if len(ds.uploadCalls) != 1 {
		t.Fatalf("expected no second upload when prefix is reused, got %d uploads", len(ds.uploadCalls))
	}
	if len(secondOut.RefFileIDs) != 1 || secondOut.RefFileIDs[0] != "file-inline-1" {
		t.Fatalf("expected reused file id in ref_file_ids, got %#v", secondOut.RefFileIDs)
	}
	if !strings.Contains(secondOut.FinalPrompt, "first latest request") || !strings.Contains(secondOut.FinalPrompt, "second latest request") {
		t.Fatalf("expected rolling tail in live prompt, got %q", secondOut.FinalPrompt)
	}
	if strings.Contains(secondOut.FinalPrompt, "stable prefix context") {
		t.Fatalf("expected stable prefix to remain in attached file only")
	}
}
