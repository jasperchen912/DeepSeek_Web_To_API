package claude

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"

	"DeepSeek_Web_To_API/internal/auth"
	openaishared "DeepSeek_Web_To_API/internal/httpapi/openai/shared"
	"DeepSeek_Web_To_API/internal/httpapi/requestbody"
	"DeepSeek_Web_To_API/internal/translatorcliproxy"
	"DeepSeek_Web_To_API/internal/util"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
)

func (h *Handler) Messages(w http.ResponseWriter, r *http.Request) {
	if strings.TrimSpace(r.Header.Get("anthropic-version")) == "" {
		r.Header.Set("anthropic-version", "2023-06-01")
	}
	if h.handleDirectClaudeIfAvailable(w, r, h.Store) {
		return
	}
	if h.OpenAI == nil {
		writeClaudeError(w, http.StatusInternalServerError, "OpenAI proxy backend unavailable.")
		return
	}
	if h.proxyViaOpenAI(w, r, h.Store) {
		return
	}
	writeClaudeError(w, http.StatusBadGateway, "Failed to proxy Claude request.")
}

var claudeProxyMaxBodyBytes int64 = openaishared.GeneralMaxSize

func (h *Handler) proxyViaOpenAI(w http.ResponseWriter, r *http.Request, store ConfigReader) bool {
	raw, err := requestbody.ReadAll(w, r, claudeProxyMaxBodyBytes)
	if err != nil {
		if errors.Is(err, requestbody.ErrInvalidUTF8Body) {
			writeClaudeError(w, http.StatusBadRequest, "invalid json")
		} else if errors.Is(err, requestbody.ErrRequestBodyTooLarge) || strings.Contains(strings.ToLower(err.Error()), "too large") {
			writeClaudeError(w, http.StatusRequestEntityTooLarge, "request body too large")
		} else {
			writeClaudeError(w, http.StatusBadRequest, "invalid body")
		}
		return true
	}
	var req map[string]any
	if err := json.Unmarshal(raw, &req); err != nil {
		writeClaudeError(w, http.StatusBadRequest, "invalid json")
		return true
	}
	model, _ := req["model"].(string)
	stream := util.ToBool(req["stream"])

	// Use the shared global model resolver so Claude/OpenAI/Gemini stay consistent.
	translateModel := model
	if store != nil {
		if norm, normErr := normalizeClaudeRequest(store, cloneMap(req)); normErr == nil && strings.TrimSpace(norm.Standard.ResolvedModel) != "" {
			translateModel = strings.TrimSpace(norm.Standard.ResolvedModel)
		}
	}
	translatedReq := translatorcliproxy.ToOpenAI(sdktranslator.FormatClaude, translateModel, raw, stream)
	translatedReq, exposeThinking := applyClaudeThinkingPolicyToOpenAIRequest(translatedReq, req, stream)

	proxyReq := r.Clone(requestbody.WithRawBody(openaishared.WithCurrentInputFileSkipped(r.Context()), translatedReq))
	proxyReq.URL.Path = "/v1/chat/completions"
	proxyReq.Body = io.NopCloser(bytes.NewReader(translatedReq))
	proxyReq.ContentLength = int64(len(translatedReq))
	proxyReq.Header.Set(auth.SessionAffinityHeader, claudeSessionAffinityScope(r, req))

	if stream {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache, no-transform")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")
		streamWriter := translatorcliproxy.NewOpenAIStreamTranslatorWriter(w, sdktranslator.FormatClaude, model, raw, translatedReq)
		h.OpenAI.ChatCompletions(streamWriter, proxyReq)
		return true
	}

	rec := httptest.NewRecorder()
	h.OpenAI.ChatCompletions(rec, proxyReq)
	res := rec.Result()
	defer func() { _ = res.Body.Close() }()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		for k, vv := range res.Header {
			for _, v := range vv {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(res.StatusCode)
		_, _ = w.Write(body)
		return true
	}
	converted := translatorcliproxy.FromOpenAINonStream(sdktranslator.FormatClaude, model, raw, translatedReq, body)
	if !exposeThinking {
		converted = stripClaudeThinkingBlocks(converted)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(converted)
	return true
}
