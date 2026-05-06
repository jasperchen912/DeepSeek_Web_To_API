package promptcompat

import (
	"strings"
	"testing"
)

func TestBuildOpenAICurrentInputContextTranscriptCanonicalizesTelegramMetadata(t *testing.T) {
	first := []any{
		map[string]any{"role": "user", "content": telegramMetadataFixture("1752", "Wed 2026-05-06 22:48 GMT+8")},
	}
	second := []any{
		map[string]any{"role": "user", "content": telegramMetadataFixture("1753", "Wed 2026-05-06 22:49 GMT+8")},
	}

	firstTranscript := BuildOpenAICurrentInputContextTranscript(first)
	secondTranscript := BuildOpenAICurrentInputContextTranscript(second)

	if firstTranscript != secondTranscript {
		t.Fatalf("expected volatile-only metadata changes to produce stable transcript\nfirst:\n%s\nsecond:\n%s", firstTranscript, secondTranscript)
	}
	for _, needle := range []string{"\"message_id\"", "22:48", "22:49", "timestamp"} {
		if strings.Contains(firstTranscript, needle) {
			t.Fatalf("expected transcript to remove volatile metadata %q, got:\n%s", needle, firstTranscript)
		}
	}
	for _, needle := range []string{"\"chat_id\": \"telegram:-1003831997039\"", "\"topic_id\": \"1682\"", "\"sender_id\": \"410030039\"", "same visible user request"} {
		if !strings.Contains(firstTranscript, needle) {
			t.Fatalf("expected transcript to keep stable content %q, got:\n%s", needle, firstTranscript)
		}
	}
}

func TestBuildOpenAIHistoryTranscriptKeepsRawMetadata(t *testing.T) {
	transcript := BuildOpenAIHistoryTranscript([]any{
		map[string]any{"role": "user", "content": telegramMetadataFixture("1752", "Wed 2026-05-06 22:48 GMT+8")},
	})
	for _, needle := range []string{"\"message_id\": \"1752\"", "Wed 2026-05-06 22:48 GMT+8"} {
		if !strings.Contains(transcript, needle) {
			t.Fatalf("expected legacy history transcript to preserve %q, got:\n%s", needle, transcript)
		}
	}
}

func telegramMetadataFixture(messageID, timestamp string) string {
	return `Conversation info (untrusted metadata):
` + "```json" + `
{
  "chat_id": "telegram:-1003831997039",
  "message_id": "` + messageID + `",
  "sender_id": "410030039",
  "conversation_label": "Javis Group id:-1003831997039 topic:1682",
  "timestamp": "` + timestamp + `",
  "group_subject": "Javis Group",
  "topic_id": "1682",
  "topic_name": "cache方案排查",
  "is_forum": true,
  "is_group_chat": true
}
` + "```" + `

Sender (untrusted metadata):
` + "```json" + `
{
  "label": "六六 (410030039)",
  "id": "410030039",
  "name": "六六",
  "username": "jasperchen2025"
}
` + "```" + `

same visible user request`
}
