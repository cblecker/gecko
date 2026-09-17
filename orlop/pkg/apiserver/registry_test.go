package apiserver

import (
	"fmt"
	"strings"
	"testing"

	"github.com/openshift-online/gecko/orlop/pkg/apiserver/storage"
	"github.com/openshift-online/gecko/orlop/pkg/apiserver/storage/memory"
	"github.com/openshift-online/gecko/orlop/pkg/apiserver/types"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestNewResourceRegistry_DefaultStorage(t *testing.T) {
	scheme := runtime.NewScheme()
	registry := NewResourceRegistry(scheme)

	if registry == nil {
		t.Fatal("Expected registry to be created")
	}

	if registry.storageFactory == nil {
		t.Fatal("Expected default storage factory to be set")
	}

	// Verify default factory creates memory stores
	gvk := schema.GroupVersionKind{Group: "test", Version: "v1", Kind: "Test"}
	store, err := registry.storageFactory("test", scheme, gvk)
	if err != nil {
		t.Fatalf("Default factory failed: %v", err)
	}

	if _, ok := store.(*memory.MemoryStore); !ok {
		t.Errorf("Expected MemoryStore, got %T", store)
	}
}

func TestNewResourceRegistry_CustomStorage(t *testing.T) {
	scheme := runtime.NewScheme()

	customCalled := false
	customFactory := func(resourceType string, s *runtime.Scheme, gvk schema.GroupVersionKind) (storage.ResourceStore, error) {
		customCalled = true
		return memory.NewMemoryStore(resourceType, s, gvk), nil
	}

	registry := NewResourceRegistry(scheme, WithStorageFactory(customFactory))

	if registry == nil {
		t.Fatal("Expected registry to be created")
	}

	// Register a resource to trigger factory
	info := types.ResourceInfo{
		GVK:        schema.GroupVersionKind{Group: "test", Version: "v1", Kind: "Test"},
		Plural:     "tests",
		SchemaYAML: "type: object",
	}

	err := registry.Register(info)
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	if !customCalled {
		t.Error("Custom factory was not called")
	}

	store := registry.GetStore(schema.GroupKind{Group: "test", Kind: "Test"})
	if store == nil {
		t.Error("Expected store to be created")
	}
}

func TestRegister_FactoryError(t *testing.T) {
	scheme := runtime.NewScheme()

	failingFactory := func(resourceType string, s *runtime.Scheme, gvk schema.GroupVersionKind) (storage.ResourceStore, error) {
		return nil, fmt.Errorf("factory error")
	}

	registry := NewResourceRegistry(scheme, WithStorageFactory(failingFactory))

	info := types.ResourceInfo{
		GVK:        schema.GroupVersionKind{Group: "test", Version: "v1", Kind: "Test"},
		Plural:     "tests",
		SchemaYAML: "type: object",
	}

	err := registry.Register(info)
	if err == nil {
		t.Error("Expected error from failing factory")
	}

	if err.Error() != "failed to create storage for test_test: factory error" {
		t.Errorf("Unexpected error message: %v", err)
	}
}

func TestRegister_StoresResource(t *testing.T) {
	scheme := runtime.NewScheme()
	registry := NewResourceRegistry(scheme)

	info := types.ResourceInfo{
		GVK:        schema.GroupVersionKind{Group: "test", Version: "v1", Kind: "Test"},
		Plural:     "tests",
		SchemaYAML: "type: object",
	}

	err := registry.Register(info)
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	// Verify resource is stored
	resources := registry.GetResources()
	if len(resources) != 1 {
		t.Errorf("Expected 1 resource, got %d", len(resources))
	}

	if resources[0].Plural != "tests" {
		t.Errorf("Expected plural 'tests', got %s", resources[0].Plural)
	}

	// Verify store is created
	store := registry.GetStore(schema.GroupKind{Group: "test", Kind: "Test"})
	if store == nil {
		t.Error("Expected store to be created")
	}
}

func TestParentStore(t *testing.T) {
	scheme := runtime.NewScheme()
	registry := NewResourceRegistry(scheme)
	parentGVK := schema.GroupVersionKind{Group: "test", Version: "v1", Kind: "Parent"}
	childInfo := types.ResourceInfo{
		GVK: schema.GroupVersionKind{Group: "test", Version: "v1", Kind: "Child"},
		ParentResource: &types.ParentResourceInfo{
			GroupKind: parentGVK.GroupKind(),
			IDField:   "spec.parentID",
		},
	}

	if err := registry.Register(types.ResourceInfo{GVK: parentGVK}); err != nil {
		t.Fatalf("register parent: %v", err)
	}
	parentStore, err := registry.parentStore(childInfo)
	if err != nil {
		t.Fatalf("resolve parent store: %v", err)
	}
	if parentStore != registry.GetStore(parentGVK.GroupKind()) {
		t.Error("resolved store does not match the registered parent store")
	}

	missingParent := childInfo
	missingParent.ParentResource = &types.ParentResourceInfo{
		GroupKind: schema.GroupKind{Group: "test", Kind: "MissingParent"},
		IDField:   "spec.parentID",
	}
	_, err = registry.parentStore(missingParent)
	if err == nil || !strings.Contains(err.Error(), "no store found for parent resource") {
		t.Errorf("missing parent store error = %v", err)
	}

	missingParent.SchemaYAML = "type: object"
	if err := registry.Register(missingParent); err != nil {
		t.Fatalf("register child: %v", err)
	}
	if _, err := registry.CreateHandler(missingParent); err == nil || !strings.Contains(err.Error(), "no store found for parent resource") {
		t.Errorf("CreateHandler missing parent store error = %v", err)
	}
}
