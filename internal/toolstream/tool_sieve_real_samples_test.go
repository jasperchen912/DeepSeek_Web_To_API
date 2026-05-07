package toolstream

import (
	"DeepSeek_Web_To_API/internal/toolcall"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSieveRealSampleCorpusDoesNotLeak(t *testing.T) {
	tests := []struct {
		name          string
		wantNames     []string
		forbiddenText []string
		assert        func(t *testing.T, calls []toolcall.ParsedToolCall)
	}{
		{
			name:          "markdown_corrupted_python_cdata",
			wantNames:     []string{"exec"},
			forbiddenText: []string{"DSML", "python3", "me/deepseek-v4-pro"},
			assert: func(t *testing.T, calls []toolcall.ParsedToolCall) {
				requireSieveArgContains(t, calls[0], "command", "me/deepseek-v4-pro")
				requireSieveArgNotContains(t, calls[0], "command", "CDATA")
			},
		},
		{
			name:          "nested_dsml_in_write_content",
			wantNames:     []string{"write"},
			forbiddenText: []string{"DSML", "global-daily", "openclaw-weixin"},
			assert: func(t *testing.T, calls []toolcall.ParsedToolCall) {
				requireSieveArgContains(t, calls[0], "content", "<|DSML|tool_calls|>")
				requireSieveArgContains(t, calls[0], "content", "openclaw-weixin-cli")
			},
		},
		{
			name:          "trailing_pipe_openclaw_lookup",
			wantNames:     []string{"web_search", "exec"},
			forbiddenText: []string{"DSML", "openclaw-weixin", "npm view"},
			assert: func(t *testing.T, calls []toolcall.ParsedToolCall) {
				requireSieveArgContains(t, calls[0], "query", "@tencent-weixin/openclaw-weixin npm package")
				requireSieveArgContains(t, calls[1], "command", "npm view @tencent-weixin/openclaw-weixin")
			},
		},
		{
			name:          "unclosed_cdata_then_cron",
			wantNames:     []string{"exec", "cron"},
			forbiddenText: []string{"DSML", "BACKUP_OK", "cron"},
			assert: func(t *testing.T, calls []toolcall.ParsedToolCall) {
				requireSieveArgContains(t, calls[0], "command", "BACKUP_OK")
				requireSieveArgNotContains(t, calls[0], "command", "DSML")
				requireSieveStringArg(t, calls[1], "action", "list")
			},
		},
		{
			name:          "zero_width_fullwidth_multi_search",
			wantNames:     []string{"web_search", "web_search", "web_search"},
			forbiddenText: []string{"DSML", "tool_calls", "Chiang Mai"},
			assert: func(t *testing.T, calls []toolcall.ParsedToolCall) {
				requireSieveArgContains(t, calls[0], "query", "Chiang Mai cheapest international schools")
				requireSieveArgContains(t, calls[2], "query", "Portugal public school digital nomad children")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls, text := runRealSampleThroughSieve(t, tt.name)
			requireSieveCallNames(t, calls, tt.wantNames)
			for _, forbidden := range tt.forbiddenText {
				if strings.Contains(text, forbidden) {
					t.Fatalf("sample %s leaked %q to text: %q", tt.name, forbidden, text)
				}
			}
			tt.assert(t, calls)
		})
	}
}

func runRealSampleThroughSieve(t *testing.T, name string) ([]toolcall.ParsedToolCall, string) {
	t.Helper()
	var state State
	var calls []toolcall.ParsedToolCall
	var text strings.Builder
	for _, chunk := range chunkRealSample(loadRealSieveSample(t, name)) {
		for _, evt := range ProcessChunk(&state, chunk, nil) {
			text.WriteString(evt.Content)
			calls = append(calls, evt.ToolCalls...)
		}
	}
	for _, evt := range Flush(&state, nil) {
		text.WriteString(evt.Content)
		calls = append(calls, evt.ToolCalls...)
	}
	return calls, text.String()
}

func loadRealSieveSample(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join("..", "..", "tests", "compat", "fixtures", "toolcalls_real_samples", name+".txt")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read real sample %s failed: %v", name, err)
	}
	return string(b)
}

func chunkRealSample(s string) []string {
	if s == "" {
		return nil
	}
	runes := []rune(s)
	sizes := []int{1, 2, 5, 13, 37, 89}
	chunks := make([]string, 0, len(runes)/13+1)
	for pos, n := 0, 0; pos < len(runes); n++ {
		size := sizes[n%len(sizes)]
		end := pos + size
		if end > len(runes) {
			end = len(runes)
		}
		chunks = append(chunks, string(runes[pos:end]))
		pos = end
	}
	return chunks
}

func requireSieveCallNames(t *testing.T, calls []toolcall.ParsedToolCall, want []string) {
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

func requireSieveStringArg(t *testing.T, call toolcall.ParsedToolCall, key, want string) {
	t.Helper()
	got, ok := call.Input[key].(string)
	if !ok || got != want {
		t.Fatalf("%s.%s: expected %q, got %#v", call.Name, key, want, call.Input[key])
	}
}

func requireSieveArgContains(t *testing.T, call toolcall.ParsedToolCall, key, needle string) {
	t.Helper()
	got, ok := call.Input[key].(string)
	if !ok || !strings.Contains(got, needle) {
		t.Fatalf("%s.%s: expected string containing %q, got %#v", call.Name, key, needle, call.Input[key])
	}
}

func requireSieveArgNotContains(t *testing.T, call toolcall.ParsedToolCall, key, needle string) {
	t.Helper()
	got, ok := call.Input[key].(string)
	if !ok || strings.Contains(got, needle) {
		t.Fatalf("%s.%s: expected string without %q, got %#v", call.Name, key, needle, call.Input[key])
	}
}
