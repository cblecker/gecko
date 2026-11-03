package apiserver

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-logr/logr"

	"github.com/openshift-online/gecko/orlop/pkg/apiserver/constants"
	"github.com/openshift-online/gecko/orlop/pkg/apiserver/conversion"
	"github.com/openshift-online/gecko/orlop/pkg/apiserver/handlers"
	"github.com/openshift-online/gecko/orlop/pkg/apiserver/middleware"
	"github.com/openshift-online/gecko/orlop/pkg/apiserver/types"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// notImplementedHandler returns an http.HandlerFunc that responds with
// 501 Not Implemented for verbs not listed in +orlop:public-verbs.
func notImplementedHandler(verb string, logger logr.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotImplemented)
		if err := json.NewEncoder(w).Encode(metav1.Status{
			TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"},
			Status:   metav1.StatusFailure,
			Message:  fmt.Sprintf("verb %q is not supported for this resource", verb),
			Reason:   metav1.StatusReasonMethodNotAllowed,
			Code:     http.StatusNotImplemented,
		}); err != nil {
			// Headers are committed; a second response cannot repair the write.
			// Do not log raw writer errors, which may contain request data.
			logger.Error(nil, "failed to write unsupported-operation response", "reason", "response_write_failed")
		}
	}
}

// restrictWatch enforces the declared watch capability independently of
// authorization. List and watch share a GET route, but are separate API verbs.
func restrictWatch(next http.HandlerFunc, info types.ResourceInfo, logger logr.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get(constants.QueryParamWatch) == "true" && !info.VerbAllowed("watch") {
			notImplementedHandler("watch", logger)(w, r)
			return
		}
		next(w, r)
	}
}

// registerVerbsForResource registers HTTP handlers for each standard verb on
// path prefix p (e.g., "/clusters"). Verbs not permitted by info.VerbAllowed
// are wired to notImplementedHandler so callers receive 501 rather than 404.
func registerVerbsForResource(r chi.Router, p string, h *handlers.ConvertingResourceHandler, info types.ResourceInfo, logger logr.Logger) {
	// Collection endpoints
	if info.VerbAllowed("create") {
		r.Post("/"+p, h.Create)
	} else {
		r.Post("/"+p, notImplementedHandler("create", logger))
	}
	if info.VerbAllowed("list") {
		r.Get("/"+p, restrictWatch(h.List, info, logger))
	} else {
		r.Get("/"+p, notImplementedHandler("list", logger))
	}
	// Item endpoints
	if info.VerbAllowed("get") {
		r.Get("/"+p+"/{name}", h.Get)
	} else {
		r.Get("/"+p+"/{name}", notImplementedHandler("get", logger))
	}
	if info.VerbAllowed("update") {
		r.Put("/"+p+"/{name}", h.Update)
	} else {
		r.Put("/"+p+"/{name}", notImplementedHandler("update", logger))
	}
	if info.VerbAllowed("patch") {
		r.Patch("/"+p+"/{name}", h.Patch)
	} else {
		r.Patch("/"+p+"/{name}", notImplementedHandler("patch", logger))
	}
	if info.VerbAllowed("delete") {
		r.Delete("/"+p+"/{name}", h.Delete)
	} else {
		r.Delete("/"+p+"/{name}", notImplementedHandler("delete", logger))
	}
}

// registerVerbsForNestedResource registers HTTP handlers for a nested
// (parent/child) route prefix (e.g., "/"). Verb restrictions from info apply.
func registerVerbsForNestedResource(r chi.Router, h *handlers.ConvertingResourceHandler, info types.ResourceInfo, logger logr.Logger) {
	if info.VerbAllowed("create") {
		r.Post("/", h.Create)
	} else {
		r.Post("/", notImplementedHandler("create", logger))
	}
	if info.VerbAllowed("list") {
		r.Get("/", restrictWatch(h.List, info, logger))
	} else {
		r.Get("/", notImplementedHandler("list", logger))
	}
	if info.VerbAllowed("get") {
		r.Get("/{name}", h.Get)
	} else {
		r.Get("/{name}", notImplementedHandler("get", logger))
	}
	if info.VerbAllowed("update") {
		r.Put("/{name}", h.Update)
	} else {
		r.Put("/{name}", notImplementedHandler("update", logger))
	}
	if info.VerbAllowed("patch") {
		r.Patch("/{name}", h.Patch)
	} else {
		r.Patch("/{name}", notImplementedHandler("patch", logger))
	}
	if info.VerbAllowed("delete") {
		r.Delete("/{name}", h.Delete)
	} else {
		r.Delete("/{name}", notImplementedHandler("delete", logger))
	}
}

// healthChecker is a function that tests storage backend connectivity.
// It returns nil if the backend is healthy, or an error describing the problem.
type healthChecker func() error

func registerHealthEndpoints(r chi.Router, check healthChecker) {
	handler := func(w http.ResponseWriter, _ *http.Request) {
		if check != nil {
			if err := check(); err != nil {
				w.WriteHeader(http.StatusServiceUnavailable)
				w.Write([]byte("storage check failed: " + err.Error()))
				return
			}
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}
	r.Get("/healthz", handler)
	r.Get("/readyz", handler)
}

// setupConvertingRouter configures the HTTP router with converting handlers for public API.
// publicRegistry defines the public API types and schemas.
// privateRegistry provides the shared storage backend.
func setupConvertingRouter(publicRegistry *ResourceRegistry, privateRegistry *ResourceRegistry, converter *conversion.Converter, privateScheme *runtime.Scheme, corsOrigins []string, customMiddleware []func(http.Handler) http.Handler, check healthChecker) (chi.Router, error) {
	r := chi.NewRouter()
	logger := publicRegistry.logger

	// Add CORS middleware
	r.Use(middleware.CORS(middleware.CORSOptions{
		AllowedOrigins: corsOrigins,
	}))

	for _, mw := range customMiddleware {
		r.Use(mw)
	}

	registerHealthEndpoints(r, check)

	// Create discovery handler using public registry (for public API types)
	// Public API does not advertise status subresource (GCP-1062)
	advertiseStatus := false
	discoveryHandler := handlers.NewDiscoveryHandler(publicRegistry, &handlers.DiscoveryOptions{
		AdvertiseStatus: &advertiseStatus,
	})

	// Discovery endpoints (must be registered BEFORE resource routes to avoid shadowing)
	r.Get("/apis", discoveryHandler.APIGroupList)
	r.Get("/openapi/v2", discoveryHandler.OpenAPIV2)
	r.Get("/openapi/v3", discoveryHandler.OpenAPIV3)

	// Group resources by GroupVersion (from public registry)
	gvResources := make(map[string][]ResourceInfo)
	for _, res := range publicRegistry.GetResources() {
		gv := fmt.Sprintf("%s/%s", res.GVK.Group, res.GVK.Version)
		gvResources[gv] = append(gvResources[gv], res)
	}

	// Setup routes for each GroupVersion
	// Errors from handler construction are captured here and returned
	// after all routes are processed, so a misconfigured resource is
	// never silently advertised in discovery with no routes.
	var routeErr error
	for gv, resources := range gvResources {
		group := resources[0].GVK.Group
		version := resources[0].GVK.Version
		apiPath := "/apis/" + gv

		r.Route(apiPath, func(r chi.Router) {
			// Discovery endpoint for this specific group/version (before namespaced routes)
			r.Get("/", func(w http.ResponseWriter, req *http.Request) {
				discoveryHandler.APIResourceList(w, req, group, version)
			})

			// Cluster-scoped resources get CRUD directly under the GV path
			for _, res := range resources {
				if res.Namespaced {
					continue
				}
				handlerInterface, err := createConvertingHandlerWithSharedStore(publicRegistry, privateRegistry, converter, privateScheme, res)
				if err != nil {
					routeErr = fmt.Errorf("resource %s/%s %s: %w", res.GVK.Group, res.GVK.Version, res.Plural, err)
					return
				}
				handler := handlerInterface.(*handlers.ConvertingResourceHandler)
				registerVerbsForResource(r, res.Plural, handler, res, logger)
				// Status updates not allowed on public API (GCP-1062)
			}

			// Namespaced resources: LIST across all namespaces + CRUD under /namespaces/{namespace}
			type convertingEntry struct {
				res     ResourceInfo
				plural  string
				handler *handlers.ConvertingResourceHandler
			}
			var namespacedHandlers []convertingEntry
			for _, res := range resources {
				if !res.Namespaced {
					continue
				}
				handlerInterface, err := createConvertingHandlerWithSharedStore(publicRegistry, privateRegistry, converter, privateScheme, res)
				if err != nil {
					routeErr = fmt.Errorf("resource %s/%s %s: %w", res.GVK.Group, res.GVK.Version, res.Plural, err)
					return
				}
				handler := handlerInterface.(*handlers.ConvertingResourceHandler)
				// Cross-namespace list: honor verb restriction
				if res.VerbAllowed("list") {
					r.Get("/"+res.Plural, restrictWatch(handler.List, res, logger))
				} else {
					r.Get("/"+res.Plural, notImplementedHandler("list", logger))
				}
				namespacedHandlers = append(namespacedHandlers, convertingEntry{res, res.Plural, handler})
			}
			if len(namespacedHandlers) > 0 {
				r.Route("/namespaces/{namespace}", func(r chi.Router) {
					for _, nh := range namespacedHandlers {
						registerVerbsForResource(r, nh.plural, nh.handler, nh.res, logger)
						// Status updates not allowed on public API (GCP-1062)

						if nh.res.ParentResource != nil {
							parentPlural := nh.res.ParentResource.Plural
							idField := nh.res.ParentResource.IDField
							childPlural := nh.plural
							handler := nh.handler
							res := nh.res
							r.Route("/"+parentPlural+"/{parentID}/"+childPlural, func(r chi.Router) {
								r.Use(parentFilterMiddleware(idField, "parentID"))
								registerVerbsForNestedResource(r, handler, res, logger)
								// Status updates not allowed on public API (GCP-1062)
							})
						}
					}
				})
			}
		})

		if routeErr != nil {
			return nil, fmt.Errorf("setting up converting routes: %w", routeErr)
		}

		// Per-group discovery endpoint
		r.Get("/apis/"+group, func(w http.ResponseWriter, req *http.Request) {
			discoveryHandler.APIGroup(w, req, group)
		})

		// OpenAPI v3 per-group-version endpoint
		r.Get("/openapi/v3/apis/"+gv, func(w http.ResponseWriter, req *http.Request) {
			discoveryHandler.OpenAPIV3GroupVersion(w, req, group, version)
		})
	}

	return r, nil
}
