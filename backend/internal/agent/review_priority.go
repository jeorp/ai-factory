package agent

import (
	"encoding/json"
	"strconv"
	"strings"

	"ai-factory/backend/internal/domain"
)

type priorityItem struct {
	Index int    `json:"index"`
	Text  string `json:"text"`
}
type priorityResponse struct {
	Index  int    `json:"index"`
	Level  string `json:"level"`
	Reason string `json:"reason"`
}

// Invalid, missing, duplicate, or out-of-range results remain explicitly unassessed.
func savePriorities(review *domain.Review, items []priorityItem, answer string) {
	if review.Priorities == nil {
		review.Priorities = map[string]domain.ReviewPriority{}
	}
	var response []priorityResponse
	_ = json.Unmarshal([]byte(answer), &response)
	for _, item := range items {
		priority := domain.ReviewPriority{Level: "unknown", Reason: "優先度を評価できませんでした。"}
		count := 0
		for _, value := range response {
			if value.Index != item.Index {
				continue
			}
			count++
			if (value.Level == "high" || value.Level == "medium" || value.Level == "low" || value.Level == "unknown") && strings.TrimSpace(value.Reason) != "" && len(value.Reason) <= 512 {
				priority = domain.ReviewPriority{Level: value.Level, Reason: value.Reason}
			}
		}
		if count != 1 {
			priority = domain.ReviewPriority{Level: "unknown", Reason: "優先度を評価できませんでした。"}
		}
		review.Priorities[strconv.Itoa(item.Index)] = priority
	}
}
