package claude

import (
	"testing"

	"DeepSeek_Web_To_API/internal/config"
)

func TestNormalizeClaudeRequest(t *testing.T) {
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", `{}`)
	store := config.LoadStore()
	req := map[string]any{
		"model": "claude-opus-4-6",
		"messages": []any{
			map[string]any{"role": "user", "content": "hello"},
		},
		"stream": true,
		"tools": []any{
			map[string]any{"name": "search", "description": "Search"},
		},
	}
	norm, err := normalizeClaudeRequest(store, req)
	if err != nil {
		t.Fatalf("normalize failed: %v", err)
	}
	if norm.Standard.ResolvedModel == "" {
		t.Fatalf("expected resolved model")
	}
	if !norm.Standard.Stream {
		t.Fatalf("expected stream=true")
	}
	if len(norm.Standard.ToolNames) == 0 {
		t.Fatalf("expected tool names")
	}
	if norm.Standard.ToolsRaw == nil {
		t.Fatalf("expected ToolsRaw preserved for downstream normalization")
	}
	if norm.Standard.FinalPrompt == "" {
		t.Fatalf("expected non-empty final prompt")
	}
}

func TestNormalizeClaudeRequestSupportsCamelCaseInputSchemaPromptInjection(t *testing.T) {
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", `{}`)
	store := config.LoadStore()
	req := map[string]any{
		"model": "claude-sonnet-4-5",
		"messages": []any{
			map[string]any{"role": "user", "content": "hello"},
		},
		"tools": []any{
			map[string]any{
				"name":        "todowrite",
				"description": "Write todos",
				"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"todos": map[string]any{"type": "array"}}},
			},
		},
	}
	norm, err := normalizeClaudeRequest(store, req)
	if err != nil {
		t.Fatalf("normalize failed: %v", err)
	}
	if !containsStr(norm.Standard.FinalPrompt, `"type":"array"`) {
		t.Fatalf("expected inputSchema to be injected into prompt, got=%q", norm.Standard.FinalPrompt)
	}
}

func TestNormalizeClaudeRequestIgnoresSchemaCacheControlPropertyAsHint(t *testing.T) {
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", `{}`)
	store := config.LoadStore()
	req := map[string]any{
		"model": "claude-sonnet-4-5",
		"messages": []any{
			map[string]any{"role": "user", "content": "hello"},
		},
		"tools": []any{
			map[string]any{
				"name":        "configure",
				"description": "Configure a resource",
				"input_schema": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"cache_control": map[string]any{
							"type":        "object",
							"description": "user payload field, not Anthropic transport cache_control",
						},
					},
				},
			},
		},
	}

	norm, err := normalizeClaudeRequest(store, req)
	if err != nil {
		t.Fatalf("normalize failed: %v", err)
	}
	if norm.Standard.PromptCacheHint != "" {
		t.Fatalf("expected schema property not to create prompt cache hint, got %q", norm.Standard.PromptCacheHint)
	}
}

func TestNormalizeClaudeRequestInjectsToolsIntoExistingSystemMessage(t *testing.T) {
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", `{}`)
	store := config.LoadStore()
	req := map[string]any{
		"model": "claude-sonnet-4-5",
		"messages": []any{
			map[string]any{"role": "system", "content": "baseline rule"},
			map[string]any{"role": "user", "content": "hello"},
		},
		"tools": []any{
			map[string]any{"name": "search", "description": "Search"},
		},
	}

	norm, err := normalizeClaudeRequest(store, req)
	if err != nil {
		t.Fatalf("normalize failed: %v", err)
	}

	if !containsStr(norm.Standard.FinalPrompt, "You have access to these tools") {
		t.Fatalf("expected tool prompt injected into final prompt, got=%q", norm.Standard.FinalPrompt)
	}
	if !containsStr(norm.Standard.FinalPrompt, "baseline rule") {
		t.Fatalf("expected existing system message preserved, got=%q", norm.Standard.FinalPrompt)
	}
}

func TestNormalizeClaudeRequestInjectsToolsIntoTopLevelSystem(t *testing.T) {
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", `{}`)
	store := config.LoadStore()
	req := map[string]any{
		"model":  "claude-sonnet-4-5",
		"system": "top-level system",
		"messages": []any{
			map[string]any{"role": "user", "content": "hello"},
		},
		"tools": []any{
			map[string]any{"name": "search", "description": "Search"},
		},
	}

	norm, err := normalizeClaudeRequest(store, req)
	if err != nil {
		t.Fatalf("normalize failed: %v", err)
	}

	if !containsStr(norm.Standard.FinalPrompt, "top-level system") {
		t.Fatalf("expected top-level system preserved, got=%q", norm.Standard.FinalPrompt)
	}
	if !containsStr(norm.Standard.FinalPrompt, "You have access to these tools") {
		t.Fatalf("expected tool prompt injected, got=%q", norm.Standard.FinalPrompt)
	}
}

func TestNormalizeClaudeRequestSupportsClaudeCodeSystemBlocks(t *testing.T) {
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", `{}`)
	store := config.LoadStore()
	req := map[string]any{
		"model": "claude-sonnet-4-6",
		"system": []any{
			map[string]any{
				"type": "text",
				"text": "Claude Code system prompt",
				"cache_control": map[string]any{
					"type": "ephemeral",
					"ttl":  "1h",
				},
			},
			"extra system line",
		},
		"betas": []any{"claude-code-20250219", "context-management-2025-06-27"},
		"context_management": map[string]any{
			"edits": []any{map[string]any{"type": "clear_thinking_20251015", "keep": "all"}},
		},
		"messages": []any{
			map[string]any{"role": "user", "content": "hello"},
		},
		"tools": []any{
			map[string]any{
				"name":          "Read",
				"description":   "Read a file",
				"defer_loading": true,
				"cache_control": map[string]any{"type": "ephemeral"},
				"input_schema":  map[string]any{"type": "object", "properties": map[string]any{"file_path": map[string]any{"type": "string"}}},
			},
		},
	}

	norm, err := normalizeClaudeRequest(store, req)
	if err != nil {
		t.Fatalf("normalize failed: %v", err)
	}
	if !containsStr(norm.Standard.FinalPrompt, "Claude Code system prompt") || !containsStr(norm.Standard.FinalPrompt, "extra system line") {
		t.Fatalf("expected system block text in prompt, got=%q", norm.Standard.FinalPrompt)
	}
	if containsStr(norm.Standard.FinalPrompt, "cache_control") || containsStr(norm.Standard.FinalPrompt, "context_management") {
		t.Fatalf("expected Claude Code beta transport fields not to leak into prompt, got=%q", norm.Standard.FinalPrompt)
	}
	if !containsStr(norm.Standard.FinalPrompt, "Tool: Read") {
		t.Fatalf("expected tool prompt preserved, got=%q", norm.Standard.FinalPrompt)
	}
	if len(norm.Standard.Messages) == 0 {
		t.Fatalf("expected prompt messages to be populated")
	}
	if norm.Standard.PromptCacheHint != "claude;blocks:2;controls:ephemeral:1h=1,ephemeral:5m=1" {
		t.Fatalf("unexpected prompt cache hint: %q", norm.Standard.PromptCacheHint)
	}
	if !norm.Standard.PromptPrefixEligible || norm.Standard.PromptPrefixHash == "" || norm.Standard.PromptPrefixTokens <= 0 {
		t.Fatalf("expected prompt prefix diagnostics, got eligible=%v hash=%q tokens=%d", norm.Standard.PromptPrefixEligible, norm.Standard.PromptPrefixHash, norm.Standard.PromptPrefixTokens)
	}
	first, _ := norm.Standard.Messages[0].(map[string]any)
	if first["role"] != "system" {
		t.Fatalf("expected first standard message to include normalized top-level system, got %#v", norm.Standard.Messages)
	}
}

func TestNormalizeClaudeRequestRecordsTopLevelCacheControlHint(t *testing.T) {
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", `{}`)
	store := config.LoadStore()
	req := map[string]any{
		"model":         "claude-sonnet-4-6",
		"cache_control": map[string]any{"type": "ephemeral", "ttl": "1h"},
		"system":        "stable system",
		"messages": []any{
			map[string]any{"role": "user", "content": "remember this"},
			map[string]any{"role": "assistant", "content": "ok"},
			map[string]any{"role": "user", "content": "now answer"},
		},
	}

	norm, err := normalizeClaudeRequest(store, req)
	if err != nil {
		t.Fatalf("normalize failed: %v", err)
	}
	if norm.Standard.PromptCacheHint != "claude;auto:ephemeral:1h" {
		t.Fatalf("unexpected prompt cache hint: %q", norm.Standard.PromptCacheHint)
	}
	if !norm.Standard.PromptPrefixEligible || norm.Standard.PromptPrefixHash == "" || norm.Standard.PromptTailTokens <= 0 {
		t.Fatalf("expected prefix diagnostics, got eligible=%v hash=%q tail=%d", norm.Standard.PromptPrefixEligible, norm.Standard.PromptPrefixHash, norm.Standard.PromptTailTokens)
	}
}

func TestNormalizeClaudeRequestUsesSystemBreakpointForPrefixDiagnostics(t *testing.T) {
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", `{}`)
	store := config.LoadStore()
	base := map[string]any{
		"model": "claude-sonnet-4-6",
		"system": []any{map[string]any{
			"type":          "text",
			"text":          "stable system",
			"cache_control": map[string]any{"type": "ephemeral", "ttl": "1h"},
		}},
	}
	reqA := cloneMap(base)
	reqA["messages"] = []any{
		map[string]any{"role": "user", "content": "volatile history a"},
		map[string]any{"role": "assistant", "content": "ok"},
		map[string]any{"role": "user", "content": "answer now"},
	}
	reqB := cloneMap(base)
	reqB["messages"] = []any{
		map[string]any{"role": "user", "content": "volatile history b"},
		map[string]any{"role": "assistant", "content": "ok"},
		map[string]any{"role": "user", "content": "answer now"},
	}

	normA, err := normalizeClaudeRequest(store, reqA)
	if err != nil {
		t.Fatalf("normalize A failed: %v", err)
	}
	normB, err := normalizeClaudeRequest(store, reqB)
	if err != nil {
		t.Fatalf("normalize B failed: %v", err)
	}
	if !normA.Standard.PromptPrefixEligible || normA.Standard.PromptPrefixHash == "" {
		t.Fatalf("expected eligible system breakpoint prefix: %#v", normA.Standard)
	}
	if normA.Standard.PromptPrefixHash != normB.Standard.PromptPrefixHash {
		t.Fatalf("expected system breakpoint to ignore volatile history, got %q vs %q", normA.Standard.PromptPrefixHash, normB.Standard.PromptPrefixHash)
	}
}

func TestNormalizeClaudeRequestUsesMessageBreakpointForPrefixDiagnostics(t *testing.T) {
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", `{}`)
	store := config.LoadStore()
	baseMessages := []any{
		map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "text", "text": "stable cached block", "cache_control": map[string]any{"type": "ephemeral"}},
		}},
	}
	reqA := map[string]any{
		"model": "claude-sonnet-4-6",
		"messages": append(append([]any{}, baseMessages...),
			map[string]any{"role": "assistant", "content": "volatile answer a"},
			map[string]any{"role": "user", "content": "answer now"},
		),
	}
	reqB := map[string]any{
		"model": "claude-sonnet-4-6",
		"messages": append(append([]any{}, baseMessages...),
			map[string]any{"role": "assistant", "content": "volatile answer b"},
			map[string]any{"role": "user", "content": "answer now"},
		),
	}

	normA, err := normalizeClaudeRequest(store, reqA)
	if err != nil {
		t.Fatalf("normalize A failed: %v", err)
	}
	normB, err := normalizeClaudeRequest(store, reqB)
	if err != nil {
		t.Fatalf("normalize B failed: %v", err)
	}
	if !normA.Standard.PromptPrefixEligible || normA.Standard.PromptPrefixHash == "" {
		t.Fatalf("expected eligible message breakpoint prefix: %#v", normA.Standard)
	}
	if normA.Standard.PromptPrefixHash != normB.Standard.PromptPrefixHash {
		t.Fatalf("expected message breakpoint to ignore later volatile messages, got %q vs %q", normA.Standard.PromptPrefixHash, normB.Standard.PromptPrefixHash)
	}
}
