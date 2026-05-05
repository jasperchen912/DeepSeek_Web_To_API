package shared

import "DeepSeek_Web_To_API/internal/toolcall"

func ToolcallFeatureMatchEnabled(_ ConfigReader) bool {
	return true
}

func ToolcallEarlyEmitHighConfidence(_ ConfigReader) bool {
	return true
}

func ShouldBufferStreamToolContent(finalPrompt string, toolNames []string) bool {
	if len(toolNames) > 0 {
		return true
	}
	hasDSML, hasCanonical := toolcall.ContainsToolCallWrapperSyntaxOutsideIgnored(finalPrompt)
	return hasDSML || hasCanonical
}
