package agent

import (
	"context"
	"path"
	"strings"

	"ai-factory/backend/internal/domain"
	"ai-factory/backend/internal/github"
)

// read overlays generated changes on model-requested reads of the base commit.
func (run *implementRun) read(ctx context.Context, read github.Read, ev *[]evidence, keep int) error {
	if read.Kind != "file" && read.Kind != "repository" {
		*ev = append(*ev, evidence{Read: read, Content: "この区切りではファイルとディレクトリだけ取得できます。"})
		return nil
	}
	if err := read.Validate(); err != nil {
		return err
	}
	if err := run.save(ctx, "GitHub: "+read.Kind+" "+read.Path); err != nil {
		return err
	}
	content, overlaid, err := run.changedContent(ctx, read.Path)
	if err != nil {
		return err
	}
	var source domain.Source
	n := 0
	if read.Kind == "file" && overlaid {
		run.cp.Metrics.CacheHits++
		run.logger.Info("implement_read", "phase", run.impl.Status, "step", run.impl.NextStep+1, "source", "generated", "bytes", len(content))
	} else {
		run.cp.Metrics.ToolCalls++
		content, source, n, err = run.r.GitHub.Read(ctx, run.in.GitHubToken, run.target(), read)
		run.logger.Info("implement_read", "phase", run.impl.Status, "step", run.impl.NextStep+1, "source", "base", "bytes", n, "outcome", outcome(err), "error_code", implementationErrorCode(err))
	}
	run.cp.Metrics.FetchedBytes += n
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		content = "取得できませんでした: " + err.Error()
	} else {
		if source.URL != "" && !containsSource(run.cp.Sources, source.URL) {
			run.cp.Sources = append(run.cp.Sources, source)
			if len(run.cp.Sources) > 40 {
				run.cp.Sources = run.cp.Sources[len(run.cp.Sources)-40:]
			}
		}
		if len(content) > 16000 {
			content = domain.Clip(content, 16000) + "\n[入力サイズ制限により後半を省略]"
		}
	}
	*ev = append(*ev, evidence{Read: read, Content: content})
	if len(*ev) > keep {
		*ev = (*ev)[len(*ev)-keep:]
	}
	return nil
}

// relatedFiles lists files the plan may have missed: test files next to
// planned files, and files the model read while planning but left out.
func relatedFiles(plan *domain.ImplementationPlan, read []string, all []string) []string {
	planned := map[string]bool{}
	for _, f := range plan.Files {
		planned[f.Path] = true
	}
	exists := map[string]bool{}
	for _, p := range all {
		exists[p] = true
	}
	out := []string{}
	add := func(p string) {
		if planned[p] || domain.ValidateChangePath(p) != nil || (len(all) > 0 && !exists[p]) {
			return
		}
		planned[p] = true
		out = append(out, p)
	}
	for _, f := range plan.Files {
		if f.Action == "create" {
			continue
		}
		dir, base := path.Split(f.Path)
		ext := path.Ext(base)
		stem := strings.TrimSuffix(base, ext)
		for _, c := range []string{stem + "_test" + ext, stem + ".test" + ext, stem + ".spec" + ext, stem + ".test.ts", stem + ".spec.ts"} {
			if len(all) > 0 {
				add(dir + c)
			}
		}
	}
	for _, p := range read {
		add(p)
	}
	if len(out) > 10 {
		out = out[:10]
	}
	return out
}

type contextFile struct {
	Path     string `json:"path"`
	Content  string `json:"content"`
	Modified bool   `json:"modified,omitempty"`
	Deleted  bool   `json:"deleted,omitempty"`
}

type previousChange struct {
	Path   string `json:"path"`
	Action string `json:"action"`
	Diff   string `json:"diff"`
}

// fileList gives the model the real paths of the base commit, newline separated.
// The list is a planning aid: when it cannot be read the plan goes ahead with
// repository reads only, and the plan path check still applies.
func (run *implementRun) fileList(ctx context.Context) (string, []string, error) {
	paths, truncated, err := run.r.Writer.FileList(ctx, run.in.GitHubToken, run.in.Session.Target.Repository, run.impl.BaseCommit)
	if err != nil {
		if ctx.Err() != nil {
			return "", nil, ctx.Err()
		}
		run.logger.Info("implement_file_list_unavailable", "error_code", implementationErrorCode(err))
		return "[ファイル一覧を取得できませんでした。repositoryで確認してください]", nil, nil
	}
	// Cut only at a line break so no partial path reaches the model.
	var list strings.Builder
	for _, p := range paths {
		if list.Len()+len(p)+1 > github.MaxListedBytes {
			truncated = true
			break
		}
		list.WriteString(p + "\n")
	}
	if truncated {
		list.WriteString("[一覧の一部（深い階層）を省略しました。必要ならrepositoryで確認してください]")
	}
	return strings.TrimSuffix(list.String(), "\n"), paths, nil
}

func addContextFile(files []string, p string) []string {
	if domain.ValidateChangePath(p) != nil {
		return files
	}
	out := []string{}
	for _, f := range files {
		if f != p {
			out = append(out, f)
		}
	}
	out = append(out, p)
	if len(out) > maxContextFiles {
		out = out[len(out)-maxContextFiles:]
	}
	return out
}

// stepContext collects what a step needs beyond its own files: diffs of files
// changed by earlier steps, so names and signatures stay consistent, and the
// code read while planning, so definitions are not guessed.
func (run *implementRun) stepContext(ctx context.Context, step []string) ([]contextFile, []previousChange, error) {
	impl := run.impl
	inStep := map[string]bool{}
	for _, p := range step {
		inStep[p] = true
	}
	changed := map[string]bool{}
	previous := []previousChange{}
	total := 0
	for _, c := range impl.Changes {
		changed[c.Path] = true
		if inStep[c.Path] || total >= previousTotalBytes {
			continue
		}
		f, err := run.savedChange(ctx, c.Path)
		if err != nil {
			return nil, nil, err
		}
		diff := "（削除）"
		if c.Action != "delete" {
			original := ""
			if c.Action == "update" {
				content, _, err := run.r.Writer.RawFile(ctx, run.in.GitHubToken, run.in.Session.Target.Repository, impl.BaseCommit, c.Path)
				if err != nil {
					if ctx.Err() != nil {
						return nil, nil, ctx.Err()
					}
					continue
				}
				original = content
			}
			diff = domain.CompactDiff(original, f.Content, min(previousDiffBytes, previousTotalBytes-total))
		}
		total += len(diff)
		previous = append(previous, previousChange{Path: c.Path, Action: c.Action, Diff: diff})
	}
	candidates := []string{}
	for _, c := range impl.Changes {
		candidates = append(candidates, c.Path)
	}
	candidates = append(candidates, impl.ContextFiles...)
	if impl.Plan != nil {
		for _, f := range impl.Plan.Files {
			if f.Action == "update" {
				candidates = append(candidates, f.Path)
			}
		}
	}
	related := []contextFile{}
	seen := map[string]bool{}
	total = 0
	for _, p := range candidates {
		if inStep[p] || seen[p] || total >= contextTotalBytes {
			continue
		}
		seen[p] = true
		var content string
		var exists bool
		var err error
		deleted := false
		if changed[p] {
			f, err := run.savedChange(ctx, p)
			if err != nil {
				return nil, nil, err
			}
			content, exists, deleted = f.Content, true, f.Action == "delete"
		} else {
			content, exists, err = run.r.Writer.RawFile(ctx, run.in.GitHubToken, run.in.Session.Target.Repository, impl.BaseCommit, p)
		}
		if err != nil || !exists {
			if ctx.Err() != nil {
				return nil, nil, ctx.Err()
			}
			continue // directories, binaries and oversized files are simply left out
		}
		limit := min(contextFileBytes, contextTotalBytes-total)
		if len(content) > limit {
			content = domain.Clip(content, limit) + "\n[以降省略]"
		}
		total += len(content)
		related = append(related, contextFile{Path: p, Content: content, Modified: changed[p], Deleted: deleted})
	}
	return related, previous, nil
}
