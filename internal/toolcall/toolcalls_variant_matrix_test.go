package toolcall

import (
	"strings"
	"testing"
)

func TestParseToolCallsVariantMatrixMetadata(t *testing.T) {
	tests := []struct {
		name         string
		text         string
		wantCalls    int
		wantVariant  string
		wantRepaired bool
		wantReject   string
	}{
		{
			name:        "canonical dsml",
			text:        `<|DSML|tool_calls><|DSML|invoke name="Bash"><|DSML|parameter name="command">pwd</|DSML|parameter></|DSML|invoke></|DSML|tool_calls>`,
			wantCalls:   1,
			wantVariant: "dsml",
		},
		{
			name:        "collapsed dsml missing pipe",
			text:        `<|DSMLtool_calls><|DSMLinvoke name="Bash"><|DSMLparameter name="command">pwd</|DSMLparameter></|DSMLinvoke></|DSMLtool_calls>`,
			wantCalls:   1,
			wantVariant: "dsml",
		},
		{
			name:        "fullwidth dsml",
			text:        `<｜DSML▁tool_calls｜><｜DSML▁invoke name="process"＞<｜DSML▁parameter name="action"＞poll</｜DSML▁parameter＞</｜DSML▁invoke＞</｜DSML▁tool_calls＞`,
			wantCalls:   1,
			wantVariant: "dsml",
		},
		{
			name:        "chinese dsml wrapper",
			text:        `<|DSML工具调用|><|DSML|invoke name="Bash"><|DSML|parameter name="command">id</|DSML|parameter></|DSML|invoke></|DSML|tool_calls>`,
			wantCalls:   1,
			wantVariant: "dsml",
		},
		{
			name:        "bounded fuzzy dsml wrapper",
			text:        "<\u200dDSML \u2581 | tool_calls data-x=\"1\"><invoke name=\"read_file\"><parameter name=\"path\">README.md</parameter></invoke>< / DSML \u2581 | tool_calls >",
			wantCalls:   1,
			wantVariant: "mixed_dsml_xml",
		},
		{
			name:         "malformed cdata close",
			text:         `<|DSMLtool_calls><|DSMLinvoke name="Bash"><|DSMLparameter name="command"><![CDATA[echo hi]]</|DSMLparameter></|DSMLinvoke></|DSMLtool_calls>`,
			wantCalls:    1,
			wantVariant:  "dsml+repaired_loose_cdata",
			wantRepaired: true,
		},
		{
			name:        "multiple canonical blocks",
			text:        `<tool_calls><invoke name="read_file"><parameter name="path">a.txt</parameter></invoke></tool_calls>` + "\n" + `<tool_calls><invoke name="search"><parameter name="q">go</parameter></invoke></tool_calls>`,
			wantCalls:   2,
			wantVariant: "canonical_xml",
		},
		{
			name:         "missing opening wrapper with strong close evidence",
			text:         `<invoke name="read_file"><parameter name="path">README.md</parameter></invoke></tool_calls>`,
			wantCalls:    1,
			wantVariant:  "canonical_xml+repaired_missing_wrapper",
			wantRepaired: true,
		},
		{
			name:        "fenced example only",
			text:        "```xml\n<tool_calls><invoke name=\"read_file\"><parameter name=\"path\">README.md</parameter></invoke></tool_calls>\n```",
			wantVariant: "fenced_tool_call_example",
			wantReject:  "no_tool_call_outside_fence",
		},
		{
			name:        "bare invoke low confidence",
			text:        `<invoke name="read_file"><parameter name="path">README.md</parameter></invoke>`,
			wantVariant: "canonical_xml",
			wantReject:  "no_tool_call_wrapper",
		},
		{
			name:        "bare singular tool call low confidence",
			text:        `<tool_call><invoke name="read_file"><parameter name="path">README.md</parameter></invoke></tool_call>`,
			wantVariant: "canonical_xml",
			wantReject:  "no_tool_call_wrapper",
		},
		{
			name:        "attribute mention of dsml tool calls is low confidence",
			text:        `<note data="DSML | tool_calls"><invoke name="read_file"><parameter name="path">README.md</parameter></invoke></note>`,
			wantVariant: "canonical_xml",
			wantReject:  "no_tool_call_wrapper",
		},
		{
			name:        "ordinary tag name containing dsml tool calls is rejected",
			text:        `<abcDSML_tool_calls><invoke name="read_file"><parameter name="path">README.md</parameter></invoke></abcDSML_tool_calls>`,
			wantVariant: "canonical_xml",
			wantReject:  "no_tool_call_wrapper",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseToolCallsDetailed(tt.text, nil)
			if len(got.Calls) != tt.wantCalls {
				t.Fatalf("expected %d calls, got %#v", tt.wantCalls, got)
			}
			if got.Variant != tt.wantVariant {
				t.Fatalf("expected variant %q, got %#v", tt.wantVariant, got)
			}
			if got.Repaired != tt.wantRepaired {
				t.Fatalf("expected repaired=%v, got %#v", tt.wantRepaired, got)
			}
			if got.RejectReason != tt.wantReject {
				t.Fatalf("expected reject reason %q, got %#v", tt.wantReject, got)
			}
		})
	}
}

func TestParseAssistantToolCallsDetailedUsesThinkingMetadata(t *testing.T) {
	thinking := strings.Join([]string{
		"<tool_calls>",
		"<invoke name=\"read_file\">",
		"<parameter name=\"path\">README.md</parameter>",
		"</invoke>",
		"</tool_calls>",
	}, "\n")
	got := ParseAssistantToolCallsDetailed("", thinking, []string{"read_file"})
	if len(got.Calls) != 1 {
		t.Fatalf("expected hidden-thinking tool call, got %#v", got)
	}
	if got.Variant != "canonical_xml" || got.RejectReason != "" {
		t.Fatalf("expected canonical thinking metadata, got %#v", got)
	}
}
