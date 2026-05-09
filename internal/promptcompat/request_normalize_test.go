package promptcompat

import (
	"strings"
	"testing"
)

type normalizeRequestTestConfig struct{}

func (normalizeRequestTestConfig) ModelAliases() map[string]string { return nil }
func (normalizeRequestTestConfig) CompatWideInputStrictOutput() bool {
	return true
}

func TestNormalizeOpenAIChatRequestToolChoiceNoneDisablesTools(t *testing.T) {
	t.Parallel()

	out, err := NormalizeOpenAIChatRequest(normalizeRequestTestConfig{}, map[string]any{
		"model":       "deepseek-v4-flash",
		"messages":    []any{map[string]any{"role": "user", "content": "hello"}},
		"tools":       requestNormalizeTools(),
		"tool_choice": "none",
	}, "")
	if err != nil {
		t.Fatalf("NormalizeOpenAIChatRequest error: %v", err)
	}
	if out.ToolChoice.Mode != ToolChoiceNone {
		t.Fatalf("expected tool_choice none, got %#v", out.ToolChoice)
	}
	if len(out.ToolNames) != 0 {
		t.Fatalf("expected no tool detection names for tool_choice none, got %#v", out.ToolNames)
	}
	if strings.Contains(out.FinalPrompt, "You have access to these tools") {
		t.Fatalf("expected tool prompt to be omitted for tool_choice none: %q", out.FinalPrompt)
	}
}

func TestNormalizeOpenAIChatRequestRecordsPromptCacheHintWithoutForwarding(t *testing.T) {
	t.Parallel()

	out, err := NormalizeOpenAIChatRequest(normalizeRequestTestConfig{}, map[string]any{
		"model":            "deepseek-v4-flash",
		"messages":         []any{map[string]any{"role": "system", "content": "stable"}, map[string]any{"role": "user", "content": "hello"}},
		"prompt_cache_key": "conversation-a",
	}, "")
	if err != nil {
		t.Fatalf("NormalizeOpenAIChatRequest error: %v", err)
	}
	if out.PromptCacheHint != "conversation-a" {
		t.Fatalf("unexpected prompt cache hint: %q", out.PromptCacheHint)
	}
	if !out.PromptPrefixEligible || out.PromptPrefixHash == "" {
		t.Fatalf("expected prompt prefix diagnostics, got %#v", out)
	}
	payload := out.CompletionPayload("session")
	if _, ok := payload["prompt_cache_key"]; ok {
		t.Fatalf("prompt_cache_key should not be forwarded: %#v", payload)
	}
}

func TestNormalizeOpenAIChatRequestToolChoiceForcedLimitsToolPrompt(t *testing.T) {
	t.Parallel()

	out, err := NormalizeOpenAIChatRequest(normalizeRequestTestConfig{}, map[string]any{
		"model":    "deepseek-v4-flash",
		"messages": []any{map[string]any{"role": "user", "content": "hello"}},
		"tools":    requestNormalizeTools(),
		"tool_choice": map[string]any{
			"type": "function",
			"function": map[string]any{
				"name": "search_web",
			},
		},
	}, "")
	if err != nil {
		t.Fatalf("NormalizeOpenAIChatRequest error: %v", err)
	}
	if out.ToolChoice.Mode != ToolChoiceForced || out.ToolChoice.ForcedName != "search_web" {
		t.Fatalf("expected forced search_web policy, got %#v", out.ToolChoice)
	}
	if len(out.ToolNames) != 1 || out.ToolNames[0] != "search_web" {
		t.Fatalf("expected only forced tool name, got %#v", out.ToolNames)
	}
	if !out.ToolChoice.Allows("search_web") || out.ToolChoice.Allows("read_file") {
		t.Fatalf("unexpected allowed tool set: %#v", out.ToolChoice.Allowed)
	}
	if !strings.Contains(out.FinalPrompt, "MUST call exactly this tool name: search_web") {
		t.Fatalf("expected forced tool instruction: %q", out.FinalPrompt)
	}
	if strings.Contains(out.FinalPrompt, "Tool: read_file") {
		t.Fatalf("expected prompt to omit unforced tools: %q", out.FinalPrompt)
	}
}

func TestNormalizeOpenAIChatRequestRequiredToolChoiceRequiresTools(t *testing.T) {
	t.Parallel()

	_, err := NormalizeOpenAIChatRequest(normalizeRequestTestConfig{}, map[string]any{
		"model":       "deepseek-v4-flash",
		"messages":    []any{map[string]any{"role": "user", "content": "hello"}},
		"tool_choice": "required",
	}, "")
	if err == nil {
		t.Fatal("expected required tool_choice without tools to fail")
	}
	if !strings.Contains(err.Error(), "requires non-empty tools") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNormalizeOpenAIChatRequestKeepsReasoningHiddenByDefault(t *testing.T) {
	t.Parallel()

	out, err := NormalizeOpenAIChatRequest(normalizeRequestTestConfig{}, map[string]any{
		"model":    "deepseek-v4-pro",
		"messages": []any{map[string]any{"role": "user", "content": "hello"}},
	}, "")
	if err != nil {
		t.Fatalf("NormalizeOpenAIChatRequest error: %v", err)
	}
	if !out.Thinking {
		t.Fatal("expected deepseek-v4-pro to keep internal thinking enabled")
	}
	if out.ExposeReasoning {
		t.Fatal("expected reasoning output hidden unless explicitly requested")
	}
}

func TestNormalizeOpenAIChatRequestExposesReasoningWhenRequested(t *testing.T) {
	t.Parallel()

	out, err := NormalizeOpenAIChatRequest(normalizeRequestTestConfig{}, map[string]any{
		"model":            "deepseek-v4-pro",
		"messages":         []any{map[string]any{"role": "user", "content": "hello"}},
		"reasoning_effort": "medium",
	}, "")
	if err != nil {
		t.Fatalf("NormalizeOpenAIChatRequest error: %v", err)
	}
	if !out.Thinking {
		t.Fatal("expected explicit reasoning_effort to keep thinking enabled")
	}
	if !out.ExposeReasoning {
		t.Fatal("expected explicit reasoning_effort to expose reasoning output")
	}
}

func TestNormalizeOpenAIChatRequestDoesNotExposeDisabledReasoning(t *testing.T) {
	t.Parallel()

	out, err := NormalizeOpenAIChatRequest(normalizeRequestTestConfig{}, map[string]any{
		"model":            "deepseek-v4-pro",
		"messages":         []any{map[string]any{"role": "user", "content": "hello"}},
		"reasoning_effort": "none",
	}, "")
	if err != nil {
		t.Fatalf("NormalizeOpenAIChatRequest error: %v", err)
	}
	if out.Thinking {
		t.Fatal("expected reasoning_effort=none to disable thinking")
	}
	if out.ExposeReasoning {
		t.Fatal("expected disabled reasoning not to be exposed")
	}
}

func TestNormalizeOpenAIChatRequestStoresRepairedToolMessages(t *testing.T) {
	t.Parallel()

	out, err := NormalizeOpenAIChatRequest(normalizeRequestTestConfig{}, map[string]any{
		"model": "deepseek-v4-flash",
		"messages": []any{
			map[string]any{
				"role": "assistant",
				"tool_calls": []any{
					map[string]any{
						"id": "call_search",
						"function": map[string]any{
							"name":      "search_web",
							"arguments": map[string]any{"q": "deepseek-web-to-api"},
						},
					},
				},
			},
			map[string]any{
				"role":    "tool",
				"name":    "search_web",
				"content": "result",
			},
		},
	}, "")
	if err != nil {
		t.Fatalf("NormalizeOpenAIChatRequest error: %v", err)
	}
	if len(out.Messages) != 2 {
		t.Fatalf("expected repaired messages preserved in standard request, got %#v", out.Messages)
	}
	toolMsg, ok := out.Messages[1].(map[string]any)
	if !ok {
		t.Fatalf("expected tool message map, got %#v", out.Messages[1])
	}
	if toolMsg["tool_call_id"] != "call_search" {
		t.Fatalf("expected tool_call_id repaired in standard request, got %#v", toolMsg)
	}
	if !strings.Contains(out.FinalPrompt, "result") {
		t.Fatalf("expected repaired tool result in final prompt, got %q", out.FinalPrompt)
	}
}

func TestNormalizeOpenAIResponsesRequestToolChoiceNoneDisablesTools(t *testing.T) {
	t.Parallel()

	out, err := NormalizeOpenAIResponsesRequest(normalizeRequestTestConfig{}, map[string]any{
		"model":       "deepseek-v4-flash",
		"input":       "hello",
		"tools":       requestNormalizeTools(),
		"tool_choice": "none",
	}, "")
	if err != nil {
		t.Fatalf("NormalizeOpenAIResponsesRequest error: %v", err)
	}
	if out.ToolChoice.Mode != ToolChoiceNone {
		t.Fatalf("expected tool_choice none, got %#v", out.ToolChoice)
	}
	if len(out.ToolNames) != 0 {
		t.Fatalf("expected no tool detection names for tool_choice none, got %#v", out.ToolNames)
	}
	if strings.Contains(out.FinalPrompt, "You have access to these tools") {
		t.Fatalf("expected tool prompt to be omitted for tool_choice none: %q", out.FinalPrompt)
	}
}

func TestNormalizeOpenAIResponsesRequestToolChoiceForcedLimitsToolPrompt(t *testing.T) {
	t.Parallel()

	out, err := NormalizeOpenAIResponsesRequest(normalizeRequestTestConfig{}, map[string]any{
		"model": "deepseek-v4-flash",
		"input": "hello",
		"tools": requestNormalizeTools(),
		"tool_choice": map[string]any{
			"type": "function",
			"function": map[string]any{
				"name": "search_web",
			},
		},
	}, "")
	if err != nil {
		t.Fatalf("NormalizeOpenAIResponsesRequest error: %v", err)
	}
	if out.ToolChoice.Mode != ToolChoiceForced || out.ToolChoice.ForcedName != "search_web" {
		t.Fatalf("expected forced search_web policy, got %#v", out.ToolChoice)
	}
	if len(out.ToolNames) != 1 || out.ToolNames[0] != "search_web" {
		t.Fatalf("expected only forced tool name, got %#v", out.ToolNames)
	}
	if !out.ToolChoice.Allows("search_web") || out.ToolChoice.Allows("read_file") {
		t.Fatalf("unexpected allowed tool set: %#v", out.ToolChoice.Allowed)
	}
	if !strings.Contains(out.FinalPrompt, "MUST call exactly this tool name: search_web") {
		t.Fatalf("expected forced tool instruction: %q", out.FinalPrompt)
	}
	if strings.Contains(out.FinalPrompt, "Tool: read_file") {
		t.Fatalf("expected prompt to omit unforced tools: %q", out.FinalPrompt)
	}
}

func TestNormalizeOpenAIResponsesRequestRequiredToolChoiceRequiresTools(t *testing.T) {
	t.Parallel()

	_, err := NormalizeOpenAIResponsesRequest(normalizeRequestTestConfig{}, map[string]any{
		"model":       "deepseek-v4-flash",
		"input":       "hello",
		"tool_choice": "required",
	}, "")
	if err == nil {
		t.Fatal("expected required tool_choice without tools to fail")
	}
	if !strings.Contains(err.Error(), "requires non-empty tools") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func requestNormalizeTools() []any {
	return []any{
		map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        "search_web",
				"description": "Search web pages",
				"parameters":  map[string]any{"type": "object"},
			},
		},
		map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        "read_file",
				"description": "Read a file",
				"parameters":  map[string]any{"type": "object"},
			},
		},
	}
}
