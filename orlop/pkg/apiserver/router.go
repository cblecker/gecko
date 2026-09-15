package apiserver

import (
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/openshift-online/gecko/orlop/pkg/apiserver/conversion"
	"github.com/openshift-online/gecko/orlop/pkg/apiserver/handlers"
	"github.com/openshift-online/gecko/orlop/pkg/apiserver/middleware"

	"k8s.io/apimachinery/pkg/runtime"
)

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

	// Add CORS middleware
	r.Use(middleware.CORS(middleware.CORSOptions{
		AllowedOrigins: corsOrigins,
	}))

	registerHealthEndpoints(r, check)

	for _, mw := range customMiddleware {
		r.Use(mw)
	}

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
				plural := res.Plural
				r.Post("/"+plural, handler.Create)
				r.Get("/"+plural, handler.List)
				r.Get("/"+plural+"/{name}", handler.Get)
				r.Put("/"+plural+"/{name}", handler.Update)
				r.Patch("/"+plural+"/{name}", handler.Patch)
				r.Delete("/"+plural+"/{name}", handler.Delete)
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
				r.Get("/"+res.Plural, handler.List)
				namespacedHandlers = append(namespacedHandlers, convertingEntry{res, res.Plural, handler})
			}
			if len(namespacedHandlers) > 0 {
				r.Route("/namespaces/{namespace}", func(r chi.Router) {
					for _, nh := range namespacedHandlers {
						r.Post("/"+nh.plural, nh.handler.Create)
						r.Get("/"+nh.plural, nh.handler.List)
						r.Get("/"+nh.plural+"/{name}", nh.handler.Get)
						r.Put("/"+nh.plural+"/{name}", nh.handler.Update)
						r.Patch("/"+nh.plural+"/{name}", nh.handler.Patch)
						r.Delete("/"+nh.plural+"/{name}", nh.handler.Delete)
						// Status updates not allowed on public API (GCP-1062)

						if nh.res.ParentResource != nil {
							parentPlural := nh.res.ParentResource.Plural
							idField := nh.res.ParentResource.IDField
							childPlural := nh.plural
							handler := nh.handler
							r.Route("/"+parentPlural+"/{parentID}/"+childPlural, func(r chi.Router) {
								r.Use(parentFilterMiddleware(idField, "parentID"))
								r.Post("/", handler.Create)
								r.Get("/", handler.List)
								r.Get("/{name}", handler.Get)
								r.Put("/{name}", handler.Update)
								r.Patch("/{name}", handler.Patch)
								r.Delete("/{name}", handler.Delete)
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
