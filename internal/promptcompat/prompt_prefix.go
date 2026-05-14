package promptcompat

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"sync"

	"DeepSeek_Web_To_API/internal/prompt"
	"DeepSeek_Web_To_API/internal/util"
)

type PromptPrefixInfo struct {
	Hash         string
	PrefixTokens int
	TailTokens   int
	Eligible     bool
	Reason       string
}

var (
	promptHashSecretOnce sync.Once
	promptHashSecret     []byte
)

func AnalyzeOpenAIPromptPrefix(messagesRaw []any, toolsRaw any, traceID string, toolPolicy ToolChoicePolicy, thinkingEnabled bool, model string) PromptPrefixInfo {
	messages := NormalizeOpenAIMessagesForPrompt(messagesRaw, traceID)
	if tools, ok := toolsRaw.([]any); ok && len(tools) > 0 {
		messages, _ = injectToolPrompt(messages, tools, toolPolicy)
	}
	return AnalyzePromptPrefixAt(messages, len(messages)-1, thinkingEnabled, model)
}

func AnalyzePromptPrefixAt(messages []map[string]any, prefixCount int, thinkingEnabled bool, model string) PromptPrefixInfo {
	if len(messages) < 2 {
		return PromptPrefixInfo{Reason: "no_stable_prefix"}
	}
	if prefixCount <= 0 || prefixCount > len(messages) {
		return PromptPrefixInfo{Reason: "no_stable_prefix"}
	}
	prefixMessages := clonePromptMessages(messages[:prefixCount])
	prefixText := strings.TrimSpace(prompt.MessagesPrepareWithThinking(prefixMessages, thinkingEnabled))
	if prefixText == "" {
		return PromptPrefixInfo{Reason: "no_stable_prefix"}
	}
	tailText := ""
	if prefixCount < len(messages) {
		tailText = strings.TrimSpace(prompt.MessagesPrepareWithThinking(messages[prefixCount:], thinkingEnabled))
	}
	return PromptPrefixInfo{
		Hash:         promptPrefixHash(prefixMessages, thinkingEnabled),
		PrefixTokens: util.CountPromptTokens(prefixText, model),
		TailTokens:   util.CountPromptTokens(tailText, model),
		Eligible:     true,
	}
}

func promptCacheHintFromRequest(req map[string]any) string {
	value, _ := req["prompt_cache_key"].(string)
	return sanitizePromptCacheHint(value)
}

func sanitizePromptCacheHint(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 256 {
		value = value[:256]
	}
	return value
}

func promptPrefixHash(messages []map[string]any, thinkingEnabled bool) string {
	h := hmac.New(sha256.New, promptHashSecretBytes())
	writePromptPrefixPart(h, "v1")
	if thinkingEnabled {
		writePromptPrefixPart(h, "thinking:1")
	} else {
		writePromptPrefixPart(h, "thinking:0")
	}
	encoder := json.NewEncoder(h)
	_ = encoder.Encode(messages)
	return hex.EncodeToString(h.Sum(nil))
}

func HashPromptText(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	h := hmac.New(sha256.New, promptHashSecretBytes())
	writePromptPrefixPart(h, "text:v1")
	writePromptPrefixPart(h, text)
	return hex.EncodeToString(h.Sum(nil))
}

func promptHashSecretBytes() []byte {
	promptHashSecretOnce.Do(func() {
		if configured := strings.TrimSpace(os.Getenv("DEEPSEEK_WEB_TO_API_PROMPT_HASH_KEY")); configured != "" {
			promptHashSecret = []byte(configured)
			return
		}
		promptHashSecret = make([]byte, 32)
		if _, err := rand.Read(promptHashSecret); err != nil {
			sum := sha256.Sum256([]byte("deepseek-web-to-api-prompt-hash-fallback"))
			promptHashSecret = append([]byte(nil), sum[:]...)
		}
	})
	return promptHashSecret
}

func clonePromptMessages(in []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(in))
	for _, msg := range in {
		cp := make(map[string]any, len(msg))
		for key, value := range msg {
			cp[key] = value
		}
		out = append(out, cp)
	}
	return out
}

func writePromptPrefixPart(h interface {
	Write([]byte) (int, error)
}, value string) {
	_, _ = h.Write([]byte(value))
	_, _ = h.Write([]byte{0})
}
