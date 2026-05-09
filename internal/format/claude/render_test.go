package claude

import "testing"

func TestBuildMessageResponseSkipsThinkingFallbackWhenFinalTextExists(t *testing.T) {
	resp := BuildMessageResponse(
		"msg_1",
		"claude-sonnet-4-5",
		[]any{map[string]any{"role": "user", "content": "hi"}},
		`{"tool_calls":[{"name":"search","input":{"q":"go"}}]}`,
		"normal answer",
		[]string{"search"},
	)

	if resp["stop_reason"] != "end_turn" {
		t.Fatalf("expected stop_reason=end_turn, got=%#v", resp["stop_reason"])
	}

	content, _ := resp["content"].([]map[string]any)
	foundText := false
	foundTool := false
	for _, block := range content {
		if block["type"] == "text" && block["text"] == "normal answer" {
			foundText = true
		}
		if block["type"] == "tool_use" {
			foundTool = true
		}
	}
	if !foundText {
		t.Fatalf("expected text block with finalText, got=%#v", resp["content"])
	}
	if foundTool {
		t.Fatalf("unexpected tool_use block when finalText exists, got=%#v", resp["content"])
	}
}

func TestBuildMessageResponseNormalizesToolInputBySchema(t *testing.T) {
	resp := BuildMessageResponse(
		"msg_1",
		"claude-sonnet-4-5",
		[]any{map[string]any{"role": "user", "content": "write"}},
		"",
		`<tool_calls><invoke name="Write">{"input":{"content":{"message":"hi"},"taskId":7}}</invoke></tool_calls>`,
		[]string{"Write"},
		[]any{
			map[string]any{
				"name": "Write",
				"inputSchema": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"content": map[string]any{"type": "string"},
						"taskId":  map[string]any{"type": "string"},
					},
				},
			},
		},
	)

	if resp["stop_reason"] != "tool_use" {
		t.Fatalf("expected tool_use stop reason, got %#v", resp["stop_reason"])
	}
	content, _ := resp["content"].([]map[string]any)
	if len(content) != 1 {
		t.Fatalf("expected one tool_use block, got %#v", resp["content"])
	}
	input, _ := content[0]["input"].(map[string]any)
	if input["content"] != `{"message":"hi"}` {
		t.Fatalf("expected object content coerced to string, got %#v", input["content"])
	}
	if input["taskId"] != "7" {
		t.Fatalf("expected taskId coerced to string, got %#v", input["taskId"])
	}
}

func TestApplyPromptCacheUsageAddsClaudeReadAndDeepSeekDiagnostics(t *testing.T) {
	resp := BuildMessageResponse(
		"msg_1",
		"claude-sonnet-4-5",
		[]any{map[string]any{"role": "user", "content": "hi"}},
		"",
		"normal answer",
		nil,
	)

	ApplyPromptCacheUsage(resp, 64, 32, true, true)

	usage, _ := resp["usage"].(map[string]any)
	if usage["cache_read_input_tokens"] != 64 || usage["prompt_cache_hit_tokens"] != 64 || usage["prompt_cache_miss_tokens"] != 32 {
		t.Fatalf("unexpected prompt cache usage: %#v", usage)
	}
	if _, ok := usage["cache_creation_input_tokens"]; ok {
		t.Fatalf("DeepSeek miss tokens must not be reported as Claude cache creation tokens: %#v", usage)
	}
}
