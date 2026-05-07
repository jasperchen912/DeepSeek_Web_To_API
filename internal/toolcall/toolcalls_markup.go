package toolcall

import (
	"encoding/json"
	"html"
	"regexp"
	"strings"
)

var toolCallMarkupKVPattern = regexp.MustCompile(`(?is)<(?:[a-z0-9_:-]+:)?([a-z0-9_\-.]+)\b[^>]*>(.*?)</(?:[a-z0-9_:-]+:)?([a-z0-9_\-.]+)>`)

// cdataPattern matches a standalone CDATA section.
var cdataPattern = regexp.MustCompile(`(?is)^<!\[CDATA\[(.*?)]](?:>|＞)$`)
var markdownCDATAOpenPattern = regexp.MustCompile(`(?is)<\*+!\[CDATA\[`)
var markdownCDATAStandalonePattern = regexp.MustCompile(`(?is)^<\*+!\[CDATA\[(.*?)\*+>$`)

func parseMarkupKVObject(text string) map[string]any {
	matches := toolCallMarkupKVPattern.FindAllStringSubmatch(strings.TrimSpace(text), -1)
	if len(matches) == 0 {
		return nil
	}
	out := map[string]any{}
	for _, m := range matches {
		if len(m) < 4 {
			continue
		}
		key := strings.TrimSpace(m[1])
		endKey := strings.TrimSpace(m[3])
		if key == "" {
			continue
		}
		if !strings.EqualFold(key, endKey) {
			continue
		}
		value := parseMarkupValue(m[2])
		if value == nil {
			continue
		}
		appendMarkupValue(out, key, value)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func parseMarkupValue(inner string) any {
	if value, ok := extractStandaloneCDATA(inner); ok {
		return value
	}
	value := strings.TrimSpace(extractRawTagValue(inner))
	if value == "" {
		return ""
	}

	if strings.Contains(value, "<") && strings.Contains(value, ">") {
		if parsed := parseStructuredToolCallInput(value); len(parsed) > 0 {
			if len(parsed) == 1 {
				if raw, ok := parsed["_raw"].(string); ok {
					return raw
				}
			}
			return parsed
		}
	}

	var jsonValue any
	if json.Unmarshal([]byte(value), &jsonValue) == nil {
		return jsonValue
	}
	return value
}

func appendMarkupValue(out map[string]any, key string, value any) {
	if existing, ok := out[key]; ok {
		switch current := existing.(type) {
		case []any:
			out[key] = append(current, value)
		default:
			out[key] = []any{current, value}
		}
		return
	}
	out[key] = value
}

// extractRawTagValue treats the inner content of a tag robustly.
// It detects CDATA and strips it, otherwise it unescapes standard HTML entities.
// It avoids over-aggressive tag stripping that might break user content.
func extractRawTagValue(inner string) string {
	trimmed := strings.TrimSpace(inner)
	if trimmed == "" {
		return ""
	}

	// 1. Check for CDATA - if present, it's the ultimate "safe" container.
	if value, ok := extractStandaloneCDATA(trimmed); ok {
		return value // Return raw content between CDATA brackets
	}

	// 2. If no CDATA, we still want to be robust.
	// We unescape standard HTML entities (like &lt; &gt; &amp;)
	// but we DON'T recursively strip tags unless they are actually valid XML tags
	// at the start/end (which should have been handled by the outer matcher anyway).

	// If it contains what looks like a single tag and no other text, it might be nested XML
	// but for KV objects we usually want the value.
	return html.UnescapeString(inner)
}

func extractStandaloneCDATA(inner string) (string, bool) {
	trimmed := strings.TrimSpace(inner)
	if cdataMatches := cdataPattern.FindStringSubmatch(trimmed); len(cdataMatches) >= 2 {
		return cdataMatches[1], true
	}
	if cdataMatches := markdownCDATAStandalonePattern.FindStringSubmatch(trimmed); len(cdataMatches) >= 2 {
		return cdataMatches[1], true
	}
	if strings.HasPrefix(strings.ToLower(trimmed), "<![cdata[") {
		return trimmed[len("<![CDATA["):], true
	}
	return "", false
}

func containsLooseCDATAOpening(text string) bool {
	if text == "" {
		return false
	}
	if strings.Contains(strings.ToLower(text), "<![cdata[") {
		return true
	}
	return markdownCDATAOpenPattern.MatchString(text)
}

func parseJSONLiteralValue(raw string) (any, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, false
	}

	switch trimmed[0] {
	case '{', '[', '"', '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9', 't', 'f', 'n':
	default:
		return nil, false
	}

	var parsed any
	if err := json.Unmarshal([]byte(trimmed), &parsed); err != nil {
		return nil, false
	}
	return parsed, true
}

// SanitizeLooseCDATA repairs malformed trailing CDATA openings just enough for
// final parsing and flush-time recovery. Properly closed CDATA blocks are left
// untouched; an unclosed opener is stripped so the remaining text can still be
// parsed as part of the surrounding tool markup.
func SanitizeLooseCDATA(text string) string {
	if text == "" {
		return ""
	}

	normalized := markdownCDATAOpenPattern.ReplaceAllString(text, "<![CDATA[")
	changed := normalized != text
	text = normalized
	lower := strings.ToLower(text)
	const openMarker = "<![cdata["
	const closeMarker = "]]>"

	var b strings.Builder
	b.Grow(len(text))
	pos := 0
	for pos < len(text) {
		startRel := strings.Index(lower[pos:], openMarker)
		if startRel < 0 {
			b.WriteString(text[pos:])
			break
		}
		start := pos + startRel
		contentStart := start + len(openMarker)
		b.WriteString(text[pos:start])

		properRel, properLen, properFullwidth := findProperCDATAClose(text, contentStart)
		nestedOpenRel := strings.Index(lower[contentStart:], openMarker)
		looseRel := findLooseCDATAClose(text, contentStart)
		markdownRel := findMarkdownCDATAClose(text, contentStart)
		parameterCloseRel := findUnclosedCDATAParameterClose(text, contentStart)
		missingLTParameterCloseRel := findMissingLTCDATAParameterClose(text, contentStart)
		earliestParameterCloseRel := minNonNegative(parameterCloseRel, missingLTParameterCloseRel)
		if properRel >= 0 &&
			nestedOpenRel >= 0 && nestedOpenRel < properRel &&
			earliestParameterCloseRel >= 0 && earliestParameterCloseRel < nestedOpenRel {
			properRel = -1
		}

		// Pick the earliest close. "]]>" wins on tie since it's the canonical form.
		closePos := -1
		closeKind := 0
		switch {
		case properRel >= 0:
			closePos = contentStart + properRel
			closeKind = 1
		case looseRel >= 0 && earliestCandidate(looseRel, markdownRel, parameterCloseRel, missingLTParameterCloseRel):
			closePos = contentStart + looseRel
			closeKind = 2
		case markdownRel >= 0 && earliestCandidate(markdownRel, parameterCloseRel, missingLTParameterCloseRel):
			closePos = contentStart + markdownRel
			closeKind = 3
		case parameterCloseRel >= 0 && earliestCandidate(parameterCloseRel, missingLTParameterCloseRel):
			closePos = contentStart + parameterCloseRel
			closeKind = 4
		case missingLTParameterCloseRel >= 0:
			closePos = contentStart + missingLTParameterCloseRel
			closeKind = 5
		}

		switch {
		case closePos < 0:
			// No close marker at all — strip the opener so the rest can still parse.
			changed = true
			b.WriteString(text[contentStart:])
			pos = len(text)
		case closeKind == 2:
			// Model emitted "]]<TAG" instead of "]]><TAG". Reproduce the
			// opener + content + "]]" then synthesize the missing ">".
			// "<TAG" at pos+2 is left for the next loop iteration to handle
			// as a regular tag start.
			changed = true
			b.WriteString(text[start:closePos]) // includes "<![CDATA[" + content
			b.WriteString("]]>")
			pos = closePos + 2 // skip "]]"
		case closeKind == 3:
			// Some Markdown renderers/models corrupt "<![CDATA[...]]>" into
			// "<**![CDATA[...**>". Treat the paired stars as the CDATA shell
			// only when they are immediately followed by the next markup tag.
			changed = true
			b.WriteString(text[start:closePos])
			b.WriteString("]]>")
			pos = closePos
			for pos < len(text) && text[pos] == '*' {
				pos++
			}
			if pos < len(text) && text[pos] == '>' {
				pos++
			}
		case closeKind == 4:
			// Model omitted the CDATA close before the parameter close tag.
			// Synthesize it and leave the closing parameter tag to be copied on
			// the next pass so following tool blocks are not swallowed as text.
			changed = true
			b.WriteString(text[start:closePos])
			b.WriteString("]]>")
			pos = closePos
		case closeKind == 5:
			// Model emitted only the ">" from "]]>" and omitted the "<" from
			// the following parameter close, yielding "...value>/DSML|parameter>".
			// Recreate both delimiters while preserving the slash/tag text.
			changed = true
			b.WriteString(text[start:closePos])
			b.WriteString("]]><")
			pos = closePos + 1
		default:
			if properFullwidth {
				changed = true
				b.WriteString(text[start:closePos])
				b.WriteString(closeMarker)
			} else {
				b.WriteString(text[start : closePos+properLen])
			}
			pos = closePos + properLen
		}
	}

	if !changed {
		return text
	}
	return b.String()
}

func findProperCDATAClose(text string, from int) (rel int, markerLen int, fullwidth bool) {
	if from >= len(text) {
		return -1, 0, false
	}
	const asciiClose = "]]>"
	const fullwidthClose = "]]＞"
	asciiRel := strings.Index(text[from:], asciiClose)
	fullwidthRel := strings.Index(text[from:], fullwidthClose)
	switch {
	case asciiRel < 0 && fullwidthRel < 0:
		return -1, 0, false
	case asciiRel >= 0 && (fullwidthRel < 0 || asciiRel <= fullwidthRel):
		return asciiRel, len(asciiClose), false
	default:
		return fullwidthRel, len(fullwidthClose), true
	}
}

func earliestCandidate(candidate int, others ...int) bool {
	for _, other := range others {
		if other >= 0 && other < candidate {
			return false
		}
	}
	return true
}

func minNonNegative(values ...int) int {
	out := -1
	for _, v := range values {
		if v < 0 {
			continue
		}
		if out < 0 || v < out {
			out = v
		}
	}
	return out
}

// findLooseCDATAClose returns the relative offset of "]]<TAG" inside text[from:],
// where "<TAG" is heuristically a real tag start (letter, '/', '|', or '｜'
// follows). Used to recover from the common model bug of emitting "]]<" when
// the canonical close is "]]>".
func findLooseCDATAClose(text string, from int) int {
	if from >= len(text) {
		return -1
	}
	for i := from; i+2 < len(text); i++ {
		if text[i] != ']' || text[i+1] != ']' || text[i+2] != '<' {
			continue
		}
		if isLikelyTagStartAt(text, i+2) {
			return i - from
		}
	}
	return -1
}

func findMarkdownCDATAClose(text string, from int) int {
	if from >= len(text) {
		return -1
	}
	for i := from; i < len(text); i++ {
		if text[i] != '*' {
			continue
		}
		j := i
		for j < len(text) && text[j] == '*' {
			j++
		}
		if j-i < 2 || j >= len(text) || text[j] != '>' {
			continue
		}
		if isLikelyCDATAEndFollowerAt(text, j+1) {
			return i - from
		}
	}
	return -1
}

func isLikelyCDATAEndFollowerAt(text string, idx int) bool {
	if idx+1 >= len(text) || text[idx] != '<' || text[idx+1] != '/' {
		return false
	}
	return isLikelyTagStartAt(text, idx)
}

func findUnclosedCDATAParameterClose(text string, from int) int {
	if from >= len(text) {
		return -1
	}
	for i := from; i < len(text); i++ {
		if text[i] != '<' {
			continue
		}
		tag, ok := scanToolMarkupTagAt(text, i)
		if !ok {
			continue
		}
		if tag.Closing && canonicalToolMarkupName(tag.Name) == "parameter" {
			return i - from
		}
		i = tag.End
	}
	return -1
}

func findMissingLTCDATAParameterClose(text string, from int) int {
	if from >= len(text) {
		return -1
	}
	for i := from; i+1 < len(text); i++ {
		if text[i] != '>' || text[i+1] != '/' {
			continue
		}
		candidate := "<" + text[i+1:]
		tag, ok := scanToolMarkupTagAt(candidate, 0)
		if !ok {
			continue
		}
		if tag.Closing && canonicalToolMarkupName(tag.Name) == "parameter" {
			return i - from
		}
	}
	return -1
}

func isLikelyTagStartAt(text string, idx int) bool {
	if idx >= len(text) || text[idx] != '<' {
		return false
	}
	rest := text[idx+1:]
	if rest == "" {
		return false
	}
	if rest[0] == '/' {
		rest = rest[1:]
	}
	if rest == "" {
		return false
	}
	c := rest[0]
	if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_' {
		return true
	}
	if c == '|' {
		return true
	}
	if strings.HasPrefix(rest, "｜") {
		return true
	}
	return false
}
