package embeddings

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"DeepSeek_Web_To_API/internal/auth"
	"DeepSeek_Web_To_API/internal/chathistory"
	"DeepSeek_Web_To_API/internal/config"
	"DeepSeek_Web_To_API/internal/httpapi/openai/shared"
	"DeepSeek_Web_To_API/internal/httpapi/requestbody"
	"DeepSeek_Web_To_API/internal/util"
)

type Handler struct {
	Store       shared.ConfigReader
	Auth        shared.AuthResolver
	DS          shared.DeepSeekCaller
	ChatHistory *chathistory.Store
}

func (h *Handler) Embeddings(w http.ResponseWriter, r *http.Request) {
	a, err := h.Auth.Determine(r)
	if err != nil {
		status := http.StatusUnauthorized
		detail := err.Error()
		if err == auth.ErrNoAccount {
			status = http.StatusTooManyRequests
		}
		shared.WriteOpenAIError(w, status, detail)
		return
	}
	defer h.Auth.Release(a)

	raw, err := requestbody.ReadAll(w, r, shared.GeneralMaxSize)
	if err != nil {
		if errors.Is(err, requestbody.ErrRequestBodyTooLarge) || strings.Contains(strings.ToLower(err.Error()), "too large") {
			shared.WriteOpenAIError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		if errors.Is(err, requestbody.ErrInvalidUTF8Body) {
			shared.WriteOpenAIError(w, http.StatusBadRequest, "invalid json: invalid utf-8 request body")
			return
		}
		shared.WriteOpenAIError(w, http.StatusBadRequest, "invalid body")
		return
	}
	var req map[string]any
	if err := json.NewDecoder(bytes.NewReader(raw)).Decode(&req); err != nil {
		shared.WriteOpenAIError(w, http.StatusBadRequest, "invalid json")
		return
	}
	model, _ := req["model"].(string)
	model = strings.TrimSpace(model)
	if model == "" {
		shared.WriteOpenAIError(w, http.StatusBadRequest, "Request must include 'model'.")
		return
	}
	if _, ok := config.ResolveModel(h.Store, model); !ok {
		shared.WriteOpenAIError(w, http.StatusBadRequest, fmt.Sprintf("Model '%s' is not available.", model))
		return
	}

	inputs := ExtractEmbeddingInputs(req["input"])
	if len(inputs) == 0 {
		shared.WriteOpenAIError(w, http.StatusBadRequest, "Request must include non-empty 'input'.")
		return
	}

	provider := ""
	if h.Store != nil {
		provider = strings.ToLower(strings.TrimSpace(h.Store.EmbeddingsProvider()))
	}
	if provider == "" {
		shared.WriteOpenAIError(w, http.StatusNotImplemented, "Embeddings provider is not configured. Set embeddings.provider in config.")
		return
	}
	switch provider {
	case "mock", "deterministic", "builtin":
		// supported local deterministic provider
	default:
		shared.WriteOpenAIError(w, http.StatusNotImplemented, fmt.Sprintf("Embeddings provider '%s' is not supported.", provider))
		return
	}

	data := make([]map[string]any, 0, len(inputs))
	totalTokens := 0
	for i, input := range inputs {
		totalTokens += util.EstimateTokens(input)
		data = append(data, map[string]any{
			"object":    "embedding",
			"index":     i,
			"embedding": DeterministicEmbedding(input),
		})
	}
	shared.WriteJSON(w, http.StatusOK, map[string]any{
		"object": "list",
		"data":   data,
		"model":  model,
		"usage": map[string]any{
			"prompt_tokens": totalTokens,
			"total_tokens":  totalTokens,
		},
	})
}

func ExtractEmbeddingInputs(raw any) []string {
	switch v := raw.(type) {
	case string:
		s := strings.TrimSpace(v)
		if s == "" {
			return nil
		}
		return []string{s}
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			switch iv := item.(type) {
			case string:
				s := strings.TrimSpace(iv)
				if s != "" {
					out = append(out, s)
				}
			case []any:
				// Token array input support: convert to stable string form.
				out = append(out, fmt.Sprintf("%v", iv))
			default:
				s := strings.TrimSpace(fmt.Sprintf("%v", iv))
				if s != "" {
					out = append(out, s)
				}
			}
		}
		return out
	default:
		return nil
	}
}

func DeterministicEmbedding(input string) []float64 {
	// Keep response shape stable without external dependencies.
	const dims = 64
	out := make([]float64, dims)
	seed := sha256.Sum256([]byte(input))
	buf := seed[:]
	for i := 0; i < dims; i++ {
		if len(buf) < 4 {
			next := sha256.Sum256(buf)
			buf = next[:]
		}
		v := binary.BigEndian.Uint32(buf[:4])
		buf = buf[4:]
		// map [0, 2^32) -> [-1, 1]
		out[i] = (float64(v)/2147483647.5 - 1.0)
	}
	return out
}
