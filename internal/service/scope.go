package service

import "context"

type scopeKey struct{}

// WithScope returns a context carrying the authenticated caller (set by the API's
// authentication; read by adapters such as the MCP endpoint).
func WithScope(ctx context.Context, sc Scope) context.Context {
	return context.WithValue(ctx, scopeKey{}, sc)
}

// ScopeFrom returns the caller stored by WithScope.
func ScopeFrom(ctx context.Context) (Scope, bool) {
	sc, ok := ctx.Value(scopeKey{}).(Scope)
	return sc, ok
}

// Allows reports whether the caller may act on the instance.
func (sc Scope) Allows(instanceID string) bool {
	return sc.Admin || (sc.InstanceID != "" && sc.InstanceID == instanceID)
}
