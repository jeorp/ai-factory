package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-factory/backend/internal/domain"
	"ai-factory/backend/internal/github"
)

type providerFunc func(context.Context, string, string, string) (Decision, Usage, error)

func (f providerFunc) Next(c context.Context, k, m, i string) (Decision, Usage, error) {
	return f(c, k, m, i)
}

type readerFunc func(context.Context, string, domain.Target, github.Read) (string, domain.Source, int, error)

func (f readerFunc) Read(c context.Context, k string, t domain.Target, r github.Read) (string, domain.Source, int, error) {
	return f(c, k, t, r)
}
func baseSession() domain.Session {
	return domain.Session{Target: domain.Target{Repository: "a/b", Commit: strings.Repeat("a", 40)}, Model: "gpt-5-mini", Messages: []domain.Message{{Role: "user", Text: "構成を確認"}}}
}
func TestContextStaysBoundedAndKeepsInstructions(t *testing.T) {
	s := baseSession()
	for i := 0; i < 100; i++ {
		s.Messages = append(s.Messages, domain.Message{Role: "assistant", Text: strings.Repeat("あ", 1000)})
	}
	s.Messages = append(s.Messages, domain.Message{Role: "user", Text: "最新の質問"})
	ctx := BuildContext(s, strings.Repeat("s", 5000), []domain.Message{{Role: "instruction", Text: "認証を優先"}}, []evidence{{Content: strings.Repeat("x", 20000)}, {Content: strings.Repeat("y", 20000)}}, nil)
	if len(ctx) > domain.MaxInputBytes || !strings.Contains(ctx, "認証を優先") || !strings.Contains(ctx, "最新の質問") {
		t.Fatal("context lost target or exceeds cap")
	}
	if !json.Valid([]byte(ctx)) {
		t.Fatal("context truncated mid-JSON")
	}
}
func TestRunnerDeduplicatesAndCheckpointsBeforeNextCall(t *testing.T) {
	calls, reads, checkpoints := 0, 0, 0
	r := Runner{AI: providerFunc(func(ctx context.Context, key, model, input string) (Decision, Usage, error) {
		calls++
		if checkpoints == 0 {
			t.Fatal("no checkpoint before call")
		}
		if calls < 3 {
			return Decision{Read: &github.Read{Kind: "repository"}}, Usage{InputTokens: 10}, nil
		}
		return Decision{Answer: "確認しました", Summary: "要点"}, Usage{}, nil
	}), GitHub: readerFunc(func(context.Context, string, domain.Target, github.Read) (string, domain.Source, int, error) {
		reads++
		return "evidence", domain.Source{URL: "https://github.com/a/b"}, 8, nil
	})}
	var last Checkpoint
	err := r.Run(context.Background(), Input{Session: baseSession()}, func(cp Checkpoint) error { checkpoints++; last = cp; return nil })
	if err != nil || reads != 1 || last.Answer == "" || last.Metrics.CacheHits != 1 {
		t.Fatalf("dedup/checkpoint failed %v reads=%d %+v", err, reads, last)
	}
}
func TestCancellationAndCheckpointFailurePreventMoreWork(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	r := Runner{AI: providerFunc(func(context.Context, string, string, string) (Decision, Usage, error) {
		calls++
		cancel()
		return Decision{Read: &github.Read{Kind: "file", Path: "x"}}, Usage{}, nil
	}), GitHub: readerFunc(func(context.Context, string, domain.Target, github.Read) (string, domain.Source, int, error) {
		t.Fatal("read after cancellation")
		return "", domain.Source{}, 0, nil
	})}
	if err := r.Run(ctx, Input{Session: baseSession()}, func(Checkpoint) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := r.Run(context.Background(), Input{Session: baseSession()}, func(Checkpoint) error { return errors.New("disk failed") }); err == nil || calls != 1 {
		t.Fatal("continued after failed persistence")
	}
}
func TestKeysNeverReachModelInputOrCheckpoint(t *testing.T) {
	key := "sk-private-test-key"
	r := Runner{AI: providerFunc(func(ctx context.Context, k, m, input string) (Decision, Usage, error) {
		if strings.Contains(input, key) {
			t.Fatal("key leaked into input")
		}
		return Decision{Answer: key}, Usage{}, nil
	})}
	if err := r.Run(context.Background(), Input{Session: baseSession(), Key: key}, func(cp Checkpoint) error {
		if strings.Contains(cp.Answer, key) {
			t.Fatal("key checkpointed")
		}
		return nil
	}); err == nil {
		t.Fatal("credential echo accepted")
	}
}
func TestOpenAIWireContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		json.Unmarshal(raw, &body)
		if body["store"] != false || body["model"] != "gpt-5-mini" || r.Header.Get("Authorization") != "Bearer secret" || strings.Contains(string(raw), "secret") {
			t.Fatal("unsafe OpenAI request")
		}
		io.WriteString(w, `{"status":"completed","usage":{"input_tokens":123,"output_tokens":20},"output":[{"content":[{"type":"output_text","text":"{\"answer\":\"回答\",\"summary\":\"要点\",\"read\":null}"}]}]}`)
	}))
	defer server.Close()
	p := OpenAI{HTTP: server.Client(), Endpoint: server.URL}
	d, u, err := p.Next(context.Background(), "secret", "gpt-5-mini", "question")
	if err != nil || d.Answer != "回答" || u.InputTokens != 123 {
		t.Fatalf("wire contract failed %v %+v", err, d)
	}
}
