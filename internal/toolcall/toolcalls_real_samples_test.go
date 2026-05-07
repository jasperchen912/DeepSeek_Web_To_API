package toolcall

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseToolCallsRealSampleCorpus(t *testing.T) {
	tests := []struct {
		name         string
		wantNames    []string
		wantVariant  string
		wantRepaired bool
		assert       func(t *testing.T, result ToolCallParseResult)
	}{
		{
			name:        "curly_dsml_multi_invoke",
			wantNames:   []string{"process", "exec"},
			wantVariant: "dsml",
			assert: func(t *testing.T, result ToolCallParseResult) {
				requireStringArg(t, result.Calls[0], "action", "poll")
				requireStringArg(t, result.Calls[0], "sessionId", "glow-seaslug")
				requireNumberArg(t, result.Calls[0], "timeout", 10000)
				requireArgContains(t, result.Calls[1], "command", `find /Users/jiajunch`)
				requireArgContains(t, result.Calls[1], "command", `deepseekapi*`)
			},
		},
		{
			name:         "markdown_corrupted_python_cdata",
			wantNames:    []string{"exec"},
			wantVariant:  "dsml+repaired_loose_cdata",
			wantRepaired: true,
			assert: func(t *testing.T, result ToolCallParseResult) {
				requireArgContains(t, result.Calls[0], "command", `python3 << 'PYEOF'`)
				requireArgContains(t, result.Calls[0], "command", "me/deepseek-v4-pro")
				requireArgContains(t, result.Calls[0], "command", "All agent model assignments")
				requireArgNotContains(t, result.Calls[0], "command", "CDATA")
				requireNumberArg(t, result.Calls[0], "timeout", 10)
			},
		},
		{
			name:        "nested_dsml_in_write_content",
			wantNames:   []string{"write"},
			wantVariant: "dsml",
			assert: func(t *testing.T, result ToolCallParseResult) {
				requireStringArg(t, result.Calls[0], "path", "/Users/jiajunch/.openclaw/skills/daily-news/tools/global-daily/out/2026-05-05-global-daily.md")
				requireArgContains(t, result.Calls[0], "content", "<|DSML|tool_calls|>")
				requireArgContains(t, result.Calls[0], "content", "@tencent-weixin/openclaw-weixin npm package")
				requireArgContains(t, result.Calls[0], "content", "openclaw-weixin-cli")
				requireArgContains(t, result.Calls[0], "content", "<![CDATA[npm view @tencent-weixin/openclaw-weixin")
			},
		},
		{
			name:        "trailing_pipe_openclaw_lookup",
			wantNames:   []string{"web_search", "exec"},
			wantVariant: "dsml",
			assert: func(t *testing.T, result ToolCallParseResult) {
				requireStringArg(t, result.Calls[0], "query", "@tencent-weixin/openclaw-weixin npm package")
				requireNumberArg(t, result.Calls[0], "count", 5)
				requireArgContains(t, result.Calls[1], "command", "npm view @tencent-weixin/openclaw-weixin")
				requireNumberArg(t, result.Calls[1], "timeout", 15)
			},
		},
		{
			name:         "unclosed_cdata_then_cron",
			wantNames:    []string{"exec", "cron"},
			wantVariant:  "dsml+repaired_loose_cdata",
			wantRepaired: true,
			assert: func(t *testing.T, result ToolCallParseResult) {
				requireArgContains(t, result.Calls[0], "command", "BACKUP_OK")
				requireArgNotContains(t, result.Calls[0], "command", "DSML")
				requireStringArg(t, result.Calls[1], "action", "list")
			},
		},
		{
			name:        "zero_width_fullwidth_multi_search",
			wantNames:   []string{"web_search", "web_search", "web_search"},
			wantVariant: "dsml",
			assert: func(t *testing.T, result ToolCallParseResult) {
				requireNumberArg(t, result.Calls[0], "count", 8)
				requireArgContains(t, result.Calls[0], "query", "Chiang Mai cheapest international schools")
				requireArgContains(t, result.Calls[1], "query", "Spain public school quality ranking")
				requireArgContains(t, result.Calls[2], "query", "Portugal public school digital nomad children")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ParseToolCallsDetailed(loadRealToolCallSample(t, tt.name), nil)
			requireCallNames(t, result.Calls, tt.wantNames)
			if result.Variant != tt.wantVariant {
				t.Fatalf("expected variant %q, got %#v", tt.wantVariant, result)
			}
			if result.Repaired != tt.wantRepaired {
				t.Fatalf("expected repaired=%v, got %#v", tt.wantRepaired, result)
			}
			if result.RejectReason != "" {
				t.Fatalf("expected empty reject reason, got %#v", result)
			}
			tt.assert(t, result)
		})
	}
}

func loadRealToolCallSample(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join("..", "..", "tests", "compat", "fixtures", "toolcalls_real_samples", name+".txt")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read real sample %s failed: %v", name, err)
	}
	return string(b)
}

func requireCallNames(t *testing.T, calls []ParsedToolCall, want []string) {
	t.Helper()
	if len(calls) != len(want) {
		t.Fatalf("expected calls %v, got %#v", want, calls)
	}
	for i, wantName := range want {
		if calls[i].Name != wantName {
			t.Fatalf("call %d: expected name %q, got %#v", i, wantName, calls[i])
		}
	}
}

func requireStringArg(t *testing.T, call ParsedToolCall, key, want string) {
	t.Helper()
	got, ok := call.Input[key].(string)
	if !ok || got != want {
		t.Fatalf("%s.%s: expected %q, got %#v", call.Name, key, want, call.Input[key])
	}
}

func requireArgContains(t *testing.T, call ParsedToolCall, key, needle string) {
	t.Helper()
	got, ok := call.Input[key].(string)
	if !ok || !strings.Contains(got, needle) {
		t.Fatalf("%s.%s: expected string containing %q, got %#v", call.Name, key, needle, call.Input[key])
	}
}

func requireArgNotContains(t *testing.T, call ParsedToolCall, key, needle string) {
	t.Helper()
	got, ok := call.Input[key].(string)
	if !ok || strings.Contains(got, needle) {
		t.Fatalf("%s.%s: expected string without %q, got %#v", call.Name, key, needle, call.Input[key])
	}
}

func requireNumberArg(t *testing.T, call ParsedToolCall, key string, want float64) {
	t.Helper()
	switch got := call.Input[key].(type) {
	case float64:
		if got == want {
			return
		}
	case int:
		if float64(got) == want {
			return
		}
	}
	t.Fatalf("%s.%s: expected numeric %v, got %#v", call.Name, key, want, call.Input[key])
}
