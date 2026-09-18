package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/openshift-online/gecko/orlop/pkg/apiserver/types"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	runtimeschema "k8s.io/apimachinery/pkg/runtime/schema"
)

// mockResourceProvider implements ResourceProvider for testing.
type mockResourceProvider struct {
	resources []types.ResourceInfo
}

func (m *mockResourceProvider) Resources() []types.ResourceInfo {
	return m.resources
}

func TestAPIResourceList_AdvertiseStatusFalse(t *testing.T) {
	// Setup test resources
	provider := &mockResourceProvider{
		resources: []types.ResourceInfo{
			{
				GVK: runtimeschema.GroupVersionKind{
					Group:   "test.orlop.gcp.managed.openshift.io",
					Version: "v1",
					Kind:    "Object",
				},
				Plural:     "objects",
				Singular:   "object",
				Namespaced: true,
				SchemaYAML: "type: object\nproperties:\n  spec:\n    type: object",
			},
		},
	}

	// Create handler with advertiseStatus=false
	advertiseStatus := false
	handler := NewDiscoveryHandler(provider, &DiscoveryOptions{
		AdvertiseStatus: &advertiseStatus,
	})

	// Create test request
	req := httptest.NewRequest(http.MethodGet, "/apis/test.orlop.gcp.managed.openshift.io/v1", nil)
	w := httptest.NewRecorder()

	// Call handler
	handler.APIResourceList(w, req, "test.orlop.gcp.managed.openshift.io", "v1")

	// Verify response
	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}

	var resourceList metav1.APIResourceList
	if err := json.Unmarshal(w.Body.Bytes(), &resourceList); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	// Should have exactly 1 resource (no status subresource)
	if len(resourceList.APIResources) != 1 {
		t.Errorf("expected 1 resource, got %d", len(resourceList.APIResources))
	}

	// Verify no status subresource
	for _, res := range resourceList.APIResources {
		if res.Name == "objects/status" {
			t.Errorf("advertiseStatus=false should NOT include status subresource, but found: %s", res.Name)
		}
	}
}

func TestAPIResourceList_AdvertiseStatusTrue(t *testing.T) {
	// Setup test resources
	provider := &mockResourceProvider{
		resources: []types.ResourceInfo{
			{
				GVK: runtimeschema.GroupVersionKind{
					Group:   "test.orlop.gcp.managed.openshift.io",
					Version: "v1",
					Kind:    "Object",
				},
				Plural:     "objects",
				Singular:   "object",
				Namespaced: true,
				SchemaYAML: "type: object\nproperties:\n  spec:\n    type: object",
			},
		},
	}

	// Create handler with advertiseStatus=true
	advertiseStatus := true
	handler := NewDiscoveryHandler(provider, &DiscoveryOptions{
		AdvertiseStatus: &advertiseStatus,
	})

	// Create test request
	req := httptest.NewRequest(http.MethodGet, "/apis/test.orlop.gcp.managed.openshift.io/v1", nil)
	w := httptest.NewRecorder()

	// Call handler
	handler.APIResourceList(w, req, "test.orlop.gcp.managed.openshift.io", "v1")

	// Verify response
	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}

	var resourceList metav1.APIResourceList
	if err := json.Unmarshal(w.Body.Bytes(), &resourceList); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	// Should have exactly 2 resources (main + status)
	if len(resourceList.APIResources) != 2 {
		t.Errorf("expected 2 resources, got %d", len(resourceList.APIResources))
	}

	// Verify status subresource present
	foundStatus := false
	for _, res := range resourceList.APIResources {
		if res.Name == "objects/status" {
			foundStatus = true
			// Verify status subresource has correct verbs
			expectedVerbs := []string{"get", "patch", "update"}
			if !slices.Equal(res.Verbs, expectedVerbs) {
				t.Errorf("status subresource verbs = %v, want %v", res.Verbs, expectedVerbs)
			}
		}
	}

	if !foundStatus {
		t.Errorf("advertiseStatus=true should include status subresource")
	}
}

func TestAPIResourceList_AdvertiseStatusNil(t *testing.T) {
	// Setup test resources
	provider := &mockResourceProvider{
		resources: []types.ResourceInfo{
			{
				GVK: runtimeschema.GroupVersionKind{
					Group:   "test.orlop.gcp.managed.openshift.io",
					Version: "v1",
					Kind:    "Object",
				},
				Plural:     "objects",
				Singular:   "object",
				Namespaced: true,
				SchemaYAML: "type: object\nproperties:\n  spec:\n    type: object",
			},
		},
	}

	// Create handler with advertiseStatus=nil (defaults to true)
	handler := NewDiscoveryHandler(provider, &DiscoveryOptions{
		AdvertiseStatus: nil,
	})

	// Create test request
	req := httptest.NewRequest(http.MethodGet, "/apis/test.orlop.gcp.managed.openshift.io/v1", nil)
	w := httptest.NewRecorder()

	// Call handler
	handler.APIResourceList(w, req, "test.orlop.gcp.managed.openshift.io", "v1")

	// Verify response
	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}

	var resourceList metav1.APIResourceList
	if err := json.Unmarshal(w.Body.Bytes(), &resourceList); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	// Should have exactly 2 resources (main + status) because nil defaults to true
	if len(resourceList.APIResources) != 2 {
		t.Errorf("expected 2 resources (nil defaults to true), got %d", len(resourceList.APIResources))
	}

	// Verify status subresource present
	foundStatus := false
	for _, res := range resourceList.APIResources {
		if res.Name == "objects/status" {
			foundStatus = true
		}
	}

	if !foundStatus {
		t.Errorf("advertiseStatus=nil should default to true and include status subresource")
	}
}

func TestAPIResourceList_MultipleResources(t *testing.T) {
	// Setup test resources with multiple resource types
	provider := &mockResourceProvider{
		resources: []types.ResourceInfo{
			{
				GVK: runtimeschema.GroupVersionKind{
					Group:   "test.orlop.gcp.managed.openshift.io",
					Version: "v1",
					Kind:    "Object",
				},
				Plural:     "objects",
				Singular:   "object",
				Namespaced: true,
				SchemaYAML: "type: object\nproperties:\n  spec:\n    type: object",
			},
			{
				GVK: runtimeschema.GroupVersionKind{
					Group:   "test.orlop.gcp.managed.openshift.io",
					Version: "v1",
					Kind:    "Widget",
				},
				Plural:     "widgets",
				Singular:   "widget",
				Namespaced: true,
				SchemaYAML: "type: object\nproperties:\n  spec:\n    type: object",
			},
		},
	}

	t.Run("advertiseStatus=false with multiple resources", func(t *testing.T) {
		advertiseStatus := false
		handler := NewDiscoveryHandler(provider, &DiscoveryOptions{
			AdvertiseStatus: &advertiseStatus,
		})

		req := httptest.NewRequest(http.MethodGet, "/apis/test.orlop.gcp.managed.openshift.io/v1", nil)
		w := httptest.NewRecorder()

		handler.APIResourceList(w, req, "test.orlop.gcp.managed.openshift.io", "v1")

		if w.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d", w.Code)
		}

		var resourceList metav1.APIResourceList
		if err := json.Unmarshal(w.Body.Bytes(), &resourceList); err != nil {
			t.Fatalf("failed to unmarshal response: %v", err)
		}

		// Should have exactly 2 resources (no status subresources)
		if len(resourceList.APIResources) != 2 {
			t.Errorf("expected 2 resources, got %d", len(resourceList.APIResources))
		}

		// Verify no status subresources
		for _, res := range resourceList.APIResources {
			if res.Name == "objects/status" || res.Name == "widgets/status" {
				t.Errorf("advertiseStatus=false should NOT include status subresource, but found: %s", res.Name)
			}
		}
	})

	t.Run("advertiseStatus=true with multiple resources", func(t *testing.T) {
		advertiseStatus := true
		handler := NewDiscoveryHandler(provider, &DiscoveryOptions{
			AdvertiseStatus: &advertiseStatus,
		})

		req := httptest.NewRequest(http.MethodGet, "/apis/test.orlop.gcp.managed.openshift.io/v1", nil)
		w := httptest.NewRecorder()

		handler.APIResourceList(w, req, "test.orlop.gcp.managed.openshift.io", "v1")

		if w.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d", w.Code)
		}

		var resourceList metav1.APIResourceList
		if err := json.Unmarshal(w.Body.Bytes(), &resourceList); err != nil {
			t.Fatalf("failed to unmarshal response: %v", err)
		}

		// Should have exactly 4 resources (2 main + 2 status)
		if len(resourceList.APIResources) != 4 {
			t.Errorf("expected 4 resources, got %d", len(resourceList.APIResources))
		}

		// Verify both status subresources present
		foundObjectStatus := false
		foundWidgetStatus := false
		for _, res := range resourceList.APIResources {
			if res.Name == "objects/status" {
				foundObjectStatus = true
			}
			if res.Name == "widgets/status" {
				foundWidgetStatus = true
			}
		}

		if !foundObjectStatus {
			t.Errorf("advertiseStatus=true should include objects/status subresource")
		}
		if !foundWidgetStatus {
			t.Errorf("advertiseStatus=true should include widgets/status subresource")
		}
	})
}
