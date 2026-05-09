package promptcompat

import "testing"

func TestAnalyzeOpenAIPromptPrefixStableAcrossDifferentLastUserMessages(t *testing.T) {
	t.Parallel()

	tools := []any{map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        "search_web",
			"description": "Search",
			"parameters":  map[string]any{"type": "object"},
		},
	}}
	baseMessages := []any{
		map[string]any{"role": "system", "content": "You are precise."},
		map[string]any{"role": "user", "content": "Remember this stable context."},
		map[string]any{"role": "assistant", "content": "Noted."},
	}
	a := append(append([]any{}, baseMessages...), map[string]any{"role": "user", "content": "question one"})
	b := append(append([]any{}, baseMessages...), map[string]any{"role": "user", "content": "question two"})

	infoA := AnalyzeOpenAIPromptPrefix(a, tools, "", DefaultToolChoicePolicy(), false, "deepseek-v4-flash")
	infoB := AnalyzeOpenAIPromptPrefix(b, tools, "", DefaultToolChoicePolicy(), false, "deepseek-v4-flash")
	if !infoA.Eligible || !infoB.Eligible {
		t.Fatalf("expected both prompts to be eligible: %#v %#v", infoA, infoB)
	}
	if infoA.Hash == "" || infoA.Hash != infoB.Hash {
		t.Fatalf("expected stable prefix hash, got %q and %q", infoA.Hash, infoB.Hash)
	}
	if infoA.PrefixTokens <= 0 || infoA.TailTokens <= 0 {
		t.Fatalf("expected token estimates, got %#v", infoA)
	}
}

func TestAnalyzeOpenAIPromptPrefixVariesBySystemAndTools(t *testing.T) {
	t.Parallel()

	messages := []any{
		map[string]any{"role": "system", "content": "system a"},
		map[string]any{"role": "user", "content": "hello"},
	}
	systemB := []any{
		map[string]any{"role": "system", "content": "system b"},
		map[string]any{"role": "user", "content": "hello"},
	}
	toolA := []any{map[string]any{"type": "function", "function": map[string]any{"name": "tool_a", "parameters": map[string]any{"type": "object"}}}}
	toolB := []any{map[string]any{"type": "function", "function": map[string]any{"name": "tool_b", "parameters": map[string]any{"type": "object"}}}}

	base := AnalyzeOpenAIPromptPrefix(messages, toolA, "", DefaultToolChoicePolicy(), false, "deepseek-v4-flash")
	if base.Hash == "" {
		t.Fatal("expected base hash")
	}
	if got := AnalyzeOpenAIPromptPrefix(systemB, toolA, "", DefaultToolChoicePolicy(), false, "deepseek-v4-flash"); got.Hash == base.Hash {
		t.Fatal("expected system prompt to affect prefix hash")
	}
	if got := AnalyzeOpenAIPromptPrefix(messages, toolB, "", DefaultToolChoicePolicy(), false, "deepseek-v4-flash"); got.Hash == base.Hash {
		t.Fatal("expected tool schema to affect prefix hash")
	}
	if got := AnalyzeOpenAIPromptPrefix(messages, toolA, "", DefaultToolChoicePolicy(), true, "deepseek-v4-flash"); got.Hash == base.Hash {
		t.Fatal("expected thinking mode to affect prefix hash")
	}
}

func TestAnalyzeOpenAIPromptPrefixSingleUserIsNotEligible(t *testing.T) {
	t.Parallel()

	info := AnalyzeOpenAIPromptPrefix([]any{
		map[string]any{"role": "user", "content": "hello"},
	}, nil, "", DefaultToolChoicePolicy(), false, "deepseek-v4-flash")
	if info.Eligible || info.Hash != "" || info.PrefixTokens != 0 {
		t.Fatalf("expected single pure user request to be ineligible, got %#v", info)
	}
}
