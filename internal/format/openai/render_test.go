package openai

import (
	"encoding/json"
	"strings"
	"testing"

	"DeepSeek_Web_To_API/internal/toolcall"
)

func TestBuildResponseObjectKeepsFencedToolPayloadAsText(t *testing.T) {
	obj := BuildResponseObject(
		"resp_test",
		"gpt-4o",
		"prompt",
		"",
		"```json\n{\"tool_calls\":[{\"name\":\"search\",\"input\":{\"q\":\"golang\"}}]}\n```",
		[]string{"search"},
		nil,
	)

	outputText, _ := obj["output_text"].(string)
	if !strings.Contains(outputText, "\"tool_calls\"") {
		t.Fatalf("expected output_text to preserve fenced tool payload, got %q", outputText)
	}
	output, _ := obj["output"].([]any)
	if len(output) != 1 {
		t.Fatalf("expected one message output item, got %#v", obj["output"])
	}
	first, _ := output[0].(map[string]any)
	if first["type"] != "message" {
		t.Fatalf("expected message output type, got %#v", first["type"])
	}
}

// Backward-compatible alias for historical test name used in CI logs.
func TestBuildResponseObjectPromotesFencedToolPayloadToFunctionCall(t *testing.T) {
	TestBuildResponseObjectKeepsFencedToolPayloadAsText(t)
}

func TestBuildResponseObjectReasoningOnlyFallsBackToOutputText(t *testing.T) {
	obj := BuildResponseObject(
		"resp_test",
		"gpt-4o",
		"prompt",
		"internal thinking content",
		"",
		nil,
		nil,
	)

	outputText, _ := obj["output_text"].(string)
	if outputText == "" {
		t.Fatalf("expected output_text fallback from reasoning when final text is empty")
	}

	output, _ := obj["output"].([]any)
	if len(output) != 1 {
		t.Fatalf("expected one output item, got %#v", obj["output"])
	}
	first, _ := output[0].(map[string]any)
	if first["type"] != "message" {
		t.Fatalf("expected output type message, got %#v", first["type"])
	}
	content, _ := first["content"].([]any)
	if len(content) == 0 {
		t.Fatalf("expected reasoning content, got %#v", first["content"])
	}
	block0, _ := content[0].(map[string]any)
	if block0["type"] != "reasoning" {
		t.Fatalf("expected first content block reasoning, got %#v", block0["type"])
	}
}

func TestBuildResponseObjectPromotesToolCallFromThinkingWhenTextEmpty(t *testing.T) {
	obj := BuildResponseObject(
		"resp_test",
		"gpt-4o",
		"prompt",
		`<tool_calls><invoke name="search"><parameter name="q">from-thinking</parameter></invoke></tool_calls>`,
		"",
		[]string{"search"},
		nil,
	)

	output, _ := obj["output"].([]any)
	if len(output) != 1 {
		t.Fatalf("expected one output item, got %#v", obj["output"])
	}
	first, _ := output[0].(map[string]any)
	if first["type"] != "function_call" {
		t.Fatalf("expected function_call output, got %#v", first["type"])
	}
}

func TestBuildChatCompletionWithToolCallsCoercesSchemaDeclaredStringArguments(t *testing.T) {
	toolsRaw := []any{
		map[string]any{
			"type": "function",
			"function": map[string]any{
				"name": "Write",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"content": map[string]any{"type": "string"},
						"taskId":  map[string]any{"type": "string"},
					},
				},
			},
		},
	}
	obj := BuildChatCompletionWithToolCalls(
		"chat_test",
		"gpt-4o",
		"prompt",
		"",
		"",
		[]toolcall.ParsedToolCall{{
			Name: "Write",
			Input: map[string]any{
				"content": map[string]any{"message": "hi"},
				"taskId":  1,
			},
		}},
		toolsRaw,
	)
	choices, _ := obj["choices"].([]map[string]any)
	message, _ := choices[0]["message"].(map[string]any)
	toolCalls, _ := message["tool_calls"].([]map[string]any)
	fn, _ := toolCalls[0]["function"].(map[string]any)
	args := map[string]any{}
	if err := json.Unmarshal([]byte(fn["arguments"].(string)), &args); err != nil {
		t.Fatalf("decode arguments failed: %v", err)
	}
	if args["content"] != `{"message":"hi"}` {
		t.Fatalf("expected content stringified by schema, got %#v", args["content"])
	}
	if args["taskId"] != "1" {
		t.Fatalf("expected taskId stringified by schema, got %#v", args["taskId"])
	}
}

func TestBuildChatUsageIncludesOpenClawAliasesAndDetails(t *testing.T) {
	usage := BuildChatUsageForModel("deepseek-v4-pro", "prompt", "thinking", "answer", 0)
	if _, ok := usage["prompt_tokens_details"].(map[string]any); !ok {
		t.Fatalf("expected prompt_tokens_details object, got %#v", usage)
	}
	if _, ok := usage["completion_tokens_details"].(map[string]any); !ok {
		t.Fatalf("expected completion_tokens_details object, got %#v", usage)
	}
	if usage["input"] == nil || usage["output"] == nil || usage["totalTokens"] == nil {
		t.Fatalf("expected OpenClaw usage aliases, got %#v", usage)
	}
	cost, _ := usage["cost"].(map[string]any)
	if cost == nil {
		t.Fatalf("expected cost compatibility object, got %#v", usage)
	}
	for _, key := range []string{"input", "output", "cacheRead", "cacheWrite", "total"} {
		if _, ok := cost[key]; !ok {
			t.Fatalf("expected cost.%s compatibility field, got %#v", key, cost)
		}
	}
}

func TestBuildResponseObjectWithToolCallsCoercesSchemaDeclaredStringArguments(t *testing.T) {
	toolsRaw := []any{
		map[string]any{
			"type": "function",
			"function": map[string]any{
				"name": "Write",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"content": map[string]any{"type": "string"},
					},
				},
			},
		},
	}
	obj := BuildResponseObjectWithToolCalls(
		"resp_test",
		"gpt-4o",
		"prompt",
		"",
		"",
		[]toolcall.ParsedToolCall{{
			Name:  "Write",
			Input: map[string]any{"content": []any{"a", 1}},
		}},
		toolsRaw,
	)
	output, _ := obj["output"].([]any)
	first, _ := output[0].(map[string]any)
	args := map[string]any{}
	if err := json.Unmarshal([]byte(first["arguments"].(string)), &args); err != nil {
		t.Fatalf("decode response arguments failed: %v", err)
	}
	if args["content"] != `["a",1]` {
		t.Fatalf("expected response content stringified by schema, got %#v", args["content"])
	}
}
