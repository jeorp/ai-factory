package agent

import (
	"context"
	"errors"
)

// ModelRouter sends each call to the provider that serves the session's model,
// so one server can offer models from several providers at once.
type ModelRouter struct {
	Providers      map[string]Provider
	ModelProviders map[string]string
}

func (r ModelRouter) Next(ctx context.Context, key, model, input string) (Decision, Usage, error) {
	provider, ok := r.Providers[r.ModelProviders[model]]
	if !ok {
		return Decision{}, Usage{}, errors.New("model_not_allowed")
	}
	return provider.Next(ctx, key, model, input)
}
