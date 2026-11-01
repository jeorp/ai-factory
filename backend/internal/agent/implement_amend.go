package agent

import (
	"context"
	"strings"

	"ai-factory/backend/internal/domain"
)

// maxPendingFiles bounds one amendment request.
const maxPendingFiles = 5

// checkAdditions validates files the model asked to add to the plan. Accepted
// ones wait for the user's approval; the rest are reported as unverified.
func (run *implementRun) checkAdditions(ctx context.Context, files []domain.PendingFile) ([]domain.PendingFile, []string, error) {
	impl := run.impl
	planned := map[string]bool{}
	for _, f := range impl.Plan.Files {
		planned[f.Path] = true
	}
	declined := map[string]bool{}
	for _, p := range impl.Declined {
		declined[p] = true
	}
	pending := []domain.PendingFile{}
	rejected := []string{}
	reject := func(f domain.PendingFile, why string) {
		rejected = append(rejected, domain.Clip("追加の提案を受け付けられませんでした: "+f.Path+"（"+why+"）", 512))
	}
	for _, f := range files {
		f.Path, f.Approach, f.Reason = strings.TrimSpace(f.Path), domain.Clip(strings.TrimSpace(f.Approach), 1024), domain.Clip(strings.TrimSpace(f.Reason), 512)
		switch {
		case domain.ValidateChangePath(f.Path) != nil:
			reject(f, "変更できないパス")
			continue
		case planned[f.Path]:
			continue // already in the plan; nothing to approve
		case declined[f.Path]:
			reject(f, "利用者が見送り済み")
			continue
		case f.Action != "create" && f.Action != "update" && f.Action != "delete":
			reject(f, "不明な操作")
			continue
		case f.Reason == "" || f.Approach == "":
			reject(f, "理由または方針がない")
			continue
		case len(pending) >= maxPendingFiles || len(impl.Plan.Files)+len(pending) >= domain.MaxChangeFiles:
			reject(f, "ファイル数の上限")
			continue
		}
		_, exists, err := run.r.Writer.RawFile(ctx, run.in.GitHubToken, run.in.Session.Target.Repository, impl.BaseCommit, f.Path)
		if err != nil {
			if ctx.Err() != nil {
				return nil, nil, ctx.Err()
			}
			switch err.Error() {
			case "change_file_size_limit", "change_not_text", "invalid_change_path", "protected_change_path", "binary_or_generated_file_excluded":
				reject(f, err.Error())
				continue
			}
			return nil, nil, err
		}
		if (f.Action == "create") == exists {
			reject(f, "ベースコミットでの有無が操作と合わない")
			continue
		}
		planned[f.Path] = true
		pending = append(pending, f)
	}
	// A proposal may refer to another proposal, so resolve references only
	// after path/existence/limit checks and prune dependents of rejected files.
	for {
		all := append([]domain.PlannedFile{}, impl.Plan.Files...)
		paths := map[string]bool{}
		for _, f := range all {
			paths[f.Path] = true
		}
		for _, f := range pending {
			paths[f.Path] = true
		}
		kept := []domain.PendingFile{}
		for _, f := range pending {
			valid := true
			seen := map[string]bool{}
			for _, dep := range f.DependsOn {
				if !paths[dep] || dep == f.Path || seen[dep] {
					valid = false
				}
				seen[dep] = true
			}
			if valid {
				kept = append(kept, f)
			} else {
				reject(f, "依存先が計画内にない、自己参照、または重複")
			}
		}
		if len(kept) != len(pending) {
			pending = kept
			continue
		}
		for _, f := range pending {
			all = append(all, domain.PlannedFile{Path: f.Path, DependsOn: f.DependsOn})
		}
		if err := domain.ValidatePlanDependencies(all); err != nil {
			for _, f := range pending {
				reject(f, "依存関係を一つの区切りで処理できません")
			}
			pending = nil
		}
		break
	}
	return pending, rejected, nil
}
