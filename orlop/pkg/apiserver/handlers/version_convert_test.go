package handlers

import (
	"testing"

	privatev1 "github.com/openshift-online/gecko/orlop/apis/private/test/v1"
	privatev2 "github.com/openshift-online/gecko/orlop/apis/private/test/v2"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	gvkV1 = schema.GroupVersionKind{Group: "test.orlop.gcp.managed.openshift.io", Version: "v1", Kind: "Object"}
	gvkV2 = schema.GroupVersionKind{Group: "test.orlop.gcp.managed.openshift.io", Version: "v2", Kind: "Object"}
)

func testScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	if err := privatev1.AddToScheme(s); err != nil {
		panic(err)
	}
	if err := privatev2.AddToScheme(s); err != nil {
		panic(err)
	}
	return s
}

func newHandler(servingGVK, storageGVK schema.GroupVersionKind) *ResourceHandler {
	h := &ResourceHandler{
		gvk:    servingGVK,
		scheme: testScheme(),
	}
	if storageGVK != servingGVK {
		h.storageGVK = storageGVK
	}
	return h
}

func TestNeedsConversion(t *testing.T) {
	tests := []struct {
		name       string
		servingGVK schema.GroupVersionKind
		storageGVK schema.GroupVersionKind
		want       bool
	}{
		{"zero storage GVK", gvkV1, schema.GroupVersionKind{}, false},
		{"same version", gvkV1, gvkV1, false},
		{"different version", gvkV2, gvkV1, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &ResourceHandler{gvk: tt.servingGVK, storageGVK: tt.storageGVK, scheme: testScheme()}
			if got := h.needsConversion(); got != tt.want {
				t.Errorf("needsConversion() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestConvertToServingVersion_NoOp(t *testing.T) {
	h := newHandler(gvkV1, gvkV1)

	obj := &privatev1.Object{}
	obj.SetGroupVersionKind(gvkV1)
	obj.SetName("test")

	result, err := h.convertToServingVersion(obj)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != obj {
		t.Error("expected same object when no conversion needed")
	}
}

func TestConvertToServingVersion_V1ToV2(t *testing.T) {
	h := newHandler(gvkV2, gvkV1)

	v1Obj := &privatev1.Object{}
	v1Obj.SetGroupVersionKind(gvkV1)
	v1Obj.SetName("test-obj")
	v1Obj.SetNamespace("default")
	v1Obj.Spec.PublicField = "hello"
	v1Obj.Spec.InternalField = "secret"

	result, err := h.convertToServingVersion(v1Obj)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.GetObjectKind().GroupVersionKind() != gvkV2 {
		t.Errorf("GVK = %v, want %v", result.GetObjectKind().GroupVersionKind(), gvkV2)
	}
	if result.GetName() != "test-obj" {
		t.Errorf("name = %q, want %q", result.GetName(), "test-obj")
	}

	v2Obj, ok := result.(*privatev2.Object)
	if !ok {
		t.Fatalf("result type = %T, want *privatev2.Object", result)
	}
	if v2Obj.Spec.PublicField != "hello" {
		t.Errorf("PublicField = %q, want %q", v2Obj.Spec.PublicField, "hello")
	}
	if v2Obj.Spec.InternalField != "secret" {
		t.Errorf("InternalField = %q, want %q", v2Obj.Spec.InternalField, "secret")
	}
}

func TestConvertToStorageVersion_V2ToV1(t *testing.T) {
	h := newHandler(gvkV2, gvkV1)

	v2Obj := &privatev2.Object{}
	v2Obj.SetGroupVersionKind(gvkV2)
	v2Obj.SetName("test-obj")
	v2Obj.SetNamespace("default")
	v2Obj.Spec.PublicField = "world"

	result, err := h.convertToStorageVersion(v2Obj)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.GetObjectKind().GroupVersionKind() != gvkV1 {
		t.Errorf("GVK = %v, want %v", result.GetObjectKind().GroupVersionKind(), gvkV1)
	}

	v1Obj, ok := result.(*privatev1.Object)
	if !ok {
		t.Fatalf("result type = %T, want *privatev1.Object", result)
	}
	if v1Obj.Spec.PublicField != "world" {
		t.Errorf("PublicField = %q, want %q", v1Obj.Spec.PublicField, "world")
	}
}

func TestConvertRoundTrip(t *testing.T) {
	h := newHandler(gvkV2, gvkV1)

	v1Obj := &privatev1.Object{}
	v1Obj.SetGroupVersionKind(gvkV1)
	v1Obj.SetName("round-trip")
	v1Obj.SetNamespace("ns")
	v1Obj.SetResourceVersion("42")
	v1Obj.Spec.PublicField = "pub"
	v1Obj.Spec.InternalField = "int"
	v1Obj.Spec.Nested.PublicField = "nested-pub"
	v1Obj.Spec.DefaultField = "def"

	// v1 → v2
	v2Result, err := h.convertToServingVersion(v1Obj)
	if err != nil {
		t.Fatalf("convertToServingVersion: %v", err)
	}

	// v2 → v1
	v1Result, err := h.convertToStorageVersion(v2Result)
	if err != nil {
		t.Fatalf("convertToStorageVersion: %v", err)
	}

	got := v1Result.(*privatev1.Object)
	if got.Spec.PublicField != "pub" {
		t.Errorf("PublicField = %q, want %q", got.Spec.PublicField, "pub")
	}
	if got.Spec.InternalField != "int" {
		t.Errorf("InternalField = %q, want %q", got.Spec.InternalField, "int")
	}
	if got.Spec.Nested.PublicField != "nested-pub" {
		t.Errorf("Nested.PublicField = %q, want %q", got.Spec.Nested.PublicField, "nested-pub")
	}
	if got.GetResourceVersion() != "42" {
		t.Errorf("ResourceVersion = %q, want %q", got.GetResourceVersion(), "42")
	}
}

func TestConvertListToServingVersion(t *testing.T) {
	h := newHandler(gvkV2, gvkV1)

	list := &privatev1.ObjectList{}
	list.SetGroupVersionKind(gvkV1.GroupVersion().WithKind("ObjectList"))

	for _, name := range []string{"a", "b", "c"} {
		item := privatev1.Object{}
		item.SetGroupVersionKind(gvkV1)
		item.SetName(name)
		item.SetNamespace("default")
		list.Items = append(list.Items, item)
	}

	result, err := h.convertListToServingVersion(list)
	if err != nil {
		t.Fatalf("convertListToServingVersion: %v", err)
	}

	v2List, ok := result.(*privatev2.ObjectList)
	if !ok {
		t.Fatalf("result type = %T, want *privatev2.ObjectList", result)
	}

	if len(v2List.Items) != 3 {
		t.Fatalf("item count = %d, want 3", len(v2List.Items))
	}

	for i, item := range v2List.Items {
		if item.GetObjectKind().GroupVersionKind() != gvkV2 {
			t.Errorf("item[%d] GVK = %v, want %v", i, item.GetObjectKind().GroupVersionKind(), gvkV2)
		}
	}
}

func TestConvertListToServingVersion_NoOp(t *testing.T) {
	h := newHandler(gvkV1, gvkV1)

	list := &privatev1.ObjectList{}
	list.SetGroupVersionKind(gvkV1.GroupVersion().WithKind("ObjectList"))

	item := privatev1.Object{}
	item.SetGroupVersionKind(gvkV1)
	item.SetName("x")
	list.Items = append(list.Items, item)

	result, err := h.convertListToServingVersion(list)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result != list {
		t.Error("expected same list when no conversion needed")
	}
}

func TestEffectiveStorageGVK(t *testing.T) {
	h := newHandler(gvkV2, gvkV1)
	if got := h.effectiveStorageGVK(); got != gvkV1 {
		t.Errorf("effectiveStorageGVK() = %v, want %v", got, gvkV1)
	}

	h2 := newHandler(gvkV1, gvkV1)
	if got := h2.effectiveStorageGVK(); got != gvkV1 {
		t.Errorf("effectiveStorageGVK() = %v, want %v", got, gvkV1)
	}
}
