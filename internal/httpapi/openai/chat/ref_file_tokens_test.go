package chat

import "testing"

func TestAddRefFileTokensUpdatesOpenClawUsageAliases(t *testing.T) {
	obj := map[string]any{
		"usage": map[string]any{
			"prompt_tokens": 10,
			"total_tokens":  15,
			"input":         10,
			"totalTokens":   15,
		},
	}

	addRefFileTokensToUsage(obj, 7)

	usage := obj["usage"].(map[string]any)
	if usage["prompt_tokens"] != 17 {
		t.Fatalf("expected prompt_tokens to include ref files, got %#v", usage)
	}
	if usage["total_tokens"] != 22 || usage["totalTokens"] != 22 {
		t.Fatalf("expected total token aliases to include ref files, got %#v", usage)
	}
	if usage["input"] != 17 {
		t.Fatalf("expected input alias to include ref files, got %#v", usage)
	}
}
