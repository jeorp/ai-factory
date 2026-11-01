package agent

import "context"

type effortContextKey struct{}

func withEffort(ctx context.Context, effort string) context.Context {
	return context.WithValue(ctx, effortContextKey{}, effort)
}

func selectedEffort(ctx context.Context) string {
	effort, _ := ctx.Value(effortContextKey{}).(string)
	return effort
}
