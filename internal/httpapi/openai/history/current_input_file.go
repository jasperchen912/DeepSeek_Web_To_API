package history

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"DeepSeek_Web_To_API/internal/auth"
	"DeepSeek_Web_To_API/internal/config"
	dsclient "DeepSeek_Web_To_API/internal/deepseek/client"
	"DeepSeek_Web_To_API/internal/httpapi/openai/shared"
	"DeepSeek_Web_To_API/internal/promptcompat"
	"DeepSeek_Web_To_API/internal/util"
)

const (
	currentInputFilename    = promptcompat.CurrentInputContextFilename
	currentInputContentType = "text/plain; charset=utf-8"
	currentInputPurpose     = "assistants"
)

func (s Service) ApplyCurrentInputFile(ctx context.Context, a *auth.RequestAuth, stdReq promptcompat.StandardRequest) (promptcompat.StandardRequest, error) {
	if s.DS == nil || s.Store == nil || a == nil || !s.Store.CurrentInputFileEnabled() {
		return stdReq, nil
	}
	threshold := s.Store.CurrentInputFileMinChars()

	index, latestUserText := latestUserInputForFile(stdReq.Messages)
	if index < 0 {
		return stdReq, nil
	}
	fileText := promptcompat.BuildOpenAICurrentInputContextTranscript(stdReq.Messages)
	if strings.TrimSpace(fileText) == "" {
		return stdReq, errors.New("current user input file produced empty transcript")
	}
	// Trigger on either a long latest user message or a large accumulated
	// prompt. Real OpenClaw turns often have a short latest user request plus
	// a very large prior context; keeping that history inline can make
	// DeepSeek Web return an empty stream for V4 Pro. Uploading the full
	// transcript as the current-input file keeps the live prompt small while
	// preserving context.
	largeAccumulatedPrompt := len(stdReq.Messages) > 1 && len([]rune(stdReq.FinalPrompt)) >= threshold
	if len([]rune(latestUserText)) < threshold && !largeAccumulatedPrompt {
		return stdReq, nil
	}
	modelType := "default"
	if resolvedType, ok := config.GetModelType(stdReq.ResolvedModel); ok {
		modelType = resolvedType
	}
	if out, ok, err := s.applyCurrentInputStablePrefix(ctx, a, stdReq, fileText, modelType); err != nil || ok {
		return out, err
	}
	return s.applyCurrentInputFullFile(ctx, a, stdReq, fileText, modelType)
}

func (s Service) applyCurrentInputFullFile(ctx context.Context, a *auth.RequestAuth, stdReq promptcompat.StandardRequest, fileText, modelType string) (promptcompat.StandardRequest, error) {
	fileID, err := s.uploadCurrentInputFile(ctx, a, fileText, modelType)
	if err != nil {
		return stdReq, err
	}

	messages := []any{
		map[string]any{
			"role":    "user",
			"content": currentInputFilePrompt(),
		},
	}

	stdReq.Messages = messages
	stdReq.HistoryText = fileText
	stdReq.CurrentInputFileApplied = true
	stdReq.CurrentInputPrefixHash = currentInputTextHash(fileText)
	stdReq.CurrentInputPrefixChars = len(fileText)
	stdReq.CurrentInputCheckpointRefresh = true
	stdReq.RefFileIDs = prependUniqueRefFileID(stdReq.RefFileIDs, fileID)
	stdReq.FinalPrompt, stdReq.ToolNames = promptcompat.BuildOpenAIPrompt(messages, stdReq.ToolsRaw, "", stdReq.ToolChoice, stdReq.Thinking)
	// Token accounting must reflect the actual downstream context:
	// the uploaded DEEPSEEK_WEB_TO_API_HISTORY.txt file content + the continuation live prompt.
	prefixTokens := util.CountPromptTokens(fileText, stdReq.ResponseModel)
	stdReq.PromptPrefixHash = stdReq.CurrentInputPrefixHash
	stdReq.PromptPrefixTokens = prefixTokens
	stdReq.PromptTailTokens = util.CountPromptTokens(stdReq.FinalPrompt, stdReq.ResponseModel)
	stdReq.PromptPrefixEligible = true
	stdReq.RefFileTokens += prefixTokens
	stdReq.PromptTokenText = fileText + "\n" + stdReq.FinalPrompt
	return stdReq, nil
}

func (s Service) uploadCurrentInputFile(ctx context.Context, a *auth.RequestAuth, fileText, modelType string) (string, error) {
	result, err := s.DS.UploadFile(ctx, a, dsclient.UploadFileRequest{
		Filename:    currentInputFilename,
		ContentType: currentInputContentType,
		Purpose:     currentInputPurpose,
		ModelType:   modelType,
		Data:        []byte(fileText),
	}, 3)
	if err != nil {
		return "", fmt.Errorf("upload current user input file: %w", err)
	}
	fileID := strings.TrimSpace(result.ID)
	if fileID == "" {
		return "", errors.New("upload current user input file returned empty file id")
	}
	return fileID, nil
}

func latestUserInputForFile(messages []any) (int, string) {
	for i := len(messages) - 1; i >= 0; i-- {
		msg, ok := messages[i].(map[string]any)
		if !ok {
			continue
		}
		role := strings.ToLower(strings.TrimSpace(shared.AsString(msg["role"])))
		if role != "user" {
			continue
		}
		text := promptcompat.NormalizeOpenAIContentForPrompt(msg["content"])
		if strings.TrimSpace(text) == "" {
			return -1, ""
		}
		return i, text
	}
	return -1, ""
}

func currentInputFilePrompt() string {
	return "Continue from the latest state in the attached DEEPSEEK_WEB_TO_API_HISTORY.txt context. Treat it as the current working state and answer the latest user request directly."
}
