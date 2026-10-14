package authz

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strings"
	"sync/atomic"

	"github.com/go-logr/logr"
	"github.com/google/uuid"

	"github.com/openshift-online/gecko/orlop/pkg/apiserver/constants"
	"github.com/openshift-online/gecko/orlop/pkg/apiserver/handlers"
	"github.com/openshift-online/gecko/orlop/pkg/apiserver/storage"
	"github.com/openshift-online/gecko/orlop/pkg/apiserver/types"
	"github.com/openshift-online/gecko/platform-api/pkg/authn"
	"github.com/openshift-online/gecko/platform-api/pkg/publicaccess"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	authorizationReasonInvalidPath             = "invalid_request_path"
	authorizationReasonClusterScopedRequest    = "cluster_scoped_request"
	authorizationReasonUnsupportedOperation    = "unsupported_operation"
	authorizationReasonListPermissionCheck     = "list_permission_check_failed"
	authorizationReasonObjectContextBuild      = "object_context_build_failed"
	authorizationReasonPolicyEvaluation        = "policy_evaluation_failed"
	authorizationReasonObjectNamespaceMismatch = "object_namespace_mismatch"
)

var (
	errAuthorizationNotConfigured        = errors.New("authorization is not configured")
	errAuthorizationListPermissionCheck  = errors.New("authorization list permission check failed")
	errAuthorizationObjectContextBuild   = errors.New("authorization object context build failed")
	errAuthorizationPolicyEvaluation     = errors.New("authorization policy evaluation failed")
	errAuthorizationObjectNamespaceMatch = errors.New("authorization object namespace mismatch")
)

// Middleware enforces Cedar authorization for public CRUD API requests.
func Middleware(authorizer *Authorizer, logger logr.Logger, resources []types.ResourceInfo) func(http.Handler) http.Handler {
	if logger.GetSink() == nil {
		logger = logr.Discard()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requestID := uuid.NewString()
			requestLogger := logger.WithValues("requestID", requestID)
			w.Header().Set(constants.HeaderRequestID, requestID)

			if isMetadataPath(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}
			if publicaccess.IsAuthorizationExemptRequest(r, resources) {
				next.ServeHTTP(w, r)
				return
			}

			email, ok := authn.UserFromContext(r.Context())
			if !ok {
				writeForbidden(w)
				return
			}
			if authorizer == nil {
				requestLogger.Error(errAuthorizationNotConfigured, "authorization denied", "reason", "authorizer_not_configured")
				writeForbidden(w)
				return
			}

			requestInfo, err := parseRequestPath(r.URL.Path)
			if err != nil {
				requestLogger.Info("authorization denied", "method", r.Method, "reason", authorizationReasonInvalidPath)
				writeForbidden(w)
				return
			}
			if requestInfo.namespace == "" {
				// Do not return resources across namespaces without an explicit
				// namespace authorization strategy.
				requestLogger.Info("authorization denied", "reason", authorizationReasonClusterScopedRequest)
				writeForbidden(w)
				return
			}

			action, err := actionForRequest(r.Method, requestInfo.plural, requestInfo.named)
			if err != nil {
				requestLogger.Info("authorization denied", "method", r.Method, "reason", authorizationReasonUnsupportedOperation)
				writeForbidden(w)
				return
			}
			if r.Method == http.MethodGet && !requestInfo.named {
				handleListAuthorization(w, r, next, authorizer, requestLogger, email, action, requestInfo)
				return
			}

			objectAuthorizer := objectAuthorizer(authorizer, requestLogger, email, action, requestInfo, r.Method)
			ctx := handlers.WithObjectAuthorizer(r.Context(), objectAuthorizer)
			next.ServeHTTP(w, r.WithContext(ctx))
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
			result.named = true
			return result, nil
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

// handleListAuthorization authorizes a namespaced collection GET request.
// Binding conditions are evaluated only for each returned object.
func handleListAuthorization(
	w http.ResponseWriter,
	r *http.Request,
	next http.Handler,
	authorizer *Authorizer,
	logger logr.Logger,
	email string,
	action Action,
	request parsedRequest,
) {
	allowed, err := authorizer.HasNamespacePermission(r.Context(), email, action, request.namespace)
	if err != nil {
		logger.Error(errAuthorizationListPermissionCheck, "authorization denied", "resource", request.plural, "action", string(action), "reason", authorizationReasonListPermissionCheck)
		writeForbidden(w)
		return
	}
	if !allowed {
		writeForbidden(w)
		return
	}

	failures := &listAuthorizationFailures{}
	ctx := handlers.WithItemFilter(r.Context(), itemFilter(authorizer, email, action, request.plural, r.Method, failures))
	next.ServeHTTP(w, r.WithContext(ctx))
	failures.log(logger, request.plural, action)
}

func itemFilter(authorizer *Authorizer, email string, action Action, resourcePlural, method string, failures *listAuthorizationFailures) storage.ItemFilter {
	return func(ctx context.Context, object client.Object) (bool, error) {
		switch authorizeListItem(ctx, authorizer, email, action, resourcePlural, method, object) {
		case listItemAuthorizationAllowed:
			return true, nil
		case listItemAuthorizationContextBuildFailed:
			failures.contextBuild.Add(1)
			return false, nil
		case listItemAuthorizationPolicyEvaluationFailed:
			failures.policyEvaluation.Add(1)
			return false, nil
		default:
			return false, nil
		}
	}
}

type listItemAuthorizationOutcome int

const (
	listItemAuthorizationDenied listItemAuthorizationOutcome = iota
	listItemAuthorizationAllowed
	listItemAuthorizationContextBuildFailed
	listItemAuthorizationPolicyEvaluationFailed
)

func authorizeListItem(ctx context.Context, authorizer *Authorizer, email string, action Action, resourcePlural, method string, object client.Object) listItemAuthorizationOutcome {
	if object == nil || object.GetNamespace() == "" {
		return listItemAuthorizationContextBuildFailed
	}
	requestContext, err := CedarContextForObject(object, resourcePlural, method)
	if err != nil {
		return listItemAuthorizationContextBuildFailed
	}
	allowed, err := authorizer.AuthorizeWithContext(ctx, email, action, object.GetNamespace(), requestContext)
	if err != nil {
		return listItemAuthorizationPolicyEvaluationFailed
	}
	if allowed {
		return listItemAuthorizationAllowed
	}
	return listItemAuthorizationDenied
}

func objectAuthorizer(authorizer *Authorizer, logger logr.Logger, email string, action Action, request parsedRequest, method string) handlers.ObjectAuthorizer {
	return func(ctx context.Context, object client.Object) (bool, error) {
		if object == nil || object.GetNamespace() != request.namespace {
			logger.Error(errAuthorizationObjectNamespaceMatch, "authorization denied", "resource", request.plural, "action", string(action), "reason", authorizationReasonObjectNamespaceMismatch)
			return false, errAuthorizationObjectNamespaceMatch
		}
		requestContext, err := CedarContextForObject(object, request.plural, method)
		if err != nil {
			logger.Error(errAuthorizationObjectContextBuild, "authorization denied", "resource", request.plural, "action", string(action), "reason", authorizationReasonObjectContextBuild)
			return false, errAuthorizationObjectContextBuild
		}
		allowed, err := authorizer.AuthorizeWithContext(ctx, email, action, request.namespace, requestContext)
		if err != nil {
			logger.Error(errAuthorizationPolicyEvaluation, "authorization denied", "resource", request.plural, "action", string(action), "reason", authorizationReasonPolicyEvaluation)
			return false, errAuthorizationPolicyEvaluation
		}
		return allowed, nil
	}
}

type listAuthorizationFailures struct {
	contextBuild     atomic.Int64
	policyEvaluation atomic.Int64
}

func (f *listAuthorizationFailures) log(logger logr.Logger, resourcePlural string, action Action) {
	if count := f.contextBuild.Load(); count > 0 {
		logger.Error(errAuthorizationObjectContextBuild, "authorization list item evaluation failed", "resource", resourcePlural, "action", string(action), "reason", authorizationReasonObjectContextBuild, "failureCount", count)
	}
	if count := f.policyEvaluation.Load(); count > 0 {
		logger.Error(errAuthorizationPolicyEvaluation, "authorization list item evaluation failed", "resource", resourcePlural, "action", string(action), "reason", authorizationReasonPolicyEvaluation, "failureCount", count)
	}
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
