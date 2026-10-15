package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/go-logr/logr"

	privatev1 "github.com/openshift-online/gecko/orlop/apis/private/test/v1"
	privatev2 "github.com/openshift-online/gecko/orlop/apis/private/test/v2"
	"github.com/openshift-online/gecko/orlop/pkg/apiserver/constants"
	"github.com/openshift-online/gecko/orlop/pkg/apiserver/storage/memory"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestResourceHandlerDeleteAuthorizesServingVersion(t *testing.T) {
	scheme := testScheme()
	store := memory.NewMemoryStore("objects", scheme, gvkV1)
	handler := NewResourceHandler(store, nil, gvkV2, "objects", scheme, logr.Discard())
	handler.SetStorageGVK(gvkV1)

	stored := &privatev1.Object{}
	stored.SetGroupVersionKind(gvkV1)
	stored.SetNamespace("default")
	stored.SetName("example")
	stored.Spec.PublicField = "stored-value"
	if err := store.Create(context.Background(), stored); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/apis/test.orlop.gcp.managed.openshift.io/v2/namespaces/default/objects/example", nil)
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add(constants.URLParamNamespace, "default")
	routeContext.URLParams.Add(constants.URLParamName, "example")
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeContext)

	var authorizedObject client.Object
	ctx = WithObjectAuthorizer(ctx, func(_ context.Context, object client.Object) (bool, error) {
		authorizedObject = object
		return false, nil
	})

	response := httptest.NewRecorder()
	handler.Delete(response, req.WithContext(ctx))
	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d: %s", response.Code, http.StatusForbidden, response.Body.String())
	}
	if authorizedObject == nil {
		t.Fatal("object authorizer was not called")
	}
	if got := authorizedObject.GetObjectKind().GroupVersionKind(); got != gvkV2 {
		t.Fatalf("authorized GVK = %s, want %s", got, gvkV2)
	}
	if _, ok := authorizedObject.(*privatev2.Object); !ok {
		t.Fatalf("authorized object type = %T, want *v2.Object", authorizedObject)
	}
}
