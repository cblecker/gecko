package publicaccess

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openshift-online/gecko/orlop/pkg/apiserver/types"

	runtimeschema "k8s.io/apimachinery/pkg/runtime/schema"
)

func TestIsAuthorizationExemptRequest(t *testing.T) {
	resources := []types.ResourceInfo{
		{
			GVK:                      runtimeschema.GroupVersionKind{Group: "gcp.managed.openshift.io", Version: "v1", Kind: "Version"},
			Plural:                   "versions",
			Verbs:                    []string{"get", "list"},
			AuthorizationExemptVerbs: []string{"get", "list"},
		},
		{
			GVK:                      runtimeschema.GroupVersionKind{Group: "gcp.managed.openshift.io", Version: "v1", Kind: "PlatformRole"},
			Plural:                   "platformroles",
			Verbs:                    []string{"get", "list"},
			AuthorizationExemptVerbs: []string{"get", "list"},
		},
		{
			GVK:                      runtimeschema.GroupVersionKind{Group: "gcp.managed.openshift.io", Version: "v1", Kind: "Channel"},
			Plural:                   "channels",
			Verbs:                    []string{"get", "list"},
			AuthorizationExemptVerbs: []string{"get", "list"},
		},
		{
			GVK:                      runtimeschema.GroupVersionKind{Group: "gcp.managed.openshift.io", Version: "v1", Kind: "WatchableCatalog"},
			Plural:                   "watchablecatalogs",
			Verbs:                    []string{"get", "list", "watch"},
			AuthorizationExemptVerbs: []string{"get", "list", "watch"},
		},
		{
			GVK:    runtimeschema.GroupVersionKind{Group: "gcp.managed.openshift.io", Version: "v1", Kind: "Unmarked"},
			Plural: "unmarkedresources",
			Verbs:  []string{"get", "list"},
		},
		{
			GVK:    runtimeschema.GroupVersionKind{Group: "gcp.managed.openshift.io", Version: "v1", Kind: "Cluster"},
			Plural: "clusters",
			Verbs:  []string{"get", "list"},
		},
		{
			GVK:        runtimeschema.GroupVersionKind{Group: "gcp.managed.openshift.io", Version: "v1", Kind: "NodePool"},
			Plural:     "nodepools",
			Namespaced: true,
			Verbs:      []string{"get", "list"},
		},
		{
			GVK:                      runtimeschema.GroupVersionKind{Group: "gcp.managed.openshift.io", Version: "v1", Kind: "Namespaced"},
			Plural:                   "namespacedresources",
			Namespaced:               true,
			Verbs:                    []string{"get", "list"},
			AuthorizationExemptVerbs: []string{"get", "list"},
		},
	}

	tests := []struct {
		name   string
		method string
		path   string
		allow  bool
	}{
		{name: "list versions", method: http.MethodGet, path: "/apis/gcp.managed.openshift.io/v1/versions", allow: true},
		{name: "list platformroles", method: http.MethodGet, path: "/apis/gcp.managed.openshift.io/v1/platformroles", allow: true},
		{name: "get version", method: http.MethodGet, path: "/apis/gcp.managed.openshift.io/v1/versions/4.22.1", allow: true},
		{name: "get platform role", method: http.MethodGet, path: "/apis/gcp.managed.openshift.io/v1/platformroles/catalog-reader", allow: true},
		{name: "platform role: patch", method: http.MethodPatch, path: "/apis/gcp.managed.openshift.io/v1/platformroles/catalog-reader"},
		{name: "platform role: delete", method: http.MethodDelete, path: "/apis/gcp.managed.openshift.io/v1/platformroles/catalog-reader"},
		{name: "platform role: wrong version", method: http.MethodGet, path: "/apis/gcp.managed.openshift.io/v2/platformroles"},
		{name: "list channels", method: http.MethodGet, path: "/apis/gcp.managed.openshift.io/v1/channels", allow: true},
		{name: "get channel", method: http.MethodGet, path: "/apis/gcp.managed.openshift.io/v1/channels/stable", allow: true},
		{name: "watch excluded by verbs", method: http.MethodGet, path: "/apis/gcp.managed.openshift.io/v1/versions?watch=true"},
		{name: "platform role: watch excluded by verbs", method: http.MethodGet, path: "/apis/gcp.managed.openshift.io/v1/platformroles?watch=true"},
		{name: "watch allowed by verbs", method: http.MethodGet, path: "/apis/gcp.managed.openshift.io/v1/watchablecatalogs?watch=true", allow: true},
		{name: "list with watch false", method: http.MethodGet, path: "/apis/gcp.managed.openshift.io/v1/versions?watch=false", allow: true},
		{name: "platform role: list with watch false", method: http.MethodGet, path: "/apis/gcp.managed.openshift.io/v1/platformroles?watch=false", allow: true},
		{name: "create collection", method: http.MethodPost, path: "/apis/gcp.managed.openshift.io/v1/versions"},
		{name: "platform role: create collection", method: http.MethodPost, path: "/apis/gcp.managed.openshift.io/v1/platformroles"},
		{name: "update item", method: http.MethodPut, path: "/apis/gcp.managed.openshift.io/v1/versions/4.22.1"},
		{name: "platform role: update item", method: http.MethodPut, path: "/apis/gcp.managed.openshift.io/v1/platformroles/catalog-reader"},
		{name: "status subresource", method: http.MethodGet, path: "/apis/gcp.managed.openshift.io/v1/versions/4.22.1/status"},
		{name: "platform role: status subresource", method: http.MethodGet, path: "/apis/gcp.managed.openshift.io/v1/platformroles/catalog-reader/status"},
		{name: "different group", method: http.MethodGet, path: "/apis/other.example.io/v1/versions/4.22.1"},
		{name: "platform role: different group", method: http.MethodGet, path: "/apis/other.example.io/v1/platformroles/catalog-reader"},
		{name: "unmarked resource", method: http.MethodGet, path: "/apis/gcp.managed.openshift.io/v1/unmarkedresources"},
		{name: "cluster list", method: http.MethodGet, path: "/apis/gcp.managed.openshift.io/v1/clusters"},
		{name: "cluster get", method: http.MethodGet, path: "/apis/gcp.managed.openshift.io/v1/clusters/example"},
		{name: "node pool list", method: http.MethodGet, path: "/apis/gcp.managed.openshift.io/v1/namespaces/project-a/nodepools"},
		{name: "node pool get", method: http.MethodGet, path: "/apis/gcp.managed.openshift.io/v1/namespaces/project-a/nodepools/example"},
		{name: "namespaced resource without namespace", method: http.MethodGet, path: "/apis/gcp.managed.openshift.io/v1/namespacedresources"},
		{name: "namespaced collection", method: http.MethodGet, path: "/apis/gcp.managed.openshift.io/v1/namespaces/project-a/namespacedresources", allow: true},
		{name: "namespaced item", method: http.MethodGet, path: "/apis/gcp.managed.openshift.io/v1/namespaces/project-a/namespacedresources/example", allow: true},
		{name: "namespace route", method: http.MethodGet, path: "/apis/gcp.managed.openshift.io/v1/namespaces/project-a/versions"},
		{name: "platform role: namespace route", method: http.MethodGet, path: "/apis/gcp.managed.openshift.io/v1/namespaces/project-a/platformroles"},
		{name: "extra path segment", method: http.MethodGet, path: "/apis/gcp.managed.openshift.io/v1/versions/4.22.1/status/extra"},
		{name: "platform role: extra path segment", method: http.MethodGet, path: "/apis/gcp.managed.openshift.io/v1/platformroles/catalog-reader/status/extra"},
		{name: "non-canonical path", method: http.MethodGet, path: "/apis/gcp.managed.openshift.io/v1//versions"},
		{name: "platform role: non-canonical path", method: http.MethodGet, path: "/apis/gcp.managed.openshift.io/v1//platformroles"},
		{name: "traversal path", method: http.MethodGet, path: "/apis/gcp.managed.openshift.io/v1/versions/../clusters"},
		{name: "platform role: traversal path", method: http.MethodGet, path: "/apis/gcp.managed.openshift.io/v1/platformroles/../clusters"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := httptest.NewRequestWithContext(context.Background(), tt.method, tt.path, nil)
			if got := IsAuthorizationExemptRequest(request, resources); got != tt.allow {
				t.Fatalf("IsAuthorizationExemptRequest() = %v, want %v", got, tt.allow)
			}
		})
	}
}
