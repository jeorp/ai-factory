package agent

import (
	"fmt"
	"strings"

	"ai-factory/backend/internal/domain"
)

func defaultBranchName(sessionID string, version int) string {
	id := strings.ToLower(sessionID)
	clean := strings.Builder{}
	for _, c := range id {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			clean.WriteRune(c)
		}
		if clean.Len() >= 12 {
			break
		}
	}
	if clean.Len() == 0 {
		clean.WriteString("change")
	}
	return fmt.Sprintf("ai-factory/%s-%d", clean.String(), version+1)
}

func defaultTitle(impl *domain.Implementation) string {
	title := strings.TrimSpace(strings.SplitN(impl.Plan.Summary, "\n", 2)[0])
	if title == "" {
		title = strings.TrimSpace(strings.SplitN(impl.Request, "\n", 2)[0])
	}
	return domain.Clip(title, 120)
}

func defaultBody(impl *domain.Implementation) string {
	var b strings.Builder
	b.WriteString("このPRはAIファクトリーのAIが作成しました。内容を確認してからマージしてください。\n\n## 依頼\n\n")
	b.WriteString(domain.Clip(impl.Request, 2000))
	b.WriteString("\n\n## 変更内容\n\n")
	b.WriteString(impl.Plan.Summary + "\n\n")
	for _, f := range impl.Plan.Files {
		b.WriteString(fmt.Sprintf("- `%s`（%s）: %s\n", f.Path, actionLabel(f.Action), f.Approach))
	}
	b.WriteString("\n## 検証\n\nアプリ内ではコード・ビルド・テストを実行していません。このリポジトリのCIの結果を確認してください。\n")
	if v := impl.Verification; v != nil {
		b.WriteString("\n### AIによる整合性確認\n\n")
		if len(v.Issues) == 0 {
			b.WriteString("確認した範囲で不整合の指摘はありません。ビルド・テストの成功を保証するものではありません。\n")
		}
		for _, s := range v.Issues {
			b.WriteString("- 指摘: " + s + "\n")
		}
		for _, s := range v.Unverified {
			b.WriteString("- 未確認: " + s + "\n")
		}
	}
	if len(impl.Plan.Tests) > 0 {
		b.WriteString("\n確認してほしい点:\n")
		for _, t := range impl.Plan.Tests {
			b.WriteString("- " + t + "\n")
		}
	}
	if len(impl.Unverified) > 0 {
		b.WriteString("\n## 未確認事項\n\n")
		for _, u := range impl.Unverified {
			b.WriteString("- " + u + "\n")
		}
	}
	return domain.Clip(b.String(), 60000)
}

func actionLabel(a string) string {
	switch a {
	case "create":
		return "追加"
	case "delete":
		return "削除"
	}
	return "変更"
}

func formatAmendment(impl *domain.Implementation) string {
	var b strings.Builder
	b.WriteString("作業の途中で、計画にないファイルも変更が必要だと分かりました。追加してよければ承認してください。見送ると、これらのファイルは変更せずに残りの作業を続けます。\n\n【追加したいファイル】\n")
	for _, f := range impl.PendingFiles {
		b.WriteString(fmt.Sprintf("- %s（%s）: %s\n  理由: %s\n", f.Path, actionLabel(f.Action), f.Approach, f.Reason))
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func formatPlan(impl *domain.Implementation) string {
	p := impl.Plan
	var b strings.Builder
	base := "ベース: " + impl.BaseBranch + " " + impl.BaseCommit[:7]
	if impl.ExpectedHead != "" && impl.PullNumber > 0 {
		base = fmt.Sprintf("PR #%d のブランチ %s の %s に追加", impl.PullNumber, impl.Branch, impl.BaseCommit[:7])
	}
	b.WriteString("実装計画を作成しました（" + base + "）。\n\n" + p.Summary + "\n\n【変更するファイル】\n")
	for _, f := range p.Files {
		b.WriteString(fmt.Sprintf("- %s（%s）: %s\n", f.Path, actionLabel(f.Action), f.Approach))
	}
	list := func(title string, items []string) {
		if len(items) == 0 {
			return
		}
		b.WriteString("\n【" + title + "】\n")
		for _, v := range items {
			b.WriteString("- " + v + "\n")
		}
	}
	list("対象外", p.OutOfScope)
	list("リスク", p.Risks)
	list("確認すべきテスト", p.Tests)
	b.WriteString("\n計画を承認すると変更を作成します。修正したい場合はチャットで追加の依頼を送ってください。")
	return b.String()
}

func formatChanges(impl *domain.Implementation) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("%dファイルの変更を作成しました。差分を確認し、ブランチ名・PRタイトル・本文を確認してからPRを作成してください。\n\n", len(impl.Changes)))
	for _, c := range impl.Changes {
		b.WriteString(fmt.Sprintf("- %s（%s）\n", c.Path, actionLabel(c.Action)))
	}
	if v := impl.Verification; v != nil {
		b.WriteString(fmt.Sprintf("\n【AIによる整合性確認】指摘%d件、未確認%d件。詳しくは実装パネルを確認してください。\n", len(v.Issues), len(v.Unverified)))
	}
	if len(impl.Unverified) > 0 {
		b.WriteString("\n【未確認事項】\n")
		for _, u := range impl.Unverified {
			b.WriteString("- " + u + "\n")
		}
	}
	b.WriteString("\nコードやテストは実行していません。作成後のCIで確認してください。")
	return b.String()
}
