package apiserver

import (
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/openshift-online/gecko/orlop/pkg/apiserver/conversion"
	"github.com/openshift-online/gecko/orlop/pkg/apiserver/handlers"
	"k8s.io/apimachinery/pkg/runtime"
)

// createConvertingHandlerWithSharedStore creates a converting handler that uses the private registry's store
// but the public registry's schema and types.
func createConvertingHandlerWithSharedStore(publicRegistry *ResourceRegistry, privateRegistry *ResourceRegistry, converter *conversion.Converter, privateScheme *runtime.Scheme, publicRes ResourceInfo) (interface{}, error) {
	// Get store from private registry (shared storage)
	gk := publicRes.GVK.GroupKind()
	store := privateRegistry.GetStore(gk)
	if store == nil {
		return nil, fmt.Errorf("no store found for resource %s in private registry", gk)
	}

	// Create schema processor with public API metadata constraints.
	// Uses createPublicProcessor (not createProcessor) so that
	// injectPublicMetadataSchema constrains metadata to only public-facing
	// fields — pruning.Prune() strips finalizers, ownerReferences,
	// managedFields, deletionTimestamp, etc. from public API inputs.
	processor, err := publicRegistry.createPublicProcessor(publicRes.SchemaYAML)
	if err != nil {
		return nil, fmt.Errorf("failed to create processor for %s: %w", publicRes.Plural, err)
	}

	// Create converting handler
	handler := handlers.NewConvertingResourceHandler(
		store,         // Store from private registry
		processor,     // Processor from public schema
		converter,     // Converter between public and private
		publicRes.GVK, // Public GVK
		publicRes.Plural,
		publicRegistry.scheme,    // Public scheme
		privateScheme,            // Private scheme
		publicRes.PrinterColumns, // Printer columns for Table format
		publicRegistry.logger.WithValues("resource", publicRes.Plural),
	)

	return handler, nil
}

func parentFilterMiddleware(idField, urlParam string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			parentID := chi.URLParam(r, urlParam)
			pf := handlers.ParentFilter{IDField: idField, ID: parentID}
			ctx := handlers.WithParentFilter(r.Context(), pf)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
