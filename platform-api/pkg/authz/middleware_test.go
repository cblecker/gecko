package authz

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-logr/logr"

	"github.com/openshift-online/gecko/orlop/pkg/apiserver/types"
	"github.com/openshift-online/gecko/platform-api/pkg/authn"

	runtimeschema "k8s.io/apimachinery/pkg/runtime/schema"
)

func TestMiddlewareAllowsAuthorizationExemptCatalogGets(t *testing.T) {
	resources := []types.ResourceInfo{
		{
			GVK:                      runtimeschema.GroupVersionKind{Group: "gcp.managed.openshift.io", Version: "v1", Kind: "Version"},
			Plural:                   "versions",
			Verbs:                    []string{"get", "list"},
			AuthorizationExemptVerbs: []string{"get", "list"},
		},
		{
			GVK:                      runtimeschema.GroupVersionKind{Group: "gcp.managed.openshift.io", Version: "v1", Kind: "Channel"},
			Plural:                   "channels",
			Verbs:                    []string{"get", "list"},
			AuthorizationExemptVerbs: []string{"get", "list"},
		},
	}
	tests := []struct {
		name   string
		method string
		path   string
		status int
	}{
		{name: "list versions", method: http.MethodGet, path: "/apis/gcp.managed.openshift.io/v1/versions", status: http.StatusNoContent},
		{name: "get version", method: http.MethodGet, path: "/apis/gcp.managed.openshift.io/v1/versions/4.22.1", status: http.StatusNoContent},
		{name: "list channels", method: http.MethodGet, path: "/apis/gcp.managed.openshift.io/v1/channels", status: http.StatusNoContent},
		{name: "get channel", method: http.MethodGet, path: "/apis/gcp.managed.openshift.io/v1/channels/stable", status: http.StatusNoContent},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := httptest.NewRequestWithContext(context.Background(), tt.method, tt.path, nil)
			response := httptest.NewRecorder()
			Middleware(nil, logr.Discard(), resources)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			})).ServeHTTP(response, request)
			if response.Code != tt.status {
				t.Fatalf("status = %d, want %d", response.Code, tt.status)
			}
		})
	}
}

func TestMiddlewareDeniesNonExemptClusterScopedRequests(t *testing.T) {
	resources := []types.ResourceInfo{{
		GVK:                      runtimeschema.GroupVersionKind{Group: "gcp.managed.openshift.io", Version: "v1", Kind: "Version"},
		Plural:                   "versions",
		Verbs:                    []string{"get", "list"},
		AuthorizationExemptVerbs: []string{"get", "list"},
	}}
	tests := []struct {
		name   string
		method string
		path   string
	}{
		{name: "write", method: http.MethodPost, path: "/apis/gcp.managed.openshift.io/v1/versions"},
		{name: "watch", method: http.MethodGet, path: "/apis/gcp.managed.openshift.io/v1/versions?watch=true"},
		{name: "other cluster resource", method: http.MethodGet, path: "/apis/gcp.managed.openshift.io/v1/platformroles"},
		{name: "nested item path", method: http.MethodGet, path: "/apis/gcp.managed.openshift.io/v1/versions/4.22.1/status"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := authn.WithUser(context.Background(), "alice@example.com")
			request := httptest.NewRequestWithContext(ctx, tt.method, tt.path, nil)
			response := httptest.NewRecorder()
			Middleware(nil, logr.Discard(), resources)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				t.Fatal("denied request reached the next handler")
			})).ServeHTTP(response, request)
			if response.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusForbidden)
			}
		})
	}
}

func TestMiddlewareDoesNotBypassAuthorizationForClusterOrNodePool(t *testing.T) {
	resources := []types.ResourceInfo{
		{
			GVK:                      runtimeschema.GroupVersionKind{Group: "gcp.managed.openshift.io", Version: "v1", Kind: "Version"},
			Plural:                   "versions",
			AuthorizationExemptVerbs: []string{"get", "list"},
		},
		{
			GVK:    runtimeschema.GroupVersionKind{Group: "gcp.managed.openshift.io", Version: "v1", Kind: "Cluster"},
			Plural: "clusters",
		},
		{
			GVK:        runtimeschema.GroupVersionKind{Group: "gcp.managed.openshift.io", Version: "v1", Kind: "NodePool"},
			Plural:     "nodepools",
			Namespaced: true,
		},
	}
	for _, requestPath := range []string{
		"/apis/gcp.managed.openshift.io/v1/clusters",
		"/apis/gcp.managed.openshift.io/v1/clusters/example",
		"/apis/gcp.managed.openshift.io/v1/namespaces/project-a/nodepools",
		"/apis/gcp.managed.openshift.io/v1/namespaces/project-a/nodepools/example",
	} {
		t.Run(requestPath, func(t *testing.T) {
			request := httptest.NewRequestWithContext(context.Background(), http.MethodGet, requestPath, nil)
			response := httptest.NewRecorder()
			Middleware(nil, logr.Discard(), resources)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				t.Fatal("authorization-exempt request reached the next handler")
			})).ServeHTTP(response, request)
			if response.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusForbidden)
			}
		})
	}
}

func TestMiddlewareFailsClosedWithoutAuthorizer(t *testing.T) {
	resources := []types.ResourceInfo{{
		GVK:        runtimeschema.GroupVersionKind{Group: "gcp.managed.openshift.io", Version: "v1", Kind: "NodePool"},
		Plural:     "nodepools",
		Namespaced: true,
	}}
	ctx := authn.WithUser(context.Background(), "alice@example.com")
	request := httptest.NewRequestWithContext(ctx, http.MethodGet, "/apis/gcp.managed.openshift.io/v1/namespaces/project-a/nodepools", nil)
	response := httptest.NewRecorder()
	Middleware(nil, logr.Discard(), resources)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("request reached the next handler without an authorizer")
	})).ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusForbidden)
	}
}

func TestParseClusterChildRequestPath(t *testing.T) {
	tests := []struct {
		name   string
		path   string
		plural string
		named  bool
	}{
		{name: "cluster item", path: "/apis/gcp.managed.openshift.io/v1/namespaces/customer/clusters/example", plural: "clusters", named: true},
		{name: "nodepool collection", path: "/apis/gcp.managed.openshift.io/v1/namespaces/customer/clusters/example/nodepools", plural: "nodepools"},
		{name: "upgrade policy collection", path: "/apis/gcp.managed.openshift.io/v1/namespaces/customer/clusters/example/controlplaneupgradepolicies", plural: "controlplaneupgradepolicies"},
		{name: "upgrade request collection", path: "/apis/gcp.managed.openshift.io/v1/namespaces/customer/clusters/example/controlplaneupgraderequests", plural: "controlplaneupgraderequests"},
		{name: "upgrade request item", path: "/apis/gcp.managed.openshift.io/v1/namespaces/customer/clusters/example/controlplaneupgraderequests/request-1", plural: "controlplaneupgraderequests", named: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseRequestPath(tt.path)
			if err != nil {
				t.Fatal(err)
			}
			if got.namespace != "customer" || got.plural != tt.plural || got.named != tt.named {
				t.Fatalf("parsed request = %+v, want namespace customer, plural %q, named %t", got, tt.plural, tt.named)
			}
		})
	}
}

func TestUnknownClusterChildRequestIsDenied(t *testing.T) {
	parsed, err := parseRequestPath("/apis/gcp.managed.openshift.io/v1/namespaces/customer/clusters/example/unknown")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := actionForRequest(http.MethodGet, parsed.plural, parsed.named); err == nil {
		t.Fatal("expected authorization to reject an unknown child resource")
	}
}
