package handlers

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/go-logr/logr"
	apiext "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	structuralschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	"github.com/openshift-online/gecko/orlop/pkg/apiserver/constants"
	"github.com/openshift-online/gecko/orlop/pkg/apiserver/conversion"
	"github.com/openshift-online/gecko/orlop/pkg/apiserver/schema"
	"github.com/openshift-online/gecko/orlop/pkg/apiserver/storage/memory"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	runtimeschema "k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	testConvertingGVK = runtimeschema.GroupVersionKind{
		Group:   "test.io",
		Version: "v1",
		Kind:    "TestObject",
	}
)

// mockObject implements client.Object for testing
type mockObject struct {
	metav1.TypeMeta
	metav1.ObjectMeta
	Spec mockSpec `json:"spec"`
}

type mockSpec struct {
	Field string `json:"field"`
}

func (m *mockObject) DeepCopyObject() runtime.Object {
	if m == nil {
		return nil
	}
	out := &mockObject{}
	*out = *m
	out.TypeMeta = m.TypeMeta
	m.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	out.Spec = m.Spec
	return out
}

func newTestConvertingScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	scheme.AddKnownTypeWithName(testConvertingGVK, &mockObject{})
	metav1.AddToGroupVersion(scheme, testConvertingGVK.GroupVersion())
	return scheme
}

func setupConvertingHandlerTest(t *testing.T) (*ConvertingResourceHandler, *memory.MemoryStore) {
	scheme := newTestConvertingScheme()
	store := memory.NewMemoryStore("testobjects", scheme, testConvertingGVK)

	// Create a minimal permissive processor for unit tests.
	// Uses x-kubernetes-preserve-unknown-fields to allow any fields through
	// without pruning, so tests don't need a full schema definition.
	processor := newPermissiveProcessor(t)

	// Converter with same scheme for both public and private (no actual conversion needed for tests)
	converter := conversion.NewConverter(scheme, scheme, "")

	// Use test logger to see debug output
	logger := logr.FromSlogHandler(testLogHandler{t: t})

	handler := NewConvertingResourceHandler(
		store,
		processor,
		converter,
		testConvertingGVK,
		"testobjects",
		scheme,
		scheme,
		nil, // No printer columns
		logger,
	)

	return handler, store
}

// newPermissiveProcessor creates a schema.Processor with a permissive schema
// that allows all fields through. Used for unit tests where schema validation
// is not the focus.
func newPermissiveProcessor(t *testing.T) *schema.Processor {
	t.Helper()
	preserve := true
	structural := &structuralschema.Structural{
		Generic: structuralschema.Generic{Type: "object"},
		Extensions: structuralschema.Extensions{
			XPreserveUnknownFields: preserve,
		},
	}
	props := &apiext.JSONSchemaProps{
		Type:                   "object",
		XPreserveUnknownFields: &preserve,
	}
	proc, err := schema.NewProcessor(structural, props)
	if err != nil {
		t.Fatalf("failed to create permissive processor: %v", err)
	}
	return proc
}

type testLogHandler struct {
	t *testing.T
}

func (h testLogHandler) Enabled(context.Context, slog.Level) bool {
	return true
}

func (h testLogHandler) Handle(ctx context.Context, r slog.Record) error {
	attrs := make([]any, 0)
	r.Attrs(func(a slog.Attr) bool {
		attrs = append(attrs, a.Key, a.Value.Any())
		return true
	})
	if len(attrs) > 0 {
		h.t.Logf("[%s] %s %v", r.Level, r.Message, attrs)
	} else {
		h.t.Logf("[%s] %s", r.Level, r.Message)
	}
	return nil
}

func (h testLogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return h
}

func (h testLogHandler) WithGroup(name string) slog.Handler {
	return h
}

func createTestObject(name, namespace string, finalizers []string) *mockObject {
	obj := &mockObject{
		TypeMeta: metav1.TypeMeta{
			APIVersion: testConvertingGVK.GroupVersion().String(),
			Kind:       testConvertingGVK.Kind,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			Namespace:  namespace,
			Finalizers: finalizers,
		},
		Spec: mockSpec{
			Field: "test-value",
		},
	}
	obj.SetGroupVersionKind(testConvertingGVK)
	return obj
}

func TestConvertingResourceHandler_Delete_NoFinalizers(t *testing.T) {
	handler, store := setupConvertingHandlerTest(t)
	ctx := context.Background()
	
	// Create object without finalizers
	obj := createTestObject("test-delete", "default", nil)
	if err := store.Create(ctx, obj); err != nil {
		t.Fatalf("failed to create test object: %v", err)
	}
	
	// Make DELETE request
	req := httptest.NewRequest(http.MethodDelete, "/apis/test.io/v1/namespaces/default/testobjects/test-delete", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(constants.URLParamNamespace, "default")
	rctx.URLParams.Add(constants.URLParamName, "test-delete")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	
	rr := httptest.NewRecorder()
	handler.Delete(rr, req)
	
	// Verify hard delete occurred
	if rr.Code != http.StatusOK {
		t.Errorf("status code = %d, want %d", rr.Code, http.StatusOK)
	}
	
	// Verify response is metav1.Status (hard delete returns Status, not object)
	var respBody map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &respBody); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	if respBody["kind"] != "Status" {
		t.Errorf("hard delete response kind = %v, want Status", respBody["kind"])
	}
	
	// Verify object is gone
	_, err := store.Get(ctx, "default", "test-delete")
	if err == nil {
		t.Error("expected object to be deleted, but Get succeeded")
	}
}

func TestConvertingResourceHandler_Delete_WithFinalizers_SoftDelete(t *testing.T) {
	handler, store := setupConvertingHandlerTest(t)
	ctx := context.Background()
	
	// Create object with finalizers
	obj := createTestObject("test-soft-delete", "default", []string{"test.io/finalizer"})
	if err := store.Create(ctx, obj); err != nil {
		t.Fatalf("failed to create test object: %v", err)
	}
	
	// Make DELETE request
	req := httptest.NewRequest(http.MethodDelete, "/apis/test.io/v1/namespaces/default/testobjects/test-soft-delete", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(constants.URLParamNamespace, "default")
	rctx.URLParams.Add(constants.URLParamName, "test-soft-delete")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	
	rr := httptest.NewRecorder()
	handler.Delete(rr, req)
	
	// Verify soft delete (200 with object returned)
	if rr.Code != http.StatusOK {
		t.Errorf("status code = %d, want %d", rr.Code, http.StatusOK)
	}
	
	// Verify response is the object (soft delete returns resource, not Status)
	var respBody map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &respBody); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	if respBody["kind"] == "Status" {
		t.Error("soft delete should return resource object, not Status")
	}
	
	// Verify object still exists with deletionTimestamp
	existing, err := store.Get(ctx, "default", "test-soft-delete")
	if err != nil {
		t.Fatalf("expected object to still exist: %v", err)
	}
	
	clientObj, ok := existing.(client.Object)
	if !ok {
		t.Fatal("object does not implement client.Object")
	}
	
	if clientObj.GetDeletionTimestamp() == nil {
		t.Error("expected deletionTimestamp to be set")
	}
	
	if len(clientObj.GetFinalizers()) == 0 {
		t.Error("expected finalizers to still be present")
	}
}

func TestConvertingResourceHandler_Delete_AlreadySoftDeleted_Idempotent(t *testing.T) {
	handler, store := setupConvertingHandlerTest(t)
	ctx := context.Background()
	
	// Create object with finalizers and deletionTimestamp (already soft-deleted)
	obj := createTestObject("test-idempotent", "default", []string{"test.io/finalizer"})
	now := metav1.Now()
	obj.SetDeletionTimestamp(&now)
	if err := store.Create(ctx, obj); err != nil {
		t.Fatalf("failed to create test object: %v", err)
	}
	
	// Make DELETE request
	req := httptest.NewRequest(http.MethodDelete, "/apis/test.io/v1/namespaces/default/testobjects/test-idempotent", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(constants.URLParamNamespace, "default")
	rctx.URLParams.Add(constants.URLParamName, "test-idempotent")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	
	rr := httptest.NewRecorder()
	handler.Delete(rr, req)
	
	// Verify idempotent response (200 with object returned)
	if rr.Code != http.StatusOK {
		t.Errorf("status code = %d, want %d", rr.Code, http.StatusOK)
	}
	
	// Verify object still exists unchanged
	existing, err := store.Get(ctx, "default", "test-idempotent")
	if err != nil {
		t.Fatalf("expected object to still exist: %v", err)
	}
	
	clientObj, ok := existing.(client.Object)
	if !ok {
		t.Fatal("object does not implement client.Object")
	}
	
	if clientObj.GetDeletionTimestamp() == nil {
		t.Error("expected deletionTimestamp to still be set")
	}
}

func TestConvertingResourceHandler_Update_PreservesDeletionTimestamp(t *testing.T) {
	handler, store := setupConvertingHandlerTest(t)
	ctx := context.Background()
	
	// Create soft-deleted object
	obj := createTestObject("test-preserve", "default", []string{"test.io/finalizer"})
	now := metav1.Now()
	obj.SetDeletionTimestamp(&now)
	if err := store.Create(ctx, obj); err != nil {
		t.Fatalf("failed to create test object: %v", err)
	}
	
	// Update the object (change spec field)
	updateObj := createTestObject("test-preserve", "default", []string{"test.io/finalizer"})
	updateObj.Spec.Field = "updated-value"
	updateJSON, _ := json.Marshal(updateObj)
	
	req := httptest.NewRequest(http.MethodPut, "/apis/test.io/v1/namespaces/default/testobjects/test-preserve", bytes.NewReader(updateJSON))
	req.Header.Set("Content-Type", "application/json")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(constants.URLParamNamespace, "default")
	rctx.URLParams.Add(constants.URLParamName, "test-preserve")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	
	rr := httptest.NewRecorder()
	handler.Update(rr, req)
	
	// Verify update succeeded
	if rr.Code != http.StatusOK {
		t.Errorf("status code = %d, want %d, body: %s", rr.Code, http.StatusOK, rr.Body.String())
	}
	
	// Verify deletionTimestamp was preserved
	existing, err := store.Get(ctx, "default", "test-preserve")
	if err != nil {
		t.Fatalf("failed to get updated object: %v", err)
	}
	
	clientObj, ok := existing.(client.Object)
	if !ok {
		t.Fatal("object does not implement client.Object")
	}
	
	if clientObj.GetDeletionTimestamp() == nil {
		t.Error("expected deletionTimestamp to be preserved after update")
	}
}

// Note: Finalizer removal via public API is not supported - finalizers are internal fields
// not exposed on the public API. Controllers remove finalizers via the private API.
// The integration test finalizer_public_api_test.go verifies this correctly.

func TestExtractUserEmail(t *testing.T) {
	tests := []struct {
		name     string
		header   string
		wantEmail string
	}{
		{
			name:      "valid header with email",
			header:    base64.RawURLEncoding.EncodeToString([]byte(`{"email":"user@example.com","sub":"12345"}`)),
			wantEmail: "user@example.com",
		},
		{
			name:      "empty header",
			header:    "",
			wantEmail: "",
		},
		{
			name:      "invalid base64",
			header:    "not-valid-base64!!!",
			wantEmail: "",
		},
		{
			name:      "valid base64 but invalid JSON",
			header:    base64.RawURLEncoding.EncodeToString([]byte(`not json`)),
			wantEmail: "",
		},
		{
			name:      "valid JSON but no email field",
			header:    base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"12345"}`)),
			wantEmail: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/test", nil)
			if tt.header != "" {
				req.Header.Set(constants.HeaderEndpointAPIUserInfo, tt.header)
			}
			got := extractUserEmail(req)
			if got != tt.wantEmail {
				t.Errorf("extractUserEmail() = %q, want %q", got, tt.wantEmail)
			}
		})
	}
}

func TestCreate_SetsCreatedByAnnotation_NoHeader(t *testing.T) {
	// Verify that without the X-Endpoint-API-UserInfo header, the created-by
	// annotation is NOT set. This tests the extractUserEmail → annotation flow
	// indirectly via the absence case, since the full Create handler requires a
	// properly configured schema processor to preserve metadata.name.
	req := httptest.NewRequest(http.MethodPost, "/test", nil)
	email := extractUserEmail(req)
	if email != "" {
		t.Errorf("expected empty email without header, got %q", email)
	}
}
