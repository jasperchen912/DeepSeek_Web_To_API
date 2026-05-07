package toolstream

import (
	"strings"
	"testing"
)

func TestSieveVariantMatrixChineseDSMLWrapper(t *testing.T) {
	var state State
	chunks := []string{
		"准备执行。\n",
		"<|DSML工具调用|>\n",
		"<|DSML|invoke name=\"Bash\">\n",
		"<|DSML|parameter name=\"command\">id</|DSML|parameter>\n",
		"</|DSML|invoke>\n",
		"</|DSML|tool_calls>",
	}

	var calls int
	var text strings.Builder
	for _, chunk := range chunks {
		for _, evt := range ProcessChunk(&state, chunk, []string{"Bash"}) {
			text.WriteString(evt.Content)
			calls += len(evt.ToolCalls)
		}
	}
	for _, evt := range Flush(&state, []string{"Bash"}) {
		text.WriteString(evt.Content)
		calls += len(evt.ToolCalls)
	}

	if calls != 1 {
		t.Fatalf("expected one Chinese DSML tool call, got %d text=%q", calls, text.String())
	}
	if strings.Contains(strings.ToLower(text.String()), "dsml") || strings.Contains(text.String(), "工具调用") {
		t.Fatalf("expected DSML wrapper to stay buffered, got text %q", text.String())
	}
}

func TestSieveVariantMatrixFuzzyDSMLWrapperAcrossChunks(t *testing.T) {
	var state State
	chunks := []string{
		"准备读取。\n",
		"<\u200dDS",
		"ML \u2581 | tool_calls>\n",
		"<invoke name=\"read_file\"><parameter name=\"path\">README.md</parameter></invoke>\n",
		"< / DSML \u2581 | tool_calls >",
	}

	var calls int
	var text strings.Builder
	for _, chunk := range chunks {
		for _, evt := range ProcessChunk(&state, chunk, []string{"read_file"}) {
			text.WriteString(evt.Content)
			calls += len(evt.ToolCalls)
		}
	}
	for _, evt := range Flush(&state, []string{"read_file"}) {
		text.WriteString(evt.Content)
		calls += len(evt.ToolCalls)
	}

	if calls != 1 {
		t.Fatalf("expected one fuzzy DSML tool call, got %d text=%q", calls, text.String())
	}
	if strings.Contains(strings.ToLower(text.String()), "dsml") || strings.Contains(text.String(), "tool_calls") {
		t.Fatalf("expected fuzzy DSML wrapper to stay buffered, got text %q", text.String())
	}
}

func TestSieveVariantMatrixFencedToolCallReleasedAsText(t *testing.T) {
	var state State
	input := strings.Join([]string{
		"```xml",
		"<tool_calls><invoke name=\"read_file\"><parameter name=\"path\">README.md</parameter></invoke></tool_calls>",
		"```",
	}, "\n")

	var calls int
	var text strings.Builder
	for _, evt := range ProcessChunk(&state, input, []string{"read_file"}) {
		text.WriteString(evt.Content)
		calls += len(evt.ToolCalls)
	}
	for _, evt := range Flush(&state, []string{"read_file"}) {
		text.WriteString(evt.Content)
		calls += len(evt.ToolCalls)
	}

	if calls != 0 {
		t.Fatalf("expected fenced tool-call example to remain text, got %d calls", calls)
	}
	if !strings.Contains(text.String(), "<tool_calls>") {
		t.Fatalf("expected fenced example text to be preserved, got %q", text.String())
	}
}
