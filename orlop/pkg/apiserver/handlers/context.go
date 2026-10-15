package handlers

import (
	"context"

	"github.com/openshift-online/gecko/orlop/pkg/apiserver/storage"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// authenticatedUserContextKey is intentionally private so callers cannot
// accidentally collide with the public API identity value.
type authenticatedUserContextKey struct{}

type itemFilterContextKey struct{}

type objectAuthorizerContextKey struct{}

// WithAuthenticatedUser stores the already validated public-API principal in
// the request context. The authn middleware owns validation; handlers only use
// this value for immutable metadata such as created-by.
func WithAuthenticatedUser(ctx context.Context, email string) context.Context {
	return context.WithValue(ctx, authenticatedUserContextKey{}, email)
}

// AuthenticatedUserFromContext returns the validated public-API principal.
func AuthenticatedUserFromContext(ctx context.Context) (string, bool) {
	email, ok := ctx.Value(authenticatedUserContextKey{}).(string)
	return email, ok && email != ""
}

// WithItemFilter attaches a per-item list filter to the request.
func WithItemFilter(ctx context.Context, filter storage.ItemFilter) context.Context {
	return context.WithValue(ctx, itemFilterContextKey{}, filter)
}

// ItemFilterFromContext returns the optional per-item list filter for a
// request.
func ItemFilterFromContext(ctx context.Context) (storage.ItemFilter, bool) {
	filter, ok := ctx.Value(itemFilterContextKey{}).(storage.ItemFilter)
	return filter, ok && filter != nil
}

// ObjectAuthorizer evaluates an object before a handler exposes or persists it.
// Handlers that mutate an existing object invoke it for both the stored and
// effective object states.
type ObjectAuthorizer func(context.Context, client.Object) (bool, error)

// WithObjectAuthorizer attaches an object-state authorization callback to a
// request. Public authorization middleware owns the callback; handlers remain
// independent of a specific authorization implementation.
func WithObjectAuthorizer(ctx context.Context, authorizer ObjectAuthorizer) context.Context {
	return context.WithValue(ctx, objectAuthorizerContextKey{}, authorizer)
}

// ObjectAuthorizerFromContext returns the optional effective-object
// authorization callback for a request.
func ObjectAuthorizerFromContext(ctx context.Context) (ObjectAuthorizer, bool) {
	authorizer, ok := ctx.Value(objectAuthorizerContextKey{}).(ObjectAuthorizer)
	return authorizer, ok && authorizer != nil
}

func applyItemFilterToListOptions(ctx context.Context, opts *storage.ListOptions) {
	if filter, ok := ItemFilterFromContext(ctx); ok {
		opts.ItemFilter = filter
	}
}

func authorizeObject(ctx context.Context, obj client.Object) (bool, error) {
	authorizer, ok := ObjectAuthorizerFromContext(ctx)
	if !ok {
		return true, nil
	}
	return authorizer(ctx, obj)
}
