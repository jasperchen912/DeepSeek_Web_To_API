package chat

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"DeepSeek_Web_To_API/internal/auth"
	"DeepSeek_Web_To_API/internal/config"
	"DeepSeek_Web_To_API/internal/currentinputmetrics"
	dsprotocol "DeepSeek_Web_To_API/internal/deepseek/protocol"
	openaifmt "DeepSeek_Web_To_API/internal/format/openai"
	"DeepSeek_Web_To_API/internal/httpapi/openai/shared"
	"DeepSeek_Web_To_API/internal/httpapi/requestbody"
	"DeepSeek_Web_To_API/internal/promptcompat"
	"DeepSeek_Web_To_API/internal/sse"
	streamengine "DeepSeek_Web_To_API/internal/stream"
)

func (h *Handler) ChatCompletions(w http.ResponseWriter, r *http.Request) {
	requestStartedAt := time.Now()
	traceID := requestTraceID(r)
	stageStartedAt := time.Now()

	// Read body first so we can compute a session-affinity key before
	// acquiring an account from the pool. Same Claude Code / Codex
	// conversation → same account, preserving upstream session context.
	r.Body = http.MaxBytesReader(w, r.Body, openAIGeneralMaxSize)
	rawBody, err := io.ReadAll(r.Body)
	readBodyDuration := time.Since(stageStartedAt)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "too large") {
			writeOpenAIError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		if errors.Is(err, requestbody.ErrInvalidUTF8Body) {
			writeOpenAIError(w, http.StatusBadRequest, "invalid json: invalid utf-8 request body")
			return
		}
		writeOpenAIError(w, http.StatusBadRequest, "invalid body")
		return
	}
	var req map[string]any
	if err := json.Unmarshal(rawBody, &req); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid json")
		return
	}

	stageStartedAt = time.Now()
	callerAuth, err := h.Auth.DetermineCaller(r)
	determineCallerDuration := time.Since(stageStartedAt)
	if err != nil {
		writeOpenAIError(w, http.StatusUnauthorized, err.Error())
		return
	}
	stageStartedAt = time.Now()
	historyStdReq, err := promptcompat.NormalizeOpenAIChatRequest(h.Store, req, traceID)
	historyNormalizeDuration := time.Since(stageStartedAt)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, err.Error())
		return
	}
	historyStdReq = shared.ApplyThinkingInjection(h.Store, historyStdReq)
	historySession := startQueuedChatHistory(h.ChatHistory, r, callerAuth, historyStdReq)

	stageStartedAt = time.Now()
	a, err := h.Auth.DetermineWithSession(r, rawBody)
	determineSessionDuration := time.Since(stageStartedAt)
	if err != nil {
		status := http.StatusUnauthorized
		detail := err.Error()
		if err == auth.ErrNoAccount {
			status = http.StatusTooManyRequests
		}
		if historySession != nil {
			historySession.error(status, detail, "error", "", "")
		}
		writeOpenAIError(w, status, detail)
		return
	}
	if historySession != nil {
		historySession.bindAuth(a)
	}
	var sessionID string
	defer func() {
		h.autoDeleteRemoteSession(r.Context(), a, sessionID)
		h.Auth.Release(a)
	}()

	r = r.WithContext(auth.WithAuth(r.Context(), a))
	stageStartedAt = time.Now()
	if err := h.preprocessInlineFileInputs(r.Context(), a, req); err != nil {
		if historySession != nil {
			historySession.error(http.StatusBadRequest, err.Error(), "error", "", "")
		}
		writeOpenAIInlineFileError(w, err)
		return
	}
	preprocessInlineFilesDuration := time.Since(stageStartedAt)
	stageStartedAt = time.Now()
	stdReq, err := promptcompat.NormalizeOpenAIChatRequest(h.Store, req, traceID)
	requestNormalizeDuration := time.Since(stageStartedAt)
	if err != nil {
		if historySession != nil {
			historySession.error(http.StatusBadRequest, err.Error(), "error", "", "")
		}
		writeOpenAIError(w, http.StatusBadRequest, err.Error())
		return
	}
	stdReq = shared.ApplyThinkingInjection(h.Store, stdReq)
	stageStartedAt = time.Now()
	stdReq, err = h.applyCurrentInputFile(r.Context(), a, stdReq)
	currentInputFileDuration := time.Since(stageStartedAt)
	if err != nil {
		status, message := mapCurrentInputFileError(err)
		if historySession != nil {
			historySession.error(status, message, "error", "", "")
		}
		writeOpenAIError(w, status, message)
		return
	}
	recordCurrentInputMetrics(stdReq, currentInputFileDuration)
	if historySession != nil {
		historySession.updateHistoryText(stdReq.HistoryText)
	}

	stageStartedAt = time.Now()
	sessionID, err = h.DS.CreateSession(r.Context(), a, 3)
	createSessionDuration := time.Since(stageStartedAt)
	if err != nil {
		sessionDetail := shared.SessionErrorDetail(err)
		if sessionDetail.Stopped || sessionDetail.Status == http.StatusGatewayTimeout {
			writeSessionCallError(w, historySession, err)
			return
		}
		if a.UseConfigToken {
			if historySession != nil {
				historySession.error(http.StatusUnauthorized, "Account token is invalid. Please re-login the account in admin.", "error", "", "")
			}
			writeOpenAIError(w, http.StatusUnauthorized, "Account token is invalid. Please re-login the account in admin.")
		} else {
			a.MarkDirectTokenInvalid()
			if historySession != nil {
				historySession.error(http.StatusUnauthorized, "Invalid token. If this should be a DeepSeek_Web_To_API key, add it to config.keys first.", "error", "", "")
			}
			writeOpenAIError(w, http.StatusUnauthorized, "Invalid token. If this should be a DeepSeek_Web_To_API key, add it to config.keys first.")
		}
		return
	}
	stageStartedAt = time.Now()
	pow, err := h.DS.GetPow(r.Context(), a, 3)
	getPowDuration := time.Since(stageStartedAt)
	if err != nil {
		powDetail := shared.PowErrorDetail(err)
		if powDetail.Stopped || powDetail.Status == http.StatusGatewayTimeout {
			writePowCallError(w, historySession, err)
			return
		}
		if !a.UseConfigToken {
			a.MarkDirectTokenInvalid()
		}
		if historySession != nil {
			historySession.error(http.StatusUnauthorized, "Failed to get PoW (invalid token or unknown error).", "error", "", "")
		}
		writeOpenAIError(w, http.StatusUnauthorized, "Failed to get PoW (invalid token or unknown error).")
		return
	}
	payload := stdReq.CompletionPayload(sessionID)
	stageStartedAt = time.Now()
	resp, err := h.DS.CallCompletion(r.Context(), a, payload, pow, 3)
	callCompletionOpenDuration := time.Since(stageStartedAt)
	if nextSessionID := strings.TrimSpace(asString(payload["chat_session_id"])); nextSessionID != "" {
		sessionID = nextSessionID
	}
	if err != nil {
		if !a.UseConfigToken && shared.CompletionErrorDetail(err).Status == http.StatusUnauthorized {
			a.MarkDirectTokenInvalid()
		}
		writeCompletionCallError(w, historySession, err, "", "")
		return
	}
	refFileTokens := stdReq.RefFileTokens
	config.Logger.Info("[openai_chat_timing] prepared",
		"trace_id", traceID,
		"model", stdReq.ResponseModel,
		"stream", stdReq.Stream,
		"thinking", stdReq.Thinking,
		"search", stdReq.Search,
		"account", authAccountID(a),
		"caller", a.CallerID,
		"session_key", shortTimingValue(a.SessionKey),
		"affinity_header", shortTimingValue(requestHeaderValue(r, auth.SessionAffinityHeader, auth.LegacySessionAffinityHeader, auth.OpenClawSessionIDHeader, auth.OpenClawClientRequestHeader, auth.OpenClawSessionAffinityHeader, auth.OpenClawLegacySessionHeader)),
		"raw_body_bytes", len(rawBody),
		"raw_body_hash", shortHashBytes(rawBody),
		"current_input_file", stdReq.CurrentInputFileApplied,
		"current_input_prefix_hash", stdReq.CurrentInputPrefixHash,
		"current_input_prefix_reused", stdReq.CurrentInputPrefixReused,
		"current_input_prefix_chars", stdReq.CurrentInputPrefixChars,
		"current_input_tail_chars", stdReq.CurrentInputTailChars,
		"current_input_tail_entries", stdReq.CurrentInputTailEntries,
		"current_input_checkpoint_refresh", stdReq.CurrentInputCheckpointRefresh,
		"messages_count", len(stdReq.Messages),
		"ref_file_count", len(stdReq.RefFileIDs),
		"history_chars", len(stdReq.HistoryText),
		"history_hash", shortHashString(stdReq.HistoryText),
		"final_prompt_chars", len(stdReq.FinalPrompt),
		"final_prompt_hash", shortHashString(stdReq.FinalPrompt),
		"prompt_token_text_chars", len(stdReq.PromptTokenText),
		"prompt_token_text_hash", shortHashString(stdReq.PromptTokenText),
		"ref_file_tokens", refFileTokens,
		"read_body_ms", durationMillis(readBodyDuration),
		"determine_caller_ms", durationMillis(determineCallerDuration),
		"history_normalize_ms", durationMillis(historyNormalizeDuration),
		"determine_session_ms", durationMillis(determineSessionDuration),
		"preprocess_inline_files_ms", durationMillis(preprocessInlineFilesDuration),
		"request_normalize_ms", durationMillis(requestNormalizeDuration),
		"current_input_file_ms", durationMillis(currentInputFileDuration),
		"create_session_ms", durationMillis(createSessionDuration),
		"get_pow_ms", durationMillis(getPowDuration),
		"call_completion_open_ms", durationMillis(callCompletionOpenDuration),
		"prepare_total_ms", durationMillis(time.Since(requestStartedAt)),
	)
	if stdReq.Stream {
		h.handleStreamWithRetry(w, r, a, resp, payload, pow, sessionID, stdReq.ResponseModel, stdReq.FinalPrompt, refFileTokens, stdReq.Thinking, stdReq.ExposeReasoning, stdReq.Search, stdReq.ToolNames, stdReq.ToolsRaw, stdReq.ToolChoice.IsRequired(), historySession, &sessionID, requestStartedAt, traceID)
		return
	}
	h.handleNonStreamWithRetry(w, r.Context(), a, resp, payload, pow, sessionID, stdReq.ResponseModel, stdReq.FinalPrompt, refFileTokens, stdReq.Thinking, stdReq.ExposeReasoning, stdReq.Search, stdReq.ToolNames, stdReq.ToolsRaw, stdReq.ToolChoice.IsRequired(), historySession, &sessionID)
}

func recordCurrentInputMetrics(stdReq promptcompat.StandardRequest, duration time.Duration) {
	currentinputmetrics.Record(currentinputmetrics.Sample{
		Applied:           stdReq.CurrentInputFileApplied,
		PrefixReused:      stdReq.CurrentInputPrefixReused,
		CheckpointRefresh: stdReq.CurrentInputCheckpointRefresh,
		PrefixHash:        stdReq.CurrentInputPrefixHash,
		PrefixChars:       stdReq.CurrentInputPrefixChars,
		TailChars:         stdReq.CurrentInputTailChars,
		TailEntries:       stdReq.CurrentInputTailEntries,
		DurationMs:        durationMillis(duration),
	})
}

func durationMillis(d time.Duration) int64 {
	if d <= 0 {
		return 0
	}
	return d.Milliseconds()
}

func shortTimingValue(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= 96 {
		return value
	}
	return value[:96]
}

func shortHashString(value string) string {
	if value == "" {
		return ""
	}
	return shortHashBytes([]byte(value))
}

func shortHashBytes(value []byte) string {
	if len(value) == 0 {
		return ""
	}
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])[:16]
}

func requestHeaderValue(r *http.Request, names ...string) string {
	if r == nil {
		return ""
	}
	for _, name := range names {
		if value := strings.TrimSpace(r.Header.Get(name)); value != "" {
			return value
		}
	}
	return ""
}

func authAccountID(a *auth.RequestAuth) string {
	if a == nil {
		return ""
	}
	return strings.TrimSpace(a.AccountID)
}

func (h *Handler) handleNonStream(w http.ResponseWriter, resp *http.Response, completionID, model, finalPrompt string, refFileTokens int, thinkingEnabled, exposeReasoning, searchEnabled bool, toolNames []string, toolsRaw any, historySession *chatHistorySession) {
	if resp.StatusCode != http.StatusOK {
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		if historySession != nil {
			historySession.error(resp.StatusCode, string(body), "error", "", "")
		}
		writeOpenAIError(w, resp.StatusCode, string(body))
		return
	}
	result := sse.CollectStream(resp, thinkingEnabled, true)

	stripReferenceMarkers := h.compatStripReferenceMarkers()
	finalThinking := cleanVisibleOutput(result.Thinking, stripReferenceMarkers)
	finalText := cleanVisibleOutput(result.Text, stripReferenceMarkers)
	if searchEnabled {
		finalText = replaceCitationMarkersWithLinks(finalText, result.CitationLinks)
	}
	detected := detectAssistantToolCalls(result.Text, finalText, result.Thinking, result.ToolDetectionThinking, toolNames)
	if shouldWriteUpstreamEmptyOutputError(finalText) && len(detected.Calls) == 0 {
		status, message, code := upstreamEmptyOutputDetail(result.ContentFilter, finalText, finalThinking)
		if historySession != nil {
			historySession.error(status, message, code, finalThinking, finalText)
		}
		writeUpstreamEmptyOutputError(w, finalText, finalThinking, result.ContentFilter)
		return
	}
	respBody := openaifmt.BuildChatCompletionWithToolCallsVisibility(completionID, model, finalPrompt, finalThinking, finalText, detected.Calls, toolsRaw, exposeReasoning)
	addRefFileTokensToUsage(respBody, refFileTokens)
	finishReason := "stop"
	if choices, ok := respBody["choices"].([]map[string]any); ok && len(choices) > 0 {
		if fr, _ := choices[0]["finish_reason"].(string); strings.TrimSpace(fr) != "" {
			finishReason = fr
		}
	}
	if historySession != nil {
		historySession.success(http.StatusOK, finalThinking, finalText, finishReason, openaifmt.BuildChatUsageForModel(model, finalPrompt, finalThinking, finalText, refFileTokens))
	}
	writeJSON(w, http.StatusOK, respBody)
}

func (h *Handler) handleStream(w http.ResponseWriter, r *http.Request, resp *http.Response, completionID, model, finalPrompt string, refFileTokens int, thinkingEnabled, exposeReasoning, searchEnabled bool, toolNames []string, toolsRaw any, historySession *chatHistorySession) {
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		if historySession != nil {
			historySession.error(resp.StatusCode, string(body), "error", "", "")
		}
		writeOpenAIError(w, resp.StatusCode, string(body))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	rc := http.NewResponseController(w)
	_, canFlush := w.(http.Flusher)
	if !canFlush {
		config.Logger.Warn("[stream] response writer does not support flush; streaming may be buffered")
	}

	created := time.Now().Unix()
	bufferToolContent := shared.ShouldBufferStreamToolContent(finalPrompt, toolNames)
	emitEarlyToolDeltas := h.toolcallFeatureMatchEnabled() && h.toolcallEarlyEmitHighConfidence()
	stripReferenceMarkers := h.compatStripReferenceMarkers()
	initialType := "text"
	if thinkingEnabled {
		initialType = "thinking"
	}

	streamRuntime := newChatStreamRuntime(
		w,
		rc,
		canFlush,
		completionID,
		created,
		model,
		finalPrompt,
		thinkingEnabled,
		exposeReasoning,
		searchEnabled,
		stripReferenceMarkers,
		toolNames,
		toolsRaw,
		false,
		bufferToolContent,
		emitEarlyToolDeltas,
	)
	streamRuntime.refFileTokens = refFileTokens

	streamengine.ConsumeSSE(streamengine.ConsumeConfig{
		Context:             r.Context(),
		Body:                resp.Body,
		ThinkingEnabled:     thinkingEnabled,
		InitialType:         initialType,
		KeepAliveInterval:   time.Duration(dsprotocol.KeepAliveTimeout) * time.Second,
		IdleTimeout:         time.Duration(dsprotocol.StreamIdleTimeout) * time.Second,
		MaxKeepAliveNoInput: dsprotocol.MaxKeepaliveCount,
	}, streamengine.ConsumeHooks{
		OnKeepAlive: func() {
			streamRuntime.sendKeepAlive()
		},
		OnParsed: func(parsed sse.LineResult) streamengine.ParsedDecision {
			decision := streamRuntime.onParsed(parsed)
			if historySession != nil {
				historySession.progress(streamRuntime.thinking.String(), streamRuntime.text.String())
			}
			return decision
		},
		OnFinalize: func(reason streamengine.StopReason, _ error) {
			if string(reason) == "content_filter" {
				streamRuntime.finalize("content_filter", false)
			} else {
				streamRuntime.finalize("stop", false)
			}
			if historySession == nil {
				return
			}
			if streamRuntime.finalErrorMessage != "" {
				historySession.error(streamRuntime.finalErrorStatus, streamRuntime.finalErrorMessage, streamRuntime.finalErrorCode, streamRuntime.thinking.String(), streamRuntime.text.String())
				return
			}
			historySession.success(http.StatusOK, streamRuntime.finalThinking, streamRuntime.finalText, streamRuntime.finalFinishReason, streamRuntime.finalUsage)
		},
		OnContextDone: func() {
			if historySession != nil {
				historySession.stopped(streamRuntime.thinking.String(), streamRuntime.text.String(), string(streamengine.StopReasonContextCancelled))
			}
		},
	})
}
