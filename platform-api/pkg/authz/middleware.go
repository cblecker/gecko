package authz

import (
	"fmt"
	"net/http"
	"path"
	"strings"

	"github.com/go-logr/logr"

	"github.com/openshift-online/gecko/orlop/pkg/apiserver/types"
	"github.com/openshift-online/gecko/platform-api/pkg/authn"
	"github.com/openshift-online/gecko/platform-api/pkg/publicaccess"
)

// Middleware enforces Cedar authorization for the public CRUD API. The
// object-state-aware and per-item list phases are not part of this foundation;
// it authorizes namespace-scoped operations and fails closed for
// cross-namespace collection requests.
func Middleware(authorizer *Authorizer, logger logr.Logger, authorizationExemptResources []types.ResourceInfo) func(http.Handler) http.Handler {
	if logger.GetSink() == nil {
		logger = logr.Discard()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isMetadataPath(r.URL.Path) || publicaccess.IsAuthorizationExemptRequest(r, authorizationExemptResources) {
				next.ServeHTTP(w, r)
				return
			}

			email, ok := authn.UserFromContext(r.Context())
			if !ok {
				writeForbidden(w)
				return
			}
			if authorizer == nil {
				logger.Error(fmt.Errorf("authorizer is not configured"), "authorization denied")
				writeForbidden(w)
				return
			}

			requestInfo, err := parseRequestPath(r.URL.Path)
			if err != nil {
				logger.Info("authorization denied for invalid public API path", "method", r.Method, "error", err)
				writeForbidden(w)
				return
			}
			if requestInfo.namespace == "" {
				// Cross-namespace filtering is not implemented in this
				// foundation. Do not return all namespaces while it is absent.
				writeForbidden(w)
				return
			}

			action, err := actionForRequest(r.Method, requestInfo.plural, requestInfo.named)
			if err != nil {
				logger.Info("authorization denied for unsupported public API operation", "method", r.Method, "resource", requestInfo.plural)
				writeForbidden(w)
				return
			}
			allowed, err := authorizer.Authorize(r.Context(), email, action, requestInfo.namespace)
			if err != nil {
				logger.Error(err, "authorization evaluation failed closed", "method", r.Method, "resource", requestInfo.plural, "namespace", requestInfo.namespace)
				writeForbidden(w)
				return
			}
			if !allowed {
				writeForbidden(w)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

type parsedRequest struct {
	namespace string
	plural    string
	named     bool
}

func parseRequestPath(rawPath string) (parsedRequest, error) {
	cleanPath := path.Clean(rawPath)
	if cleanPath != rawPath {
		return parsedRequest{}, fmt.Errorf("non-canonical path")
	}
	if strings.Contains(rawPath, "..") {
		return parsedRequest{}, fmt.Errorf("path traversal")
	}

	parts := strings.Split(strings.Trim(cleanPath, "/"), "/")
	if len(parts) < 4 || parts[0] != "apis" {
		return parsedRequest{}, fmt.Errorf("not a public resource path")
	}
	// parts[1] is the API group and parts[2] is the version.
	i := 3
	result := parsedRequest{}
	if parts[i] == "namespaces" {
		if len(parts) <= i+2 || parts[i+1] == "" {
			return parsedRequest{}, fmt.Errorf("namespace and resource are required")
		}
		result.namespace = parts[i+1]
		result.plural = parts[i+2]
		i += 3
	} else {
		result.plural = parts[i]
		i++
	}

	// Cluster children also have nested parent routes:
	// /namespaces/{namespace}/clusters/{clusterID}/{childResource}.
	if result.plural == "clusters" && i < len(parts) {
		i++ // parent cluster name
		if i == len(parts) {
			return parsedRequest{namespace: result.namespace, plural: result.plural, named: true}, nil
		}
		// Authorization checks the child action; the router checks whether this
		// resource actually has a nested Cluster route.
		result.plural = parts[i]
		i++
	}

	if i < len(parts) {
		if i+1 != len(parts) || parts[i] == "" {
			return parsedRequest{}, fmt.Errorf("invalid resource path")
		}
		result.named = true
		i++
	}
	if i != len(parts) {
		return parsedRequest{}, fmt.Errorf("invalid resource path")
	}
	if result.namespace == "" && result.named {
		return parsedRequest{}, fmt.Errorf("cluster-wide named resources are not public")
	}
	return result, nil
}

func isMetadataPath(requestPath string) bool {
	switch requestPath {
	case "/healthz", "/readyz", "/livez", "/apis", "/apis/":
		return true
	}
	if strings.HasPrefix(requestPath, "/openapi/") {
		return true
	}
	parts := strings.Split(strings.Trim(requestPath, "/"), "/")
	return len(parts) == 2 && parts[0] == "apis" || len(parts) == 3 && parts[0] == "apis"
}

func writeForbidden(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	_, _ = w.Write([]byte(`{"error":"forbidden"}`))
}
