package toolcall

import (
	"strings"
)

type ParsedToolCall struct {
	Name  string         `json:"name"`
	Input map[string]any `json:"input"`
}

type ToolCallParseResult struct {
	Calls             []ParsedToolCall
	SawToolCallSyntax bool
	RejectedByPolicy  bool
	RejectedToolNames []string
	Repaired          bool
	Variant           string
	RejectReason      string
}

func ParseToolCalls(text string, availableToolNames []string) []ParsedToolCall {
	return ParseToolCallsDetailed(text, availableToolNames).Calls
}

func ParseToolCallsDetailed(text string, availableToolNames []string) ToolCallParseResult {
	return parseToolCallsDetailedXMLOnly(text)
}

func ParseStandaloneToolCalls(text string, availableToolNames []string) []ParsedToolCall {
	return ParseStandaloneToolCallsDetailed(text, availableToolNames).Calls
}

func ParseStandaloneToolCallsDetailed(text string, availableToolNames []string) ToolCallParseResult {
	return parseToolCallsDetailedXMLOnly(text)
}

func ParseAssistantToolCallsDetailed(text, thinking string, availableToolNames []string) ToolCallParseResult {
	textParsed := ParseStandaloneToolCallsDetailed(text, availableToolNames)
	if len(textParsed.Calls) > 0 {
		return textParsed
	}
	if strings.TrimSpace(text) != "" {
		return textParsed
	}
	thinkingParsed := ParseStandaloneToolCallsDetailed(thinking, availableToolNames)
	if len(thinkingParsed.Calls) > 0 {
		return thinkingParsed
	}
	return textParsed
}

func parseToolCallsDetailedXMLOnly(text string) ToolCallParseResult {
	result := ToolCallParseResult{}
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		result.RejectReason = "empty_input"
		return result
	}
	result.SawToolCallSyntax = looksLikeToolCallSyntax(trimmed)
	result.Variant = classifyToolCallVariant(trimmed)
	if !result.SawToolCallSyntax {
		result.RejectReason = "no_tool_call_wrapper"
		return result
	}
	trimmed = stripFencedCodeBlocks(trimmed)
	trimmed = strings.TrimSpace(trimmed)
	if trimmed == "" {
		result.Variant = "fenced_tool_call_example"
		result.RejectReason = "no_tool_call_outside_fence"
		return result
	}
	if !looksLikeToolCallSyntax(trimmed) {
		result.Variant = "fenced_tool_call_example"
		result.RejectReason = "no_tool_call_outside_fence"
		return result
	}

	// Repair malformed CDATA closes ("]]<TAG" → "]]><TAG") BEFORE the
	// DSML-rewrite pass, otherwise the rewriter sees an unclosed CDATA and
	// stops rewriting the rest of the document, leaving downstream tags in
	// non-canonical form.
	if containsLooseCDATAOpening(trimmed) {
		if repaired := SanitizeLooseCDATA(trimmed); repaired != trimmed {
			trimmed = repaired
			result.Repaired = true
			result.Variant = withRepairVariant(result.Variant, "loose_cdata")
		}
	}

	normalized, ok := normalizeDSMLToolCallMarkup(trimmed)
	if !ok {
		result.RejectReason = "normalize_failed"
		return result
	}
	xmlParsed := parseXMLToolCallsWithMetadata(normalized)
	parsed := xmlParsed.Calls
	if xmlParsed.Repaired {
		result.Repaired = true
		result.Variant = withRepairVariant(result.Variant, xmlParsed.RepairKind)
	}
	if len(parsed) == 0 && containsLooseCDATAOpening(normalized) {
		recovered := SanitizeLooseCDATA(normalized)
		if recovered != normalized {
			xmlParsed = parseXMLToolCallsWithMetadata(recovered)
			parsed = xmlParsed.Calls
			result.Repaired = true
			result.Variant = withRepairVariant(result.Variant, "loose_cdata")
			if xmlParsed.Repaired {
				result.Variant = withRepairVariant(result.Variant, xmlParsed.RepairKind)
			}
		}
	}
	if len(parsed) == 0 {
		result.RejectReason = "parse_failed"
		return result
	}

	result.SawToolCallSyntax = true
	calls, rejectedNames := filterToolCallsDetailed(parsed)
	result.Calls = calls
	result.RejectedToolNames = rejectedNames
	result.RejectedByPolicy = len(rejectedNames) > 0 && len(calls) == 0
	switch {
	case result.RejectedByPolicy:
		result.RejectReason = "policy_rejected"
	case len(calls) == 0:
		result.RejectReason = "empty_arguments"
	default:
		result.RejectReason = ""
	}
	return result
}

func classifyToolCallVariant(text string) string {
	hasDSML, hasCanonical := ContainsToolMarkupSyntaxOutsideIgnored(text)
	switch {
	case hasDSML && hasCanonical:
		return "mixed_dsml_xml"
	case hasDSML:
		return "dsml"
	case hasCanonical:
		return "canonical_xml"
	default:
		return "unknown"
	}
}

func withRepairVariant(base, repair string) string {
	repair = strings.TrimSpace(repair)
	if repair == "" {
		return base
	}
	base = strings.TrimSpace(base)
	if base == "" || base == "unknown" {
		return "repaired_" + repair
	}
	if strings.Contains(base, "repaired_"+repair) {
		return base
	}
	return base + "+repaired_" + repair
}

func filterToolCallsDetailed(parsed []ParsedToolCall) ([]ParsedToolCall, []string) {
	out := make([]ParsedToolCall, 0, len(parsed))
	for _, tc := range parsed {
		if tc.Name == "" {
			continue
		}
		if tc.Input == nil {
			tc.Input = map[string]any{}
		}
		if len(tc.Input) > 0 && !toolCallInputHasMeaningfulValue(tc.Input) {
			continue
		}
		out = append(out, tc)
	}
	return out, nil
}

func toolCallInputHasMeaningfulValue(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case string:
		return strings.TrimSpace(x) != ""
	case map[string]any:
		if len(x) == 0 {
			return false
		}
		for _, child := range x {
			if toolCallInputHasMeaningfulValue(child) {
				return true
			}
		}
		return false
	case []any:
		if len(x) == 0 {
			return false
		}
		for _, child := range x {
			if toolCallInputHasMeaningfulValue(child) {
				return true
			}
		}
		return false
	default:
		return true
	}
}

func looksLikeToolCallSyntax(text string) bool {
	hasDSML, hasCanonical := ContainsToolCallWrapperSyntaxOutsideIgnored(text)
	return hasDSML || hasCanonical
}

func stripFencedCodeBlocks(text string) string {
	if text == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(text))

	lines := strings.SplitAfter(text, "\n")
	inFence := false
	fenceMarker := ""
	inCDATA := false
	// Track builder length when a fence opens so we can preserve content
	// collected before the unclosed fence.
	beforeFenceLen := 0
	for _, line := range lines {
		if inCDATA || cdataStartsBeforeFence(line) {
			b.WriteString(line)
			inCDATA = updateCDATAState(inCDATA, line)
			continue
		}
		trimmed := strings.TrimLeft(line, " \t")
		if !inFence {
			if marker, ok := parseFenceOpen(trimmed); ok {
				inFence = true
				fenceMarker = marker
				beforeFenceLen = b.Len()
				continue
			}
			b.WriteString(line)
			continue
		}

		if isFenceClose(trimmed, fenceMarker) {
			inFence = false
			fenceMarker = ""
		}
	}

	if inFence {
		// Unclosed fence: preserve content that was collected before the
		// fence started rather than dropping everything.
		result := b.String()
		if beforeFenceLen > 0 && beforeFenceLen <= len(result) {
			return result[:beforeFenceLen]
		}
		return ""
	}
	return b.String()
}

func cdataStartsBeforeFence(line string) bool {
	cdataIdx := strings.Index(strings.ToLower(line), "<![cdata[")
	if cdataIdx < 0 {
		return false
	}
	fenceIdx := firstFenceMarkerIndex(line)
	return fenceIdx < 0 || cdataIdx < fenceIdx
}

func firstFenceMarkerIndex(line string) int {
	idxBacktick := strings.Index(line, "```")
	idxTilde := strings.Index(line, "~~~")
	switch {
	case idxBacktick < 0:
		return idxTilde
	case idxTilde < 0:
		return idxBacktick
	case idxBacktick < idxTilde:
		return idxBacktick
	default:
		return idxTilde
	}
}

func updateCDATAState(inCDATA bool, line string) bool {
	lower := strings.ToLower(line)
	pos := 0
	state := inCDATA
	for pos < len(lower) {
		if state {
			end := strings.Index(lower[pos:], "]]>")
			if end < 0 {
				return true
			}
			pos += end + len("]]>")
			state = false
			continue
		}
		start := strings.Index(lower[pos:], "<![cdata[")
		if start < 0 {
			return false
		}
		pos += start + len("<![cdata[")
		state = true
	}
	return state
}

func parseFenceOpen(line string) (string, bool) {
	if len(line) < 3 {
		return "", false
	}
	ch := line[0]
	if ch != '`' && ch != '~' {
		return "", false
	}
	count := countLeadingFenceChars(line, ch)
	if count < 3 {
		return "", false
	}
	return strings.Repeat(string(ch), count), true
}

func isFenceClose(line, marker string) bool {
	if marker == "" {
		return false
	}
	ch := marker[0]
	if line == "" || line[0] != ch {
		return false
	}
	count := countLeadingFenceChars(line, ch)
	if count < len(marker) {
		return false
	}
	rest := strings.TrimSpace(line[count:])
	return rest == ""
}

func countLeadingFenceChars(line string, ch byte) int {
	count := 0
	for count < len(line) && line[count] == ch {
		count++
	}
	return count
}
