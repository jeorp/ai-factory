package domain

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	MaxInputBytes   = 24 * 1024
	MaxFetchBytes   = 100 * 1024
	MaxToolCalls    = 6
	MaxFiles        = 5
	MaxHistoryBytes = 300 * 1024
)

var ErrConflict = errors.New("session_busy")
var ErrNotFound = errors.New("not_found")
var slug = regexp.MustCompile(`^[a-zA-Z0-9_.-]+/[a-zA-Z0-9_.-]+$`)
var sha = regexp.MustCompile(`^[a-fA-F0-9]{40}$`)

type Target struct {
	Repository string `json:"repository"`
	Commit     string `json:"commit"`
	Kind       string `json:"kind"`
	Number     int    `json:"number"`
}

func ParseTarget(value string) (Target, error) {
	value = strings.TrimSpace(value)
	t := Target{Kind: "repository"}
	if strings.HasPrefix(value, "https://") {
		u, err := url.Parse(value)
		if err != nil || u.Host != "github.com" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
			return t, errors.New("invalid_github_url")
		}
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) != 2 && len(parts) != 4 {
			return t, errors.New("invalid_github_url")
		}
		value = strings.Join(parts[:2], "/")
		if len(parts) == 4 {
			if parts[2] != "pull" && parts[2] != "issues" {
				return t, errors.New("invalid_github_url")
			}
			for _, c := range parts[3] {
				if c < '0' || c > '9' {
					return t, errors.New("invalid_number")
				}
				t.Number = t.Number*10 + int(c-'0')
				if t.Number > 100000000 {
					return t, errors.New("invalid_number")
				}
			}
			if t.Number < 1 {
				return t, errors.New("invalid_number")
			}
			t.Kind = parts[2]
		}
	}
	if !slug.MatchString(value) || strings.Contains(value, "..") {
		return t, errors.New("invalid_repository")
	}
	t.Repository = value
	return t, nil
}
func ValidCommit(s string) bool { return sha.MatchString(s) }

type Source struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}
type Message struct {
	ID      string    `json:"id"`
	Role    string    `json:"role"`
	Text    string    `json:"text"`
	At      time.Time `json:"at"`
	Applied bool      `json:"applied"`
}
type Metrics struct {
	InputTokens      int   `json:"inputTokens"`
	LastInputTokens  int   `json:"lastInputTokens"`
	LastOutputTokens int   `json:"lastOutputTokens"`
	HasLastUsage     bool  `json:"hasLastUsage"`
	OutputTokens     int   `json:"outputTokens"`
	MaxInputBytes    int   `json:"maxInputBytes"`
	FetchedBytes     int   `json:"fetchedBytes"`
	ToolCalls        int   `json:"toolCalls"`
	CacheHits        int   `json:"cacheHits"`
	DurationMS       int64 `json:"durationMs"`
	HeapPeakBytes    int64 `json:"heapPeakBytes"`
}
type RepositoryOverview struct {
	Text      string `json:"text"`
	Commit    string `json:"commit"`
	Truncated bool   `json:"truncated"`
}

type Session struct {
	ID                 string              `json:"id"`
	Title              string              `json:"title"`
	Target             Target              `json:"target"`
	Model              string              `json:"model"`
	Effort             string              `json:"effort,omitempty"`
	RunKind            string              `json:"runKind"`
	ReviewScope        string              `json:"reviewScope,omitempty"`
	ActivePullRequest  int                 `json:"activePullRequest"`
	Review             *Review             `json:"review,omitempty"`
	Implementation     *Implementation     `json:"implementation,omitempty"`
	State              string              `json:"state"`
	Messages           []Message           `json:"messages"`
	Summary            string              `json:"summary"`
	RepositoryOverview *RepositoryOverview `json:"-"`
	OverviewPending    bool                `json:"-"`
	Sources            []Source            `json:"sources"`
	Limitations        []string            `json:"limitations"`
	Metrics            Metrics             `json:"metrics"`
	UpdatedAt          time.Time           `json:"updatedAt"`
	ExpiresAt          time.Time           `json:"expiresAt"`
	Lease              string              `json:"-"`
	LeaseUntil         time.Time           `json:"-"`
}

func (s *Session) Normalize() {
	if s.Messages == nil {
		s.Messages = []Message{}
	}
	if s.Sources == nil {
		s.Sources = []Source{}
	}
	if s.Limitations == nil {
		s.Limitations = []string{}
	}
	if i := s.Implementation; i != nil {
		if i.Changes == nil {
			i.Changes = []ChangeSummary{}
		}
		if i.Unverified == nil {
			i.Unverified = []string{}
		}
		if v := i.Verification; v != nil {
			if v.Issues == nil {
				v.Issues = []string{}
			}
			if v.Unverified == nil {
				v.Unverified = []string{}
			}
		}
	}
	if r := s.Review; r != nil {
		if r.Steps == nil {
			r.Steps = []ReviewStep{}
		}
		if r.Findings == nil {
			r.Findings = []Finding{}
		}
		if r.Unverified == nil {
			r.Unverified = []string{}
		}
		for i := range r.Steps {
			if r.Steps[i].Files == nil {
				r.Steps[i].Files = []ReviewFile{}
			}
		}
	}
}

func Clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}
func (s *Session) HistoryBytes() int {
	n := 0
	for _, m := range s.Messages {
		n += len(m.Text)
	}
	return n
}
func (s *Session) Add(role, text, id string) error {
	if len(s.Messages) >= 200 || s.HistoryBytes()+len(text) > MaxHistoryBytes {
		return errors.New("history_limit_create_new_session")
	}
	s.Messages = append(s.Messages, Message{ID: id, Role: role, Text: text, At: time.Now().UTC(), Applied: role != "instruction"})
	return nil
}
func (s *Session) HasMessage(id string) bool {
	for _, m := range s.Messages {
		if m.ID == id {
			return true
		}
	}
	return false
}
