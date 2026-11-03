package httpapi

import (
	"ai-factory/backend/internal/domain"
	"strings"
)

func documentationCommandProblem(cmd *command) string {
	if cmd.Type != "documentation" {
		return ""
	}
	if cmd.DocumentScope == "" {
		cmd.DocumentScope = "feature"
	}
	if cmd.DocumentScope != "feature" && cmd.DocumentScope != "repository" {
		return "invalid_documentation_scope"
	}
	cmd.Text = strings.TrimSpace(cmd.Text)
	if cmd.Text == "" {
		if cmd.DocumentScope == "feature" {
			return "documentation_request_required"
		}
		cmd.Text = "リポジトリ全体の構成と主要機能の実装を理解する資料を作成してください。"
	}
	return ""
}

func applyDocumentationCommand(session *domain.Session, cmd command) {
	if cmd.Type != "documentation" {
		return
	}
	session.RunKind = "documentation"
	session.Documentation = &domain.Documentation{
		Request: cmd.Text, Scope: cmd.DocumentScope, Repository: session.Target.Repository,
		Commit: session.Target.Commit, Status: "researching", Sources: []domain.Source{}, Limitations: []string{},
	}
}
