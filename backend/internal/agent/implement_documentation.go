package agent

import (
	"context"
	"strings"

	"ai-factory/backend/internal/domain"
)

const projectDocumentationBytes = 8000

type projectDocumentation struct {
	Path        string `json:"path"`
	Content     string `json:"content,omitempty"`
	Truncated   bool   `json:"truncated,omitempty"`
	Unavailable bool   `json:"unavailable,omitempty"`
	Modified    bool   `json:"modified,omitempty"`
}

// Keep the repository's documented contract in every implementation phase,
// independent of model-selected reads and their bounded evidence window.
// The document is pinned to the same base commit as the source being changed.
func (run *implementRun) loadProjectDocumentation(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if run.projectDocumentationLoaded {
		return nil
	}
	if run.r.Writer == nil {
		return nil
	}
	run.cp.Metrics.ToolCalls++
	content, exists, err := run.r.Writer.RawFile(ctx, run.in.GitHubToken, run.in.Session.Target.Repository, run.impl.BaseCommit, "README.md")
	if ctx.Err() != nil {
		return ctx.Err()
	}
	run.projectDocumentationLoaded = true
	if err != nil {
		run.projectDocumentation = &projectDocumentation{Path: "README.md", Unavailable: true}
		run.logger.Info("implement_project_documentation_unavailable", "error_code", implementationErrorCode(err))
		return nil
	}
	if !exists {
		return nil
	}
	run.cp.Metrics.FetchedBytes += len(content)
	for _, secret := range []string{run.in.Key, run.in.GitHubToken} {
		if secret != "" {
			content = strings.ReplaceAll(content, secret, "***MASKED_SECRET***")
		}
	}
	content = domain.MaskSecrets(content)
	run.projectDocumentation = &projectDocumentation{Path: "README.md", Content: domain.Clip(content, projectDocumentationBytes), Truncated: len(content) > projectDocumentationBytes}
	source := domain.Source{Label: "README.md", URL: "https://github.com/" + run.in.Session.Target.Repository + "/blob/" + run.impl.BaseCommit + "/README.md"}
	if !containsSource(run.cp.Sources, source.URL) {
		run.cp.Sources = append(run.cp.Sources, source)
		if len(run.cp.Sources) > 40 {
			run.cp.Sources = run.cp.Sources[len(run.cp.Sources)-40:]
		}
	}
	return nil
}

// currentProjectDocumentation overlays a saved README so verification sees the
// contract produced by the implementation rather than the obsolete base.
func (run *implementRun) currentProjectDocumentation(ctx context.Context) (*projectDocumentation, error) {
	doc := run.projectDocumentation
	content, changed, err := run.changedContent(ctx, "README.md")
	if err != nil {
		return nil, err
	}
	if !changed {
		return doc, nil
	}
	for _, secret := range []string{run.in.Key, run.in.GitHubToken} {
		if secret != "" {
			content = strings.ReplaceAll(content, secret, "***MASKED_SECRET***")
		}
	}
	content = domain.MaskSecrets(content)
	return &projectDocumentation{Path: "README.md", Content: domain.Clip(content, projectDocumentationBytes), Truncated: len(content) > projectDocumentationBytes, Modified: true}, nil
}
