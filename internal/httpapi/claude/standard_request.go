package claude

import (
	"fmt"
	"sort"
	"strings"

	"DeepSeek_Web_To_API/internal/config"
	"DeepSeek_Web_To_API/internal/prompt"
	"DeepSeek_Web_To_API/internal/promptcompat"
	"DeepSeek_Web_To_API/internal/util"
)

type claudeNormalizedRequest struct {
	Standard           promptcompat.StandardRequest
	NormalizedMessages []any
}

func normalizeClaudeRequest(store ConfigReader, req map[string]any) (claudeNormalizedRequest, error) {
	model, _ := req["model"].(string)
	messagesRaw, _ := req["messages"].([]any)
	if strings.TrimSpace(model) == "" || len(messagesRaw) == 0 {
		return claudeNormalizedRequest{}, fmt.Errorf("request must include 'model' and 'messages'")
	}
	if _, ok := req["max_tokens"]; !ok {
		req["max_tokens"] = 8192
	}
	normalizedMessages := normalizeClaudeMessages(messagesRaw)
	payload := cloneMap(req)
	if systemText := claudeSystemText(req["system"]); systemText != "" {
		payload["system"] = systemText
	} else {
		delete(payload, "system")
	}
	payload["messages"] = normalizedMessages
	toolsRequested, _ := req["tools"].([]any)
	payload["messages"] = injectClaudeToolPrompt(payload, normalizedMessages, toolsRequested)

	dsPayload := convertClaudeToDeepSeek(payload, store)
	dsModel, _ := dsPayload["model"].(string)
	_, searchEnabled, ok := config.GetModelConfig(dsModel)
	if !ok {
		searchEnabled = false
	}
	thinkingEnabled := util.ResolveThinkingEnabled(req, false)
	if config.IsNoThinkingModel(dsModel) {
		thinkingEnabled = false
	}
	dsMessages, _ := dsPayload["messages"].([]any)
	finalPrompt := prompt.MessagesPrepareWithThinking(toMessageMaps(dsMessages), thinkingEnabled)
	prefixInfo := promptcompat.AnalyzeOpenAIPromptPrefix(dsMessages, nil, "", promptcompat.DefaultToolChoicePolicy(), thinkingEnabled, dsModel)
	toolNames := extractClaudeToolNames(toolsRequested)
	if len(toolNames) == 0 && len(toolsRequested) > 0 {
		toolNames = []string{"__any_tool__"}
	}

	return claudeNormalizedRequest{
		Standard: promptcompat.StandardRequest{
			Surface:              "anthropic_messages",
			RequestedModel:       strings.TrimSpace(model),
			ResolvedModel:        dsModel,
			ResponseModel:        strings.TrimSpace(model),
			Messages:             dsMessages,
			PromptTokenText:      finalPrompt,
			ToolsRaw:             toolsRequested,
			PromptCacheHint:      claudePromptCacheHint(req),
			PromptPrefixHash:     prefixInfo.Hash,
			PromptPrefixTokens:   prefixInfo.PrefixTokens,
			PromptTailTokens:     prefixInfo.TailTokens,
			PromptPrefixEligible: prefixInfo.Eligible,
			PromptPrefixReason:   prefixInfo.Reason,
			FinalPrompt:          finalPrompt,
			ToolNames:            toolNames,
			Stream:               util.ToBool(req["stream"]),
			Thinking:             thinkingEnabled,
			Search:               searchEnabled,
		},
		NormalizedMessages: normalizedMessages,
	}, nil
}

type claudeCacheControlSummary struct {
	auto     string
	blocks   int
	controls map[string]int
}

func claudePromptCacheHint(req map[string]any) string {
	if req == nil {
		return ""
	}
	summary := claudeCacheControlSummary{controls: map[string]int{}}
	if control := claudeCacheControlID(req["cache_control"]); control != "" {
		summary.auto = control
	}
	observeClaudeToolCacheControls(req["tools"], &summary)
	observeClaudeSystemCacheControls(req["system"], &summary)
	observeClaudeMessageCacheControls(req["messages"], &summary)
	if summary.auto == "" && summary.blocks == 0 {
		return ""
	}
	parts := []string{"claude"}
	if summary.auto != "" {
		parts = append(parts, "auto:"+summary.auto)
	}
	if summary.blocks > 0 {
		parts = append(parts, fmt.Sprintf("blocks:%d", summary.blocks))
		keys := make([]string, 0, len(summary.controls))
		for key := range summary.controls {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		controls := make([]string, 0, len(keys))
		for _, key := range keys {
			controls = append(controls, fmt.Sprintf("%s=%d", key, summary.controls[key]))
		}
		if len(controls) > 0 {
			parts = append(parts, "controls:"+strings.Join(controls, ","))
		}
	}
	return strings.Join(parts, ";")
}

func observeClaudeToolCacheControls(value any, summary *claudeCacheControlSummary) {
	if summary == nil || value == nil {
		return
	}
	tools, ok := value.([]any)
	if !ok {
		return
	}
	for _, item := range tools {
		observeClaudeCacheControlBlock(item, summary)
	}
}

func observeClaudeSystemCacheControls(value any, summary *claudeCacheControlSummary) {
	if summary == nil || value == nil {
		return
	}
	switch system := value.(type) {
	case []any:
		for _, item := range system {
			observeClaudeCacheControlBlock(item, summary)
		}
	case map[string]any:
		observeClaudeCacheControlBlock(system, summary)
	}
}

func observeClaudeMessageCacheControls(value any, summary *claudeCacheControlSummary) {
	if summary == nil || value == nil {
		return
	}
	messages, ok := value.([]any)
	if !ok {
		return
	}
	for _, item := range messages {
		msg, ok := item.(map[string]any)
		if !ok {
			continue
		}
		observeClaudeContentCacheControls(msg["content"], summary)
	}
}

func observeClaudeContentCacheControls(value any, summary *claudeCacheControlSummary) {
	if summary == nil || value == nil {
		return
	}
	switch content := value.(type) {
	case []any:
		for _, item := range content {
			observeClaudeCacheControlBlock(item, summary)
		}
	case map[string]any:
		observeClaudeCacheControlBlock(content, summary)
	}
}

func observeClaudeCacheControlBlock(value any, summary *claudeCacheControlSummary) {
	block, ok := value.(map[string]any)
	if !ok || summary == nil {
		return
	}
	if control := claudeCacheControlID(block["cache_control"]); control != "" {
		summary.blocks++
		summary.controls[control]++
	}
}

func claudeCacheControlID(value any) string {
	control, ok := value.(map[string]any)
	if !ok || control == nil {
		return ""
	}
	typeName := strings.ToLower(strings.TrimSpace(safeStringValue(control["type"])))
	if typeName == "" {
		typeName = "ephemeral"
	}
	if typeName != "ephemeral" {
		typeName = "other"
	}
	ttl := strings.ToLower(strings.TrimSpace(safeStringValue(control["ttl"])))
	switch ttl {
	case "", "5m":
		ttl = "5m"
	case "1h":
		ttl = "1h"
	default:
		ttl = "other"
	}
	return typeName + ":" + ttl
}

func injectClaudeToolPrompt(payload map[string]any, normalizedMessages []any, tools []any) []any {
	if len(tools) == 0 {
		return normalizedMessages
	}
	toolPrompt := strings.TrimSpace(buildClaudeToolPrompt(tools))
	if toolPrompt == "" {
		return normalizedMessages
	}

	// Prefer top-level Anthropic-style system prompt when available.
	if systemText, ok := payload["system"].(string); ok && strings.TrimSpace(systemText) != "" {
		payload["system"] = mergeSystemPrompt(systemText, toolPrompt)
		return normalizedMessages
	}

	messages := cloneAnySlice(normalizedMessages)
	for i := range messages {
		msg, ok := messages[i].(map[string]any)
		if !ok {
			continue
		}
		role, _ := msg["role"].(string)
		if !strings.EqualFold(strings.TrimSpace(role), "system") {
			continue
		}
		copied := cloneMap(msg)
		copied["content"] = mergeSystemPrompt(strings.TrimSpace(fmt.Sprintf("%v", copied["content"])), toolPrompt)
		messages[i] = copied
		return messages
	}

	return append([]any{map[string]any{"role": "system", "content": toolPrompt}}, messages...)
}

func mergeSystemPrompt(base, extra string) string {
	base = strings.TrimSpace(base)
	extra = strings.TrimSpace(extra)
	switch {
	case base == "":
		return extra
	case extra == "":
		return base
	default:
		return base + "\n\n" + extra
	}
}

func cloneAnySlice(in []any) []any {
	if len(in) == 0 {
		return nil
	}
	out := make([]any, len(in))
	copy(out, in)
	return out
}
