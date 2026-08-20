package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/go-logr/logr"
	"github.com/openshift-online/gecko/orlop/pkg/apiserver/constants"
	"github.com/openshift-online/gecko/orlop/pkg/apiserver/conversion"
	"github.com/openshift-online/gecko/orlop/pkg/apiserver/storage/memory"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	runtimeschema "k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	objectGVK = runtimeschema.GroupVersionKind{
		Group:   "test.io",
		Version: "v1",
		Kind:    "Object",
	}
)

// object matches public Cluster structure: has spec and status
type object struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              objectSpec   `json:"spec,omitempty"`
	Status            objectStatus `json:"status,omitempty"`
}

type objectSpec struct {
	Field string `json:"field,omitempty"`
	Value int    `json:"value,omitempty"`
}

type objectStatus struct {
	Conditions          []metav1.Condition      `json:"conditions,omitempty"`
	PlacementResult     *statusPlacementResult  `json:"placementResult,omitempty"`
	HostedClusterResult *statusHostedClusterRes `json:"hostedClusterResult,omitempty"`
}

type statusPlacementResult struct {
	ManagementClusterName string `json:"managementClusterName,omitempty"`
}

type statusHostedClusterRes struct {
	APIEndpoint string `json:"apiEndpoint,omitempty"`
	Version     string `json:"version,omitempty"`
}

func (m *object) DeepCopyObject() runtime.Object {
	if m == nil {
		return nil
	}
	out := &object{}
	*out = *m
	out.TypeMeta = m.TypeMeta
	m.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	out.Spec = m.Spec
	// Deep copy status
	if m.Status.Conditions != nil {
		out.Status.Conditions = make([]metav1.Condition, len(m.Status.Conditions))
		for i := range m.Status.Conditions {
			m.Status.Conditions[i].DeepCopyInto(&out.Status.Conditions[i])
		}
	}
	if m.Status.PlacementResult != nil {
		out.Status.PlacementResult = &statusPlacementResult{
			ManagementClusterName: m.Status.PlacementResult.ManagementClusterName,
		}
	}
	if m.Status.HostedClusterResult != nil {
		out.Status.HostedClusterResult = &statusHostedClusterRes{
			APIEndpoint: m.Status.HostedClusterResult.APIEndpoint,
			Version:     m.Status.HostedClusterResult.Version,
		}
	}
	return out
}

func newObjectScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	scheme.AddKnownTypeWithName(objectGVK, &object{})
	metav1.AddToGroupVersion(scheme, objectGVK.GroupVersion())
	return scheme
}

func setupStatusStrippingTest(t *testing.T) (*ConvertingResourceHandler, *memory.MemoryStore) {
	scheme := newObjectScheme()
	store := memory.NewMemoryStore("objects", scheme, objectGVK)
	processor := newPermissiveProcessor(t)
	converter := conversion.NewConverter(scheme, scheme, "")
	logger := logr.FromSlogHandler(testLogHandler{t: t})

	handler := NewConvertingResourceHandler(
		store,
		processor,
		converter,
		objectGVK,
		"objects",
		scheme,
		scheme,
		nil, // No printer columns for this test
		logger,
	)

	return handler, store
}

// mustMarshalJSON marshals v to JSON, failing the test on error.
func mustMarshalJSON(t *testing.T, v interface{}) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("failed to marshal test fixture: %v", err)
	}
	return b
}

// Test: Public API Create cannot set status (all status fields)
func TestStatusStripping_Create_StatusNotPersisted(t *testing.T) {
	handler, store := setupStatusStrippingTest(t)
	ctx := context.Background()

	// Create request body with FULL status subtree
	createBody := map[string]interface{}{
		"apiVersion": objectGVK.GroupVersion().String(),
		"kind":       objectGVK.Kind,
		"metadata": map[string]interface{}{
			"name":      "test-create",
			"namespace": "default",
		},
		"spec": map[string]interface{}{
			"field": "user-value",
			"value": 42,
		},
		"status": map[string]interface{}{
			"conditions": []interface{}{
				map[string]interface{}{
					"type":               "Ready",
					"status":             "True",
					"reason":             "EvilReason",
					"message":            "Client-injected condition",
					"lastTransitionTime": "2024-01-01T00:00:00Z",
				},
			},
			"placementResult": map[string]interface{}{
				"managementClusterName": "evil-cluster",
			},
			"hostedClusterResult": map[string]interface{}{
				"apiEndpoint": "https://evil.example.com",
				"version":     "4.99.0",
			},
		},
	}

	bodyJSON := mustMarshalJSON(t, createBody)
	req := httptest.NewRequest(http.MethodPost, "/apis/test.io/v1/namespaces/default/objects", bytes.NewReader(bodyJSON))
	req.Header.Set("Content-Type", "application/json")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(constants.URLParamNamespace, "default")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rr := httptest.NewRecorder()
	handler.Create(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status code = %d, want %d, body: %s", rr.Code, http.StatusCreated, rr.Body.String())
	}

	// Verify status NOT persisted
	stored, err := store.Get(ctx, "default", "test-create")
	if err != nil {
		t.Fatalf("failed to get stored object: %v", err)
	}

	obj, ok := stored.(*object)
	if !ok {
		t.Fatal("stored object wrong type")
	}

	// Verify spec WAS persisted
	if obj.Spec.Field != "user-value" || obj.Spec.Value != 42 {
		t.Errorf("spec not persisted correctly: got field=%q value=%d", obj.Spec.Field, obj.Spec.Value)
	}

	// Verify ALL status fields empty/nil
	if len(obj.Status.Conditions) != 0 {
		t.Errorf("conditions should be empty, got %d conditions", len(obj.Status.Conditions))
	}
	if obj.Status.PlacementResult != nil {
		t.Errorf("placementResult should be nil, got %+v", obj.Status.PlacementResult)
	}
	if obj.Status.HostedClusterResult != nil {
		t.Errorf("hostedClusterResult should be nil, got %+v", obj.Status.HostedClusterResult)
	}
}

// Test: Public API Update cannot overwrite controller-set status
func TestStatusStripping_Update_ExistingStatusPreserved(t *testing.T) {
	handler, store := setupStatusStrippingTest(t)
	ctx := context.Background()

	// Create object with controller-set status
	existing := &object{
		TypeMeta: metav1.TypeMeta{
			APIVersion: objectGVK.GroupVersion().String(),
			Kind:       objectGVK.Kind,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-update",
			Namespace: "default",
		},
		Spec: objectSpec{
			Field: "original-value",
			Value: 1,
		},
		Status: objectStatus{
			Conditions: []metav1.Condition{
				{
					Type:               "Ready",
					Status:             metav1.ConditionTrue,
					Reason:             "ControllerReason",
					Message:            "Controller-set condition",
					LastTransitionTime: metav1.Now(),
				},
			},
			PlacementResult: &statusPlacementResult{
				ManagementClusterName: "controller-cluster",
			},
			HostedClusterResult: &statusHostedClusterRes{
				APIEndpoint: "https://controller.example.com",
				Version:     "4.14.0",
			},
		},
	}
	existing.SetGroupVersionKind(objectGVK)
	if err := store.Create(ctx, existing); err != nil {
		t.Fatalf("failed to create existing object: %v", err)
	}

	// Client PUTs with different status values
	updateBody := map[string]interface{}{
		"apiVersion": objectGVK.GroupVersion().String(),
		"kind":       objectGVK.Kind,
		"metadata": map[string]interface{}{
			"name":      "test-update",
			"namespace": "default",
		},
		"spec": map[string]interface{}{
			"field": "updated-value",
			"value": 2,
		},
		"status": map[string]interface{}{
			"conditions": []interface{}{
				map[string]interface{}{
					"type":    "Ready",
					"status":  "False",
					"reason":  "ClientReason",
					"message": "Client-injected condition",
				},
			},
			"placementResult": map[string]interface{}{
				"managementClusterName": "evil-cluster",
			},
			"hostedClusterResult": map[string]interface{}{
				"apiEndpoint": "https://evil.example.com",
				"version":     "4.99.0",
			},
		},
	}

	bodyJSON := mustMarshalJSON(t, updateBody)
	req := httptest.NewRequest(http.MethodPut, "/apis/test.io/v1/namespaces/default/objects/test-update", bytes.NewReader(bodyJSON))
	req.Header.Set("Content-Type", "application/json")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(constants.URLParamNamespace, "default")
	rctx.URLParams.Add(constants.URLParamName, "test-update")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rr := httptest.NewRecorder()
	handler.Update(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d, body: %s", rr.Code, http.StatusOK, rr.Body.String())
	}

	// Verify spec updated
	stored, err := store.Get(ctx, "default", "test-update")
	if err != nil {
		t.Fatalf("failed to get stored object: %v", err)
	}

	obj, ok := stored.(*object)
	if !ok {
		t.Fatal("stored object wrong type")
	}

	if obj.Spec.Field != "updated-value" || obj.Spec.Value != 2 {
		t.Errorf("spec not updated: got field=%q value=%d", obj.Spec.Field, obj.Spec.Value)
	}

	// Verify controller-set status preserved (NOT client-supplied values)
	if len(obj.Status.Conditions) != 1 {
		t.Fatalf("expected 1 condition, got %d", len(obj.Status.Conditions))
	}
	if obj.Status.Conditions[0].Reason != "ControllerReason" {
		t.Errorf("condition.reason = %q, want ControllerReason (controller-set, not client-supplied)", obj.Status.Conditions[0].Reason)
	}
	if obj.Status.PlacementResult == nil || obj.Status.PlacementResult.ManagementClusterName != "controller-cluster" {
		t.Errorf("placementResult overwritten, want controller-cluster")
	}
	if obj.Status.HostedClusterResult == nil || obj.Status.HostedClusterResult.APIEndpoint != "https://controller.example.com" {
		t.Errorf("hostedClusterResult overwritten, want controller-set values")
	}
}

// Test: Public API Patch cannot inject/modify status
func TestStatusStripping_Patch_ExistingStatusPreserved(t *testing.T) {
	handler, store := setupStatusStrippingTest(t)
	ctx := context.Background()

	// Create object with controller-set status
	existing := &object{
		TypeMeta: metav1.TypeMeta{
			APIVersion: objectGVK.GroupVersion().String(),
			Kind:       objectGVK.Kind,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-patch",
			Namespace: "default",
		},
		Spec: objectSpec{
			Field: "original-value",
			Value: 1,
		},
		Status: objectStatus{
			Conditions: []metav1.Condition{
				{
					Type:               "Ready",
					Status:             metav1.ConditionTrue,
					Reason:             "ControllerReason",
					Message:            "Controller-set",
					LastTransitionTime: metav1.Now(),
				},
			},
			PlacementResult: &statusPlacementResult{
				ManagementClusterName: "controller-cluster",
			},
		},
	}
	existing.SetGroupVersionKind(objectGVK)
	if err := store.Create(ctx, existing); err != nil {
		t.Fatalf("failed to create existing object: %v", err)
	}

	// Patch including status
	patchBody := map[string]interface{}{
		"spec": map[string]interface{}{
			"value": 99,
		},
		"status": map[string]interface{}{
			"conditions": []interface{}{
				map[string]interface{}{
					"type":   "Ready",
					"status": "False",
					"reason": "ClientReason",
				},
			},
			"placementResult": map[string]interface{}{
				"managementClusterName": "evil-cluster",
			},
		},
	}

	patchJSON := mustMarshalJSON(t, patchBody)
	req := httptest.NewRequest(http.MethodPatch, "/apis/test.io/v1/namespaces/default/objects/test-patch", bytes.NewReader(patchJSON))
	req.Header.Set("Content-Type", "application/merge-patch+json")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(constants.URLParamNamespace, "default")
	rctx.URLParams.Add(constants.URLParamName, "test-patch")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rr := httptest.NewRecorder()
	handler.Patch(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d, body: %s", rr.Code, http.StatusOK, rr.Body.String())
	}

	// Verify spec.value updated
	stored, err := store.Get(ctx, "default", "test-patch")
	if err != nil {
		t.Fatalf("failed to get stored object: %v", err)
	}

	obj, ok := stored.(*object)
	if !ok {
		t.Fatal("stored object wrong type")
	}

	if obj.Spec.Value != 99 {
		t.Errorf("spec.value = %d, want 99", obj.Spec.Value)
	}

	// Verify controller-set status retained
	if len(obj.Status.Conditions) != 1 || obj.Status.Conditions[0].Reason != "ControllerReason" {
		t.Errorf("status.conditions overwritten by patch")
	}
	if obj.Status.PlacementResult == nil || obj.Status.PlacementResult.ManagementClusterName != "controller-cluster" {
		t.Errorf("placementResult overwritten by patch")
	}
}

// Test: specChanged still works (generation incremented for spec changes)
func TestStatusStripping_Update_SpecChangedIncrementGeneration(t *testing.T) {
	handler, store := setupStatusStrippingTest(t)
	ctx := context.Background()

	// Create object
	existing := &object{
		TypeMeta: metav1.TypeMeta{
			APIVersion: objectGVK.GroupVersion().String(),
			Kind:       objectGVK.Kind,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-gen",
			Namespace:  "default",
			Generation: 1,
		},
		Spec: objectSpec{
			Field: "original",
			Value: 1,
		},
	}
	existing.SetGroupVersionKind(objectGVK)
	if err := store.Create(ctx, existing); err != nil {
		t.Fatalf("failed to create existing object: %v", err)
	}

	// Update with spec changes + status changes
	updateBody := map[string]interface{}{
		"apiVersion": objectGVK.GroupVersion().String(),
		"kind":       objectGVK.Kind,
		"metadata": map[string]interface{}{
			"name":      "test-gen",
			"namespace": "default",
		},
		"spec": map[string]interface{}{
			"field": "updated", // CHANGED
			"value": 1,
		},
		"status": map[string]interface{}{
			"conditions": []interface{}{
				map[string]interface{}{"type": "Ready", "status": "True"},
			},
		},
	}

	bodyJSON := mustMarshalJSON(t, updateBody)
	req := httptest.NewRequest(http.MethodPut, "/apis/test.io/v1/namespaces/default/objects/test-gen", bytes.NewReader(bodyJSON))
	req.Header.Set("Content-Type", "application/json")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(constants.URLParamNamespace, "default")
	rctx.URLParams.Add(constants.URLParamName, "test-gen")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rr := httptest.NewRecorder()
	handler.Update(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d, body: %s", rr.Code, http.StatusOK, rr.Body.String())
	}

	// Verify generation incremented (spec changed)
	stored, err := store.Get(ctx, "default", "test-gen")
	if err != nil {
		t.Fatalf("failed to get stored object: %v", err)
	}

	obj, ok := stored.(*object)
	if !ok {
		t.Fatal("stored object wrong type")
	}

	if obj.Generation != 2 {
		t.Errorf("generation = %d, want 2 (spec changed)", obj.Generation)
	}

	// Verify spec persisted, status stripped
	if obj.Spec.Field != "updated" {
		t.Errorf("spec.field = %q, want updated", obj.Spec.Field)
	}
	if len(obj.Status.Conditions) != 0 {
		t.Errorf("status.conditions should be empty")
	}
}

// Test: Body with no status field → no error (normal operation)
func TestStatusStripping_Create_NoStatusField(t *testing.T) {
	handler, _ := setupStatusStrippingTest(t)

	createBody := map[string]interface{}{
		"apiVersion": objectGVK.GroupVersion().String(),
		"kind":       objectGVK.Kind,
		"metadata": map[string]interface{}{
			"name":      "test-nostatus",
			"namespace": "default",
		},
		"spec": map[string]interface{}{
			"field": "value",
		},
		// No "status" key
	}

	bodyJSON := mustMarshalJSON(t, createBody)
	req := httptest.NewRequest(http.MethodPost, "/apis/test.io/v1/namespaces/default/objects", bytes.NewReader(bodyJSON))
	req.Header.Set("Content-Type", "application/json")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(constants.URLParamNamespace, "default")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rr := httptest.NewRecorder()
	handler.Create(rr, req)

	if rr.Code != http.StatusCreated {
		t.Errorf("status code = %d, want %d, body: %s", rr.Code, http.StatusCreated, rr.Body.String())
	}
}

// Test: Body with empty status → no error
func TestStatusStripping_Create_EmptyStatus(t *testing.T) {
	handler, _ := setupStatusStrippingTest(t)

	createBody := map[string]interface{}{
		"apiVersion": objectGVK.GroupVersion().String(),
		"kind":       objectGVK.Kind,
		"metadata": map[string]interface{}{
			"name":      "test-emptystatus",
			"namespace": "default",
		},
		"spec": map[string]interface{}{
			"field": "value",
		},
		"status": map[string]interface{}{}, // Empty
	}

	bodyJSON := mustMarshalJSON(t, createBody)
	req := httptest.NewRequest(http.MethodPost, "/apis/test.io/v1/namespaces/default/objects", bytes.NewReader(bodyJSON))
	req.Header.Set("Content-Type", "application/json")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(constants.URLParamNamespace, "default")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rr := httptest.NewRecorder()
	handler.Create(rr, req)

	if rr.Code != http.StatusCreated {
		t.Errorf("status code = %d, want %d, body: %s", rr.Code, http.StatusCreated, rr.Body.String())
	}
}

// Test: Body with null status → no error
func TestStatusStripping_Create_NullStatus(t *testing.T) {
	handler, _ := setupStatusStrippingTest(t)

	bodyJSON := []byte(`{
		"apiVersion": "test.io/v1",
		"kind": "Object",
		"metadata": {
			"name": "test-nullstatus",
			"namespace": "default"
		},
		"spec": {
			"field": "value"
		},
		"status": null
	}`)

	req := httptest.NewRequest(http.MethodPost, "/apis/test.io/v1/namespaces/default/objects", bytes.NewReader(bodyJSON))
	req.Header.Set("Content-Type", "application/json")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(constants.URLParamNamespace, "default")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rr := httptest.NewRecorder()
	handler.Create(rr, req)

	if rr.Code != http.StatusCreated {
		t.Errorf("status code = %d, want %d, body: %s", rr.Code, http.StatusCreated, rr.Body.String())
	}
}

// Test: Create strips case-variant status keys (Status, STATUS, etc.)
func TestStatusStripping_Create_CaseVariants(t *testing.T) {
	handler, store := setupStatusStrippingTest(t)
	ctx := context.Background()

	testCases := []struct {
		name      string
		statusKey string
	}{
		{"uppercase Status", "Status"},
		{"all caps STATUS", "STATUS"},
		{"mixed StAtUs", "StAtUs"},
	}

	for _, tc := range testCases {
		t.Run(tc.statusKey, func(t *testing.T) {
			bodyJSON := mustMarshalJSON(t, map[string]interface{}{
				"apiVersion": objectGVK.GroupVersion().String(),
				"kind":       objectGVK.Kind,
				"metadata": map[string]interface{}{
					"name":      "test-case-" + tc.statusKey,
					"namespace": "default",
				},
				"spec": map[string]interface{}{
					"field": "value",
				},
				tc.statusKey: map[string]interface{}{
					"conditions": []interface{}{
						map[string]interface{}{"type": "Ready", "status": "True"},
					},
				},
			})

			req := httptest.NewRequest(http.MethodPost, "/apis/test.io/v1/namespaces/default/objects", bytes.NewReader(bodyJSON))
			req.Header.Set("Content-Type", "application/json")
			rctx := chi.NewRouteContext()
			rctx.URLParams.Add(constants.URLParamNamespace, "default")
			req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

			rr := httptest.NewRecorder()
			handler.Create(rr, req)

			if rr.Code != http.StatusCreated {
				t.Fatalf("status code = %d, want %d, body: %s", rr.Code, http.StatusCreated, rr.Body.String())
			}

			// Verify case-variant status was stripped
			stored, err := store.Get(ctx, "default", "test-case-"+tc.statusKey)
			if err != nil {
				t.Fatalf("failed to get stored object: %v", err)
			}

			obj, ok := stored.(*object)
			if !ok {
				t.Fatal("stored object wrong type")
			}

			if len(obj.Status.Conditions) != 0 {
				t.Errorf("case-variant %q status was not stripped, got %d conditions", tc.statusKey, len(obj.Status.Conditions))
			}
		})
	}
}

// Test: Update strips case-variant status keys
func TestStatusStripping_Update_CaseVariants(t *testing.T) {
	handler, store := setupStatusStrippingTest(t)
	ctx := context.Background()

	// Create object with controller-set status
	existing := &object{
		TypeMeta: metav1.TypeMeta{
			APIVersion: objectGVK.GroupVersion().String(),
			Kind:       objectGVK.Kind,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-update-case",
			Namespace: "default",
		},
		Spec: objectSpec{Field: "original"},
		Status: objectStatus{
			Conditions: []metav1.Condition{
				{Type: "Ready", Status: metav1.ConditionTrue, Reason: "ControllerSet"},
			},
		},
	}
	existing.SetGroupVersionKind(objectGVK)
	if err := store.Create(ctx, existing); err != nil {
		t.Fatalf("failed to create: %v", err)
	}

	bodyJSON := mustMarshalJSON(t, map[string]interface{}{
		"apiVersion": objectGVK.GroupVersion().String(),
		"kind":       objectGVK.Kind,
		"metadata": map[string]interface{}{
			"name":      "test-update-case",
			"namespace": "default",
		},
		"spec": map[string]interface{}{
			"field": "updated",
		},
		"STATUS": map[string]interface{}{
			"conditions": []interface{}{
				map[string]interface{}{"type": "Failed", "status": "True"},
			},
		},
	})

	req := httptest.NewRequest(http.MethodPut, "/apis/test.io/v1/namespaces/default/objects/test-update-case", bytes.NewReader(bodyJSON))
	req.Header.Set("Content-Type", "application/json")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(constants.URLParamNamespace, "default")
	rctx.URLParams.Add(constants.URLParamName, "test-update-case")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rr := httptest.NewRecorder()
	handler.Update(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", rr.Code, http.StatusOK)
	}

	// Verify controller-set status preserved
	stored, err := store.Get(ctx, "default", "test-update-case")
	if err != nil {
		t.Fatalf("failed to get: %v", err)
	}

	obj := stored.(*object)
	if len(obj.Status.Conditions) != 1 || obj.Status.Conditions[0].Reason != "ControllerSet" {
		t.Error("case-variant STATUS was not stripped, controller status lost")
	}
}

// Test: Patch strips case-variant status keys
func TestStatusStripping_Patch_CaseVariants(t *testing.T) {
	handler, store := setupStatusStrippingTest(t)
	ctx := context.Background()

	// Create object with controller-set status
	existing := &object{
		TypeMeta: metav1.TypeMeta{
			APIVersion: objectGVK.GroupVersion().String(),
			Kind:       objectGVK.Kind,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-patch-case",
			Namespace: "default",
		},
		Spec: objectSpec{Field: "original"},
		Status: objectStatus{
			Conditions: []metav1.Condition{
				{Type: "Ready", Status: metav1.ConditionTrue, Reason: "ControllerSet"},
			},
		},
	}
	existing.SetGroupVersionKind(objectGVK)
	if err := store.Create(ctx, existing); err != nil {
		t.Fatalf("failed to create: %v", err)
	}

	patchJSON := mustMarshalJSON(t, map[string]interface{}{
		"Status": map[string]interface{}{
			"conditions": []interface{}{
				map[string]interface{}{"type": "Failed", "status": "True"},
			},
		},
	})

	req := httptest.NewRequest(http.MethodPatch, "/apis/test.io/v1/namespaces/default/objects/test-patch-case", bytes.NewReader(patchJSON))
	req.Header.Set("Content-Type", "application/merge-patch+json")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(constants.URLParamNamespace, "default")
	rctx.URLParams.Add(constants.URLParamName, "test-patch-case")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rr := httptest.NewRecorder()
	handler.Patch(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", rr.Code, http.StatusOK)
	}

	// Verify controller-set status preserved
	stored, err := store.Get(ctx, "default", "test-patch-case")
	if err != nil {
		t.Fatalf("failed to get: %v", err)
	}

	obj := stored.(*object)
	if len(obj.Status.Conditions) != 1 || obj.Status.Conditions[0].Reason != "ControllerSet" {
		t.Error("case-variant Status was not stripped, controller status lost")
	}
}

// Test: GET returns controller-set status (read path unaffected)
func TestStatusStripping_Get_ReturnsControllerSetStatus(t *testing.T) {
	handler, store := setupStatusStrippingTest(t)
	ctx := context.Background()

	// Create object with controller-set status (via private API / direct store)
	obj := &object{
		TypeMeta: metav1.TypeMeta{
			APIVersion: objectGVK.GroupVersion().String(),
			Kind:       objectGVK.Kind,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-get",
			Namespace: "default",
		},
		Spec: objectSpec{
			Field: "value",
		},
		Status: objectStatus{
			Conditions: []metav1.Condition{
				{
					Type:               "Ready",
					Status:             metav1.ConditionTrue,
					Reason:             "ControllerSet",
					Message:            "Controller set this",
					LastTransitionTime: metav1.Now(),
				},
			},
			PlacementResult: &statusPlacementResult{
				ManagementClusterName: "mgmt-cluster",
			},
		},
	}
	obj.SetGroupVersionKind(objectGVK)
	if err := store.Create(ctx, obj); err != nil {
		t.Fatalf("failed to create object: %v", err)
	}

	// GET via public API
	req := httptest.NewRequest(http.MethodGet, "/apis/test.io/v1/namespaces/default/objects/test-get", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(constants.URLParamNamespace, "default")
	rctx.URLParams.Add(constants.URLParamName, "test-get")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rr := httptest.NewRecorder()
	handler.Get(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", rr.Code, http.StatusOK)
	}

	// Verify response includes controller-set status
	var respBody map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &respBody); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	status, ok := respBody["status"].(map[string]interface{})
	if !ok || status == nil {
		t.Fatal("response missing status field")
	}

	conditions, ok := status["conditions"].([]interface{})
	if !ok || len(conditions) == 0 {
		t.Fatal("response missing status.conditions")
	}

	placementResult, ok := status["placementResult"].(map[string]interface{})
	if !ok || placementResult["managementClusterName"] != "mgmt-cluster" {
		t.Error("response missing or incorrect placementResult")
	}
}

// Test: Response to Create shows correct status (not what client tried to set)
func TestStatusStripping_Create_ResponseShowsCorrectStatus(t *testing.T) {
	handler, _ := setupStatusStrippingTest(t)

	createBody := map[string]interface{}{
		"apiVersion": objectGVK.GroupVersion().String(),
		"kind":       objectGVK.Kind,
		"metadata": map[string]interface{}{
			"name":      "test-response",
			"namespace": "default",
		},
		"spec": map[string]interface{}{
			"field": "value",
		},
		"status": map[string]interface{}{
			"conditions": []interface{}{
				map[string]interface{}{"type": "Ready", "status": "True", "reason": "ClientReason"},
			},
		},
	}

	bodyJSON := mustMarshalJSON(t, createBody)
	req := httptest.NewRequest(http.MethodPost, "/apis/test.io/v1/namespaces/default/objects", bytes.NewReader(bodyJSON))
	req.Header.Set("Content-Type", "application/json")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(constants.URLParamNamespace, "default")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rr := httptest.NewRecorder()
	handler.Create(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status code = %d, want %d", rr.Code, http.StatusCreated)
	}

	// Verify response status is empty (not client-supplied)
	var respBody map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &respBody); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	status, ok := respBody["status"].(map[string]interface{})
	if ok && status != nil {
		conditions, _ := status["conditions"].([]interface{})
		if len(conditions) != 0 {
			t.Error("response status.conditions should be empty, not client-supplied values")
		}
	}
}

// Test: Update response shows existing status, not client-supplied
func TestStatusStripping_Update_ResponseShowsExistingStatus(t *testing.T) {
	handler, store := setupStatusStrippingTest(t)
	ctx := context.Background()

	// Create with controller-set status
	existing := &object{
		TypeMeta: metav1.TypeMeta{
			APIVersion: objectGVK.GroupVersion().String(),
			Kind:       objectGVK.Kind,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-update-resp",
			Namespace: "default",
		},
		Spec: objectSpec{
			Field: "original",
		},
		Status: objectStatus{
			Conditions: []metav1.Condition{
				{
					Type:               "Ready",
					Status:             metav1.ConditionTrue,
					Reason:             "ControllerSet",
					Message:            "Controller message",
					LastTransitionTime: metav1.Now(),
				},
			},
			PlacementResult: &statusPlacementResult{
				ManagementClusterName: "controller-cluster",
			},
		},
	}
	existing.SetGroupVersionKind(objectGVK)
	if err := store.Create(ctx, existing); err != nil {
		t.Fatalf("failed to create: %v", err)
	}

	// Update with different status
	updateBody := map[string]interface{}{
		"apiVersion": objectGVK.GroupVersion().String(),
		"kind":       objectGVK.Kind,
		"metadata": map[string]interface{}{
			"name":      "test-update-resp",
			"namespace": "default",
		},
		"spec": map[string]interface{}{
			"field": "updated",
		},
		"status": map[string]interface{}{
			"conditions": []interface{}{
				map[string]interface{}{"type": "Ready", "status": "False", "reason": "ClientReason"},
			},
			"placementResult": map[string]interface{}{
				"managementClusterName": "evil-cluster",
			},
		},
	}

	bodyJSON := mustMarshalJSON(t, updateBody)
	req := httptest.NewRequest(http.MethodPut, "/apis/test.io/v1/namespaces/default/objects/test-update-resp", bytes.NewReader(bodyJSON))
	req.Header.Set("Content-Type", "application/json")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(constants.URLParamNamespace, "default")
	rctx.URLParams.Add(constants.URLParamName, "test-update-resp")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rr := httptest.NewRecorder()
	handler.Update(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d, body: %s", rr.Code, http.StatusOK, rr.Body.String())
	}

	// Verify response shows controller-set status, not client-supplied
	var respBody map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &respBody); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	status, ok := respBody["status"].(map[string]interface{})
	if !ok || status == nil {
		t.Fatal("response missing status")
	}

	conditions, ok := status["conditions"].([]interface{})
	if !ok || len(conditions) == 0 {
		t.Fatal("response missing conditions")
	}

	// Check first condition has controller-set reason (not client-supplied)
	cond0 := conditions[0].(map[string]interface{})
	if cond0["reason"] != "ControllerSet" {
		t.Errorf("response condition.reason = %q, want ControllerSet (not ClientReason)", cond0["reason"])
	}

	placementResult, ok := status["placementResult"].(map[string]interface{})
	if !ok || placementResult["managementClusterName"] != "controller-cluster" {
		t.Error("response placementResult should show controller-cluster, not evil-cluster")
	}
}

// Test: Patch response shows existing status, not client-supplied
func TestStatusStripping_Patch_ResponseShowsExistingStatus(t *testing.T) {
	handler, store := setupStatusStrippingTest(t)
	ctx := context.Background()

	// Create with controller-set status
	existing := &object{
		TypeMeta: metav1.TypeMeta{
			APIVersion: objectGVK.GroupVersion().String(),
			Kind:       objectGVK.Kind,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-patch-resp",
			Namespace: "default",
		},
		Spec: objectSpec{
			Field: "original",
		},
		Status: objectStatus{
			Conditions: []metav1.Condition{
				{
					Type:               "Ready",
					Status:             metav1.ConditionTrue,
					Reason:             "ControllerSet",
					Message:            "Controller message",
					LastTransitionTime: metav1.Now(),
				},
			},
			HostedClusterResult: &statusHostedClusterRes{
				APIEndpoint: "https://controller.example.com",
				Version:     "4.14.0",
			},
		},
	}
	existing.SetGroupVersionKind(objectGVK)
	if err := store.Create(ctx, existing); err != nil {
		t.Fatalf("failed to create: %v", err)
	}

	// Patch with different status
	patchBody := map[string]interface{}{
		"spec": map[string]interface{}{
			"value": 99,
		},
		"status": map[string]interface{}{
			"conditions": []interface{}{
				map[string]interface{}{"type": "Ready", "status": "False", "reason": "ClientReason"},
			},
			"hostedClusterResult": map[string]interface{}{
				"apiEndpoint": "https://evil.example.com",
				"version":     "4.99.0",
			},
		},
	}

	patchJSON := mustMarshalJSON(t, patchBody)
	req := httptest.NewRequest(http.MethodPatch, "/apis/test.io/v1/namespaces/default/objects/test-patch-resp", bytes.NewReader(patchJSON))
	req.Header.Set("Content-Type", "application/merge-patch+json")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(constants.URLParamNamespace, "default")
	rctx.URLParams.Add(constants.URLParamName, "test-patch-resp")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rr := httptest.NewRecorder()
	handler.Patch(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d, body: %s", rr.Code, http.StatusOK, rr.Body.String())
	}

	// Verify response shows controller-set status, not client-supplied
	var respBody map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &respBody); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	status, ok := respBody["status"].(map[string]interface{})
	if !ok || status == nil {
		t.Fatal("response missing status")
	}

	conditions, ok := status["conditions"].([]interface{})
	if !ok || len(conditions) == 0 {
		t.Fatal("response missing conditions")
	}

	// Check first condition has controller-set reason (not client-supplied)
	cond0 := conditions[0].(map[string]interface{})
	if cond0["reason"] != "ControllerSet" {
		t.Errorf("response condition.reason = %q, want ControllerSet (not ClientReason)", cond0["reason"])
	}

	hostedClusterResult, ok := status["hostedClusterResult"].(map[string]interface{})
	if !ok || hostedClusterResult["apiEndpoint"] != "https://controller.example.com" {
		t.Error("response hostedClusterResult should show controller values, not client-supplied")
	}
	if hostedClusterResult["version"] != "4.14.0" {
		t.Error("response hostedClusterResult.version should show 4.14.0, not 4.99.0")
	}
}

// Test: Update strips all status subfields (placementResult, hostedClusterResult)
func TestStatusStripping_Update_AllSubfieldsStripped(t *testing.T) {
	handler, store := setupStatusStrippingTest(t)
	ctx := context.Background()

	// Create without status
	existing := &object{
		TypeMeta: metav1.TypeMeta{
			APIVersion: objectGVK.GroupVersion().String(),
			Kind:       objectGVK.Kind,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-update-allfields",
			Namespace: "default",
		},
		Spec: objectSpec{
			Field: "original",
		},
	}
	existing.SetGroupVersionKind(objectGVK)
	if err := store.Create(ctx, existing); err != nil {
		t.Fatalf("failed to create: %v", err)
	}

	// Update with ALL status subfields
	updateBody := map[string]interface{}{
		"apiVersion": objectGVK.GroupVersion().String(),
		"kind":       objectGVK.Kind,
		"metadata": map[string]interface{}{
			"name":      "test-update-allfields",
			"namespace": "default",
		},
		"spec": map[string]interface{}{
			"field": "updated",
		},
		"status": map[string]interface{}{
			"conditions": []interface{}{
				map[string]interface{}{"type": "Ready", "status": "True"},
			},
			"placementResult": map[string]interface{}{
				"managementClusterName": "evil-placement",
			},
			"hostedClusterResult": map[string]interface{}{
				"apiEndpoint": "https://evil.example.com",
				"version":     "4.99.0",
			},
		},
	}

	bodyJSON := mustMarshalJSON(t, updateBody)
	req := httptest.NewRequest(http.MethodPut, "/apis/test.io/v1/namespaces/default/objects/test-update-allfields", bytes.NewReader(bodyJSON))
	req.Header.Set("Content-Type", "application/json")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(constants.URLParamNamespace, "default")
	rctx.URLParams.Add(constants.URLParamName, "test-update-allfields")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rr := httptest.NewRecorder()
	handler.Update(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", rr.Code, http.StatusOK)
	}

	// Verify ALL status subfields are empty
	stored, err := store.Get(ctx, "default", "test-update-allfields")
	if err != nil {
		t.Fatalf("failed to get stored object: %v", err)
	}

	obj, ok := stored.(*object)
	if !ok {
		t.Fatal("stored object wrong type")
	}

	if len(obj.Status.Conditions) != 0 {
		t.Errorf("conditions should be empty, got %d", len(obj.Status.Conditions))
	}
	if obj.Status.PlacementResult != nil {
		t.Errorf("placementResult should be nil, got %+v", obj.Status.PlacementResult)
	}
	if obj.Status.HostedClusterResult != nil {
		t.Errorf("hostedClusterResult should be nil, got %+v", obj.Status.HostedClusterResult)
	}
}

// Test: Patch strips all status subfields
func TestStatusStripping_Patch_AllSubfieldsStripped(t *testing.T) {
	handler, store := setupStatusStrippingTest(t)
	ctx := context.Background()

	// Create with controller-set status
	existing := &object{
		TypeMeta: metav1.TypeMeta{
			APIVersion: objectGVK.GroupVersion().String(),
			Kind:       objectGVK.Kind,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-patch-allfields",
			Namespace: "default",
		},
		Spec: objectSpec{
			Field: "original",
		},
		Status: objectStatus{
			Conditions: []metav1.Condition{
				{Type: "Ready", Status: metav1.ConditionTrue, Reason: "ControllerSet"},
			},
			PlacementResult: &statusPlacementResult{
				ManagementClusterName: "controller-cluster",
			},
			HostedClusterResult: &statusHostedClusterRes{
				APIEndpoint: "https://controller.example.com",
			},
		},
	}
	existing.SetGroupVersionKind(objectGVK)
	if err := store.Create(ctx, existing); err != nil {
		t.Fatalf("failed to create: %v", err)
	}

	// Patch attempting to change ALL status subfields
	patchBody := map[string]interface{}{
		"status": map[string]interface{}{
			"conditions": []interface{}{
				map[string]interface{}{"type": "Failed", "status": "True"},
			},
			"placementResult": map[string]interface{}{
				"managementClusterName": "evil-cluster",
			},
			"hostedClusterResult": map[string]interface{}{
				"apiEndpoint": "https://evil.example.com",
			},
		},
	}

	patchJSON := mustMarshalJSON(t, patchBody)
	req := httptest.NewRequest(http.MethodPatch, "/apis/test.io/v1/namespaces/default/objects/test-patch-allfields", bytes.NewReader(patchJSON))
	req.Header.Set("Content-Type", "application/merge-patch+json")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(constants.URLParamNamespace, "default")
	rctx.URLParams.Add(constants.URLParamName, "test-patch-allfields")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rr := httptest.NewRecorder()
	handler.Patch(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", rr.Code, http.StatusOK)
	}

	// Verify controller-set status preserved (patch ignored)
	stored, err := store.Get(ctx, "default", "test-patch-allfields")
	if err != nil {
		t.Fatalf("failed to get stored object: %v", err)
	}

	obj, ok := stored.(*object)
	if !ok {
		t.Fatal("stored object wrong type")
	}

	if len(obj.Status.Conditions) != 1 || obj.Status.Conditions[0].Reason != "ControllerSet" {
		t.Error("conditions changed after patch (should be preserved)")
	}
	if obj.Status.PlacementResult == nil || obj.Status.PlacementResult.ManagementClusterName != "controller-cluster" {
		t.Error("placementResult changed after patch (should be preserved)")
	}
	if obj.Status.HostedClusterResult == nil || obj.Status.HostedClusterResult.APIEndpoint != "https://controller.example.com" {
		t.Error("hostedClusterResult changed after patch (should be preserved)")
	}
}
