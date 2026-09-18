package apiserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/go-logr/logr"

	"github.com/openshift-online/gecko/orlop/pkg/apiserver/handlers"
	pkgschema "github.com/openshift-online/gecko/orlop/pkg/apiserver/schema"
	"github.com/openshift-online/gecko/orlop/pkg/apiserver/storage/memory"

	apiext "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	extschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	runtimeschema "k8s.io/apimachinery/pkg/runtime/schema"

	"sigs.k8s.io/yaml"
)

func TestParentFilterMiddleware(t *testing.T) {
	var captured *handlers.ParentFilter

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = handlers.ParentFilterFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	r := chi.NewRouter()
	r.Route("/parents/{parentID}/children", func(r chi.Router) {
		r.Use(parentFilterMiddleware("spec.clusterID", "parentID"))
		r.Get("/", inner)
	})

	req := httptest.NewRequest(http.MethodGet, "/parents/c1/children", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if captured == nil {
		t.Fatal("expected parent filter in context, got nil")
	}
	if captured.IDField != "spec.clusterID" {
		t.Fatalf("expected IDField spec.clusterID, got %s", captured.IDField)
	}
	if captured.ID != "c1" {
		t.Fatalf("expected ID c1, got %s", captured.ID)
	}
}

func TestParentFilterMiddleware_DifferentParams(t *testing.T) {
	var captured *handlers.ParentFilter

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = handlers.ParentFilterFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	r := chi.NewRouter()
	r.Route("/clusters/{clusterID}/nodepools", func(r chi.Router) {
		r.Use(parentFilterMiddleware("spec.clusterRef", "clusterID"))
		r.Get("/", inner)
	})

	req := httptest.NewRequest(http.MethodGet, "/clusters/my-cluster/nodepools", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if captured == nil {
		t.Fatal("expected parent filter in context")
	}
	if captured.IDField != "spec.clusterRef" || captured.ID != "my-cluster" {
		t.Fatalf("expected {spec.clusterRef my-cluster}, got {%s %s}", captured.IDField, captured.ID)
	}
}

const childSchemaYAML = `
type: object
properties:
  apiVersion:
    type: string
  kind:
    type: string
  metadata:
    type: object
  spec:
    type: object
    properties:
      objectID:
        type: string
      publicField:
        type: string
  status:
    type: object
`

func newTestProcessor(t *testing.T) *pkgschema.Processor {
	t.Helper()

	var propsV1 apiextv1.JSONSchemaProps
	if err := yaml.Unmarshal([]byte(childSchemaYAML), &propsV1); err != nil {
		t.Fatalf("failed to unmarshal YAML: %v", err)
	}

	var props apiext.JSONSchemaProps
	if err := apiextv1.Convert_v1_JSONSchemaProps_To_apiextensions_JSONSchemaProps(&propsV1, &props, nil); err != nil {
		t.Fatalf("failed to convert props: %v", err)
	}

	structural, err := extschema.NewStructural(&props)
	if err != nil {
		t.Fatalf("failed to create structural schema: %v", err)
	}

	processor, err := pkgschema.NewProcessor(structural, &props)
	if err != nil {
		t.Fatalf("failed to create processor: %v", err)
	}

	return processor
}

func newNestedRouteTestRouter(t *testing.T) (chi.Router, *memory.MemoryStore) {
	t.Helper()

	gvk := runtimeschema.GroupVersionKind{
		Group: "test.example.com", Version: "v1", Kind: "Child",
	}

	scheme := runtime.NewScheme()
	scheme.AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(
		runtimeschema.GroupVersionKind{Group: "test.example.com", Version: "v1", Kind: "ChildList"},
		&unstructured.UnstructuredList{},
	)

	store := memory.NewMemoryStore("children", scheme, gvk)
	processor := newTestProcessor(t)

	handler := handlers.NewResourceHandler(store, processor, gvk, "children", scheme, logr.Discard())

	r := chi.NewRouter()
	r.Route("/namespaces/{namespace}/parents/{parentID}/children", func(r chi.Router) {
		r.Use(parentFilterMiddleware("spec.objectID", "parentID"))
		r.Post("/", handler.Create)
		r.Get("/", handler.List)
		r.Get("/{name}", handler.Get)
		r.Delete("/{name}", handler.Delete)
	})

	return r, store
}

func TestNestedRoute_CreateMatchingParent(t *testing.T) {
	router, _ := newNestedRouteTestRouter(t)

	payload := map[string]interface{}{
		"apiVersion": "test.example.com/v1",
		"kind":       "Child",
		"metadata":   map[string]interface{}{"name": "child1"},
		"spec":       map[string]interface{}{"objectID": "p1", "publicField": "val"},
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/namespaces/default/parents/p1/children", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestNestedRoute_CreateMismatchedParent(t *testing.T) {
	router, _ := newNestedRouteTestRouter(t)

	payload := map[string]interface{}{
		"apiVersion": "test.example.com/v1",
		"kind":       "Child",
		"metadata":   map[string]interface{}{"name": "child1"},
		"spec":       map[string]interface{}{"objectID": "p2", "publicField": "val"},
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/namespaces/default/parents/p1/children", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestNestedRoute_ListFiltersbyParent(t *testing.T) {
	router, store := newNestedRouteTestRouter(t)
	ctx := context.Background()

	for _, item := range []struct {
		name     string
		objectID string
	}{
		{"child-p1a", "p1"},
		{"child-p1b", "p1"},
		{"child-p2", "p2"},
	} {
		obj := &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "test.example.com/v1",
				"kind":       "Child",
				"metadata":   map[string]interface{}{"name": item.name, "namespace": "default"},
				"spec":       map[string]interface{}{"objectID": item.objectID},
			},
		}
		if err := store.Create(ctx, obj); err != nil {
			t.Fatalf("store.Create(%s) failed: %v", item.name, err)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/namespaces/default/parents/p1/children", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var result map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	items, ok := result["items"].([]interface{})
	if !ok {
		t.Fatalf("expected items array, got %T", result["items"])
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items for parent p1, got %d", len(items))
	}
}

func TestNestedRoute_GetWrongParent(t *testing.T) {
	router, store := newNestedRouteTestRouter(t)
	ctx := context.Background()

	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "test.example.com/v1",
			"kind":       "Child",
			"metadata":   map[string]interface{}{"name": "child1", "namespace": "default"},
			"spec":       map[string]interface{}{"objectID": "p2"},
		},
	}
	if err := store.Create(ctx, obj); err != nil {
		t.Fatalf("store.Create failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/namespaces/default/parents/p1/children/child1", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for wrong parent, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestNestedRoute_GetCorrectParent(t *testing.T) {
	router, store := newNestedRouteTestRouter(t)
	ctx := context.Background()

	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "test.example.com/v1",
			"kind":       "Child",
			"metadata":   map[string]interface{}{"name": "child1", "namespace": "default"},
			"spec":       map[string]interface{}{"objectID": "p1"},
		},
	}
	if err := store.Create(ctx, obj); err != nil {
		t.Fatalf("store.Create failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/namespaces/default/parents/p1/children/child1", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestNestedRoute_DeleteWrongParent(t *testing.T) {
	router, store := newNestedRouteTestRouter(t)
	ctx := context.Background()

	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "test.example.com/v1",
			"kind":       "Child",
			"metadata":   map[string]interface{}{"name": "child1", "namespace": "default"},
			"spec":       map[string]interface{}{"objectID": "p2"},
		},
	}
	if err := store.Create(ctx, obj); err != nil {
		t.Fatalf("store.Create failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/namespaces/default/parents/p1/children/child1", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for wrong parent, got %d: %s", rr.Code, rr.Body.String())
	}
}
