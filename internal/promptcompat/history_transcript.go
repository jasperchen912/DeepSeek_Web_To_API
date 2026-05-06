package promptcompat

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

const CurrentInputContextFilename = "DEEPSEEK_WEB_TO_API_HISTORY.txt"

const historyTranscriptTitle = "# DEEPSEEK_WEB_TO_API_HISTORY.txt"
const historyTranscriptSummary = "Prior conversation history and tool progress."

func BuildOpenAIHistoryTranscript(messages []any) string {
	return buildOpenAIHistoryTranscript(messages, false)
}

func BuildOpenAICurrentUserInputTranscript(text string) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	return buildOpenAIHistoryTranscript([]any{
		map[string]any{"role": "user", "content": text},
	}, false)
}

func BuildOpenAICurrentInputContextTranscript(messages []any) string {
	return buildOpenAIHistoryTranscript(messages, true)
}

func buildOpenAIHistoryTranscript(messages []any, canonicalizeVolatileMetadata bool) string {
	if len(messages) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(historyTranscriptTitle)
	b.WriteString("\n")
	b.WriteString(historyTranscriptSummary)
	b.WriteString("\n\n")

	entry := 0
	for _, raw := range messages {
		msg, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		role := normalizeOpenAIRoleForPrompt(strings.ToLower(strings.TrimSpace(asString(msg["role"]))))
		content := strings.TrimSpace(buildOpenAIHistoryEntry(role, msg, canonicalizeVolatileMetadata))
		if content == "" {
			continue
		}
		entry++
		fmt.Fprintf(&b, "=== %d. %s ===\n%s\n\n", entry, strings.ToUpper(roleLabelForHistory(role)), content)
	}

	transcript := strings.TrimSpace(b.String())
	if transcript == "" {
		return ""
	}
	return transcript + "\n"
}

func buildOpenAIHistoryEntry(role string, msg map[string]any, canonicalizeVolatileMetadata bool) string {
	var content string
	switch role {
	case "assistant":
		content = buildAssistantContentForPrompt(msg)
	case "tool", "function":
		content = buildToolHistoryContent(msg)
	case "system", "user":
		content = NormalizeOpenAIContentForPrompt(msg["content"])
	default:
		content = NormalizeOpenAIContentForPrompt(msg["content"])
	}
	content = strings.TrimSpace(content)
	if canonicalizeVolatileMetadata {
		content = strings.TrimSpace(canonicalizeVolatileTranscriptText(content))
	}
	return content
}

func buildToolHistoryContent(msg map[string]any) string {
	content := strings.TrimSpace(NormalizeOpenAIContentForPrompt(msg["content"]))
	parts := make([]string, 0, 2)
	if name := strings.TrimSpace(asString(msg["name"])); name != "" {
		parts = append(parts, "name="+name)
	}
	if callID := strings.TrimSpace(asString(msg["tool_call_id"])); callID != "" {
		parts = append(parts, "tool_call_id="+callID)
	}
	header := ""
	if len(parts) > 0 {
		header = "[" + strings.Join(parts, " ") + "]"
	}
	switch {
	case header != "" && content != "":
		return header + "\n" + content
	case header != "":
		return header
	default:
		return content
	}
}

func roleLabelForHistory(role string) string {
	role = strings.ToLower(strings.TrimSpace(role))
	switch role {
	case "function":
		return "tool"
	case "":
		return "unknown"
	default:
		return role
	}
}

var volatileMetadataBlockRE = regexp.MustCompile(`(?s)((?:Conversation info|Sender) \(untrusted metadata\):\s*` + "```" + `json\s*\n?)(.*?)(\n?` + "```" + `)`)

func canonicalizeVolatileTranscriptText(text string) string {
	if text == "" || !strings.Contains(text, "untrusted metadata") {
		return text
	}
	return volatileMetadataBlockRE.ReplaceAllStringFunc(text, func(block string) string {
		matches := volatileMetadataBlockRE.FindStringSubmatch(block)
		if len(matches) != 4 {
			return block
		}
		cleaned, ok := canonicalizeMetadataJSON(matches[2])
		if !ok {
			return block
		}
		return matches[1] + cleaned + matches[3]
	})
}

func canonicalizeMetadataJSON(raw string) (string, bool) {
	var value any
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &value); err != nil {
		return "", false
	}
	stripVolatileMetadataFields(value)
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return "", false
	}
	return string(b), true
}

func stripVolatileMetadataFields(value any) {
	switch x := value.(type) {
	case map[string]any:
		delete(x, "message_id")
		delete(x, "timestamp")
		delete(x, "timestamp_ms")
		for _, child := range x {
			stripVolatileMetadataFields(child)
		}
	case []any:
		for _, child := range x {
			stripVolatileMetadataFields(child)
		}
	}
}
