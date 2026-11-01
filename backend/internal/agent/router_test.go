package agent

import (
	"context"
	"testing"
)

type namedProvider string

func (p namedProvider) Next(context.Context, string, string, string) (Decision, Usage, error) {
	return Decision{Answer: string(p)}, Usage{}, nil
}

func TestModelRouterUsesTheModelsProvider(t *testing.T) {
	r := ModelRouter{Providers: map[string]Provider{"gemini": namedProvider("gemini"), "vertex": namedProvider("vertex")}, ModelProviders: map[string]string{"gemini-3.8-flash": "gemini", "claude-haiku-5-5": "vertex"}}
	for model, want := range map[string]string{"gemini-3.8-flash": "gemini", "claude-haiku-5-5": "vertex"} {
		if d, _, err := r.Next(context.Background(), "key", model, "input"); err != nil || d.Answer != want {
			t.Fatalf("%s: got %q %v", model, d.Answer, err)
		}
	}
	if _, _, err := r.Next(context.Background(), "key", "gpt-5-mini", "input"); err == nil {
		t.Fatal("expected an unknown model to be rejected")
	}
}
