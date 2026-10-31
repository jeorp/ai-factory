package domain

import (
	"errors"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Limits for AI-authored change sets (specification section 13; provisional values).
const (
	MaxChangeFiles     = 20
	MaxChangeFileBytes = 100 * 1024
	MaxChangeSetBytes  = 200 * 1024
	BranchPrefix       = "ai-factory/"
)

// FileChange is one file of an AI-authored change set. Content is the full new
// text for create/update and empty for delete.
type FileChange struct {
	Path    string `json:"path"`
	Action  string `json:"action"`
	Content string `json:"content,omitempty"`
}

// Edit replaces exactly one occurrence of Search with Replace.
type Edit struct {
	Search  string `json:"search"`
	Replace string `json:"replace"`
}

var secretPattern = regexp.MustCompile(`(?:AIza|sk-|github_pat_|gh[opusr]_|xox[abp]-)[a-zA-Z0-9_-]{12,}|-----BEGIN [A-Z ]*PRIVATE KEY-----`)

// ContainsSecret reports whether text contains a credential-like token.
func ContainsSecret(text string) bool { return secretPattern.MatchString(text) }

// ApplyEdits applies search/replace edits in order. Each search text must match
// exactly once in the current content so that the model cannot change an
// unintended location.
func ApplyEdits(content string, edits []Edit) (string, error) {
	if len(edits) == 0 {
		return "", errors.New("edit_empty")
	}
	for _, e := range edits {
		if e.Search == "" {
			return "", errors.New("edit_search_empty")
		}
		switch strings.Count(content, e.Search) {
		case 0:
			return "", errors.New("edit_search_not_found")
		case 1:
		default:
			return "", errors.New("edit_search_ambiguous")
		}
		content = strings.Replace(content, e.Search, e.Replace, 1)
	}
	return content, nil
}

// ValidateChangePath rejects paths the AI must never write.
func ValidateChangePath(p string) error {
	if p == "" || len(p) > 512 || !utf8.ValidString(p) || strings.ContainsAny(p, "\\\x00:*?\"<>|") {
		return errors.New("invalid_change_path")
	}
	if strings.HasPrefix(p, "/") || path.Clean(p) != p {
		return errors.New("invalid_change_path")
	}
	lower := strings.ToLower(p)
	for _, seg := range strings.Split(lower, "/") {
		if seg == "" || seg == "." || seg == ".." || seg == ".git" {
			return errors.New("invalid_change_path")
		}
	}
	if strings.HasPrefix(lower, ".github/workflows/") || lower == ".gitmodules" {
		return errors.New("protected_change_path")
	}
	for _, suffix := range []string{".png", ".jpg", ".jpeg", ".gif", ".webp", ".pdf", ".zip", ".mp4", ".woff", ".woff2", ".exe", ".lock", ".min.js", ".map"} {
		if strings.HasSuffix(lower, suffix) {
			return errors.New("binary_or_generated_file_excluded")
		}
	}
	return nil
}

// ValidateChangeSet checks a change set against the user-approved plan and limits.
func ValidateChangeSet(files []FileChange, approved []string) error {
	if len(files) == 0 {
		return errors.New("change_set_empty")
	}
	if len(files) > MaxChangeFiles {
		return errors.New("change_set_file_limit")
	}
	allowed := map[string]bool{}
	for _, p := range approved {
		allowed[p] = true
	}
	seen := map[string]bool{}
	total := 0
	for _, f := range files {
		if err := ValidateChangePath(f.Path); err != nil {
			return err
		}
		if !allowed[f.Path] {
			return errors.New("change_path_not_approved")
		}
		if seen[f.Path] {
			return errors.New("change_path_duplicate")
		}
		seen[f.Path] = true
		switch f.Action {
		case "create", "update":
			if !utf8.ValidString(f.Content) || strings.ContainsRune(f.Content, 0) {
				return errors.New("change_not_text")
			}
		case "delete":
			if f.Content != "" {
				return errors.New("change_delete_with_content")
			}
		default:
			return errors.New("invalid_change_action")
		}
		if len(f.Content) > MaxChangeFileBytes {
			return errors.New("change_file_size_limit")
		}
		total += len(f.Content)
		if total > MaxChangeSetBytes {
			return errors.New("change_set_size_limit")
		}
		if ContainsSecret(f.Content) {
			return errors.New("change_contains_secret")
		}
	}
	return nil
}

var branchPattern = regexp.MustCompile(`^ai-factory/[a-z0-9][a-z0-9._-]{0,62}(/[a-z0-9][a-z0-9._-]{0,62})?$`)

// ValidBranchName accepts only new AI branches under the ai-factory/ prefix.
func ValidBranchName(name string) bool {
	return branchPattern.MatchString(name) && !strings.Contains(name, "..") && !strings.HasSuffix(name, ".lock") && !strings.HasSuffix(name, ".")
}
