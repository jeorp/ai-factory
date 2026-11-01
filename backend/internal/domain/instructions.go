package domain

import "strings"

// LatestInstructions preserves the newest messages within the input budget.
// Older text may be shortened, with an explicit omission marker.
func LatestInstructions(messages []Message, limit int) string {
	if limit <= 0 {
		return ""
	}
	text := ""
	for i := len(messages) - 1; i >= 0; i-- {
		next := messages[i].Text
		separator := ""
		if text != "" {
			separator = "\n"
		}
		if len(next)+len(separator)+len(text) > limit {
			marker := "[以前の指示の一部を省略]\n"
			room := limit - len(text) - len(separator) - len(marker)
			if room > 0 {
				return marker + Clip(next, room) + separator + text
			}
			if text != "" {
				return text
			}
			return Clip(next, limit)
		}
		text = next + separator + text
	}
	return text
}

func AppendRequest(previous, latest string, limit int) string {
	if limit <= 0 {
		return ""
	}
	latest = Clip(strings.TrimSpace(latest), limit)
	if previous == "" {
		return latest
	}
	separator := "\n\n追加の依頼: "
	room := limit - len(latest) - len(separator)
	if room <= 0 {
		return latest
	}
	return Clip(previous, room) + separator + latest
}
