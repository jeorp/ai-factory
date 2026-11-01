package agent

import (
	"context"
)

// changedContent is an overlay on the pinned repository; it never falls back
// to stale base content when a generated file has expired or was deleted.
func (run *implementRun) changedContent(ctx context.Context, path string) (string, bool, error) {
	for _, c := range run.impl.Changes {
		if c.Path != path {
			continue
		}
		f, err := run.savedChange(ctx, path)
		if err != nil {
			return "", true, err
		}
		if f.Action == "delete" {
			return "[作業中の変更: このファイルは削除済みです]", true, nil
		}
		return "[作業中の変更後の内容]\n" + f.Content, true, nil
	}
	return "", false, nil
}
