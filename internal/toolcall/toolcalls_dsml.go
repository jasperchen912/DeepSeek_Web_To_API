package toolcall

import "strings"

func normalizeDSMLToolCallMarkup(text string) (string, bool) {
	if text == "" {
		return "", true
	}
	hasAliasLikeMarkup, _ := ContainsToolMarkupSyntaxOutsideIgnored(text)
	if !hasAliasLikeMarkup {
		return text, true
	}
	return rewriteDSMLToolMarkupOutsideIgnored(text), true
}

func rewriteDSMLToolMarkupOutsideIgnored(text string) string {
	if text == "" {
		return ""
	}
	lower := strings.ToLower(text)
	var b strings.Builder
	b.Grow(len(text))
	for i := 0; i < len(text); {
		next, advanced, blocked := skipXMLIgnoredSection(lower, i)
		if blocked {
			b.WriteString(text[i:])
			break
		}
		if advanced {
			b.WriteString(text[i:next])
			i = next
			continue
		}
		tag, ok := scanToolMarkupTagAt(text, i)
		if !ok {
			b.WriteByte(text[i])
			i++
			continue
		}
		canonicalName := canonicalToolMarkupName(tag.Name)
		if tag.DSMLLike || canonicalName != tag.Name {
			b.WriteByte('<')
			if tag.Closing {
				b.WriteByte('/')
			}
			b.WriteString(canonicalName)
			// Strip any DSML-style trailing pipes ("|>" / "｜>") between the
			// recognized name and the closing '>' so the rewritten tag is
			// pure canonical XML the downstream parser expects.
			tail := stripDSMLTrailingPipe(text[tag.NameEnd : tag.End+1])
			b.WriteString(tail)
			if !strings.HasSuffix(tail, ">") {
				b.WriteByte('>')
			}
			i = tag.End + 1
			continue
		}
		b.WriteString(text[tag.Start : tag.End+1])
		i = tag.End + 1
	}
	return b.String()
}

// stripDSMLTrailingPipe removes a single trailing '|' or '｜' that some models
// emit just before the closing '>' (e.g. "<|DSML|tool_calls|>"). The
// canonical XML form has no such pipe; without stripping, downstream lookup
// like findXMLElementBlocks(text,"tool_calls") would fail to match
// "<tool_calls|>".
func stripDSMLTrailingPipe(seg string) string {
	if seg == "" {
		return seg
	}
	fullwidthEnd := false
	end := ">"
	switch {
	case strings.HasSuffix(seg, "＞"):
		end = "＞"
		fullwidthEnd = true
	case strings.HasSuffix(seg, ">"):
	default:
		return seg
	}
	body := strings.TrimSuffix(seg, end)
	body = strings.TrimRight(body, " \t\r\n")
	strippedPipe := false
	if strings.HasSuffix(body, "|") {
		body = strings.TrimSuffix(body, "|")
		strippedPipe = true
	} else if strings.HasSuffix(body, "｜") {
		body = strings.TrimSuffix(body, "｜")
		strippedPipe = true
	} else if !fullwidthEnd {
		return seg
	}
	if !strippedPipe && !fullwidthEnd {
		return seg
	}
	return strings.TrimRight(body, " \t\r\n") + ">"
}
