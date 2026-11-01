package domain

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestLatestInstructionsKeepsNewestAndBoundsUTF8(t *testing.T) {
	latest := "新しい指示: 色を青に変更"
	messages := []Message{{Text: strings.Repeat("古", 1400)}, {Text: "古い指示: 赤"}, {Text: latest}}
	got := LatestInstructions(messages, 4096)
	if !strings.HasSuffix(got, latest) || !strings.Contains(got, "省略") || len(got) > 4096 || !utf8.ValidString(got) {
		t.Fatalf("new instruction lost: %q", got)
	}
	for _, limit := range []int{0, 1, 4, 50, 4096} {
		got := LatestInstructions([]Message{{Text: strings.Repeat("あ", 2000)}}, limit)
		if len(got) > limit || !utf8.ValidString(got) {
			t.Fatalf("bad bound %d: %q", limit, got)
		}
	}
}

func TestAppendRequestPreservesLatestAfterRepeatedRevisions(t *testing.T) {
	request := strings.Repeat("最初の依頼", 400)
	for i := 0; i < 50; i++ {
		request = AppendRequest(request, strings.Repeat("追加修正", 60), 4096)
	}
	latest := "今回は日本語表示を維持して境界値を修正"
	got := AppendRequest(request, latest, 4096)
	if len(got) > 4096 || !utf8.ValidString(got) || !strings.HasSuffix(got, latest) {
		t.Fatalf("latest lost: %q", got)
	}
}
