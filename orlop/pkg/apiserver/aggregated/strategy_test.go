package aggregated

import (
	"context"
	"fmt"
	"testing"

	"github.com/go-logr/logr"

	testv1 "github.com/openshift-online/gecko/orlop/apis/private/test/v1"
	"github.com/openshift-online/gecko/orlop/pkg/apiserver/constants"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/endpoints/request"
)

func newTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := testv1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add test scheme: %v", err)
	}
	return scheme
}

var testGVK = schema.GroupVersionKind{
	Group:   testv1.GroupVersion.Group,
	Version: testv1.GroupVersion.Version,
	Kind:    "Object",
}

func newTestStrategy(t *testing.T, namespaced bool) *ResourceStrategy {
	t.Helper()
	return NewResourceStrategy(newTestScheme(t), nil, namespaced, testGVK, logr.Discard())
}

func TestPrepareForCreate(t *testing.T) {
	strategy := newTestStrategy(t, true)
	ctx := context.Background()

	obj := &testv1.Object{
		TypeMeta: metav1.TypeMeta{
			APIVersion: testv1.GroupVersion.String(),
			Kind:       "Object",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-obj",
			Namespace: "default",
		},
		Spec: testv1.ObjectSpec{
			PublicField: "value",
		},
	}

	strategy.PrepareForCreate(ctx, obj)

	if obj.UID == "" {
		t.Error("expected UID to be set after PrepareForCreate")
	}
	if obj.CreationTimestamp.IsZero() {
		t.Error("expected CreationTimestamp to be set after PrepareForCreate")
	}
	if obj.Generation != 1 {
		t.Errorf("expected Generation to be 1, got %d", obj.Generation)
	}
}

func TestPrepareForUpdate_SpecChanged(t *testing.T) {
	strategy := newTestStrategy(t, true)
	ctx := context.Background()

	oldObj := &testv1.Object{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-obj",
			Namespace:  "default",
			UID:        "old-uid",
			Generation: 3,
			CreationTimestamp: metav1.Time{
				Time: metav1.Now().Add(-1 * 60 * 1e9),
			},
		},
		Spec: testv1.ObjectSpec{
			PublicField: "original",
		},
	}

	newObj := &testv1.Object{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-obj",
			Namespace: "default",
		},
		Spec: testv1.ObjectSpec{
			PublicField: "changed",
		},
	}

	strategy.PrepareForUpdate(ctx, newObj, oldObj)

	if newObj.Generation != 4 {
		t.Errorf("expected Generation to be 4 (incremented), got %d", newObj.Generation)
	}
}

func TestPrepareForUpdate_SpecUnchanged(t *testing.T) {
	strategy := newTestStrategy(t, true)
	ctx := context.Background()

	oldObj := &testv1.Object{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-obj",
			Namespace:  "default",
			UID:        "old-uid",
			Generation: 5,
			CreationTimestamp: metav1.Time{
				Time: metav1.Now().Add(-1 * 60 * 1e9),
			},
		},
		Spec: testv1.ObjectSpec{
			PublicField: "same-value",
		},
	}

	newObj := &testv1.Object{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-obj",
			Namespace: "default",
		},
		Spec: testv1.ObjectSpec{
			PublicField: "same-value",
		},
	}

	strategy.PrepareForUpdate(ctx, newObj, oldObj)

	if newObj.Generation != 5 {
		t.Errorf("expected Generation to remain 5, got %d", newObj.Generation)
	}
}

func TestPrepareForUpdate_PreservesMetadata(t *testing.T) {
	strategy := newTestStrategy(t, true)
	ctx := context.Background()

	creationTime := metav1.Now()
	oldObj := &testv1.Object{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "test-obj",
			Namespace:         "default",
			UID:               "preserved-uid",
			Generation:        2,
			CreationTimestamp: creationTime,
		},
		Spec: testv1.ObjectSpec{
			PublicField: "value",
		},
	}

	newObj := &testv1.Object{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-obj",
			Namespace: "default",
			// UID and CreationTimestamp intentionally not set;
			// PrepareForUpdate should copy them from old.
		},
		Spec: testv1.ObjectSpec{
			PublicField: "value",
		},
	}

	strategy.PrepareForUpdate(ctx, newObj, oldObj)

	if newObj.UID != "preserved-uid" {
		t.Errorf("expected UID %q, got %q", "preserved-uid", newObj.UID)
	}
	if !newObj.CreationTimestamp.Equal(&creationTime) {
		t.Errorf("expected CreationTimestamp %v, got %v", creationTime, newObj.CreationTimestamp)
	}
}

func TestNamespaceScoped(t *testing.T) {
	tests := []struct {
		name       string
		namespaced bool
	}{
		{"returns true when namespaced", true},
		{"returns false when cluster-scoped", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			strategy := newTestStrategy(t, tc.namespaced)
			if got := strategy.NamespaceScoped(); got != tc.namespaced {
				t.Errorf("NamespaceScoped() = %v, want %v", got, tc.namespaced)
			}
		})
	}
}

func TestAllowCreateOnUpdate(t *testing.T) {
	strategy := newTestStrategy(t, true)
	if strategy.AllowCreateOnUpdate(t.Context()) {
		t.Error("expected AllowCreateOnUpdate() to return false")
	}
}

func TestAllowUnconditionalUpdate(t *testing.T) {
	strategy := newTestStrategy(t, true)
	if strategy.AllowUnconditionalUpdate(t.Context()) {
		t.Error("expected AllowUnconditionalUpdate() to return false")
	}
}

func TestPrepareForUpdate_PreservesDeletionTimestamp(t *testing.T) {
	strategy := newTestStrategy(t, true)
	ctx := context.Background()

	deletionTime := metav1.Now()
	oldObj := &testv1.Object{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "test-obj",
			Namespace:         "default",
			UID:               "old-uid",
			Generation:        2,
			DeletionTimestamp: &deletionTime,
			Finalizers:        []string{"test-finalizer"},
			CreationTimestamp: metav1.Time{Time: metav1.Now().Add(-1 * 60 * 1e9)},
		},
		Spec: testv1.ObjectSpec{
			PublicField: "value",
		},
	}

	newObj := &testv1.Object{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-obj",
			Namespace: "default",
			// DeletionTimestamp intentionally not set by client.
		},
		Spec: testv1.ObjectSpec{
			PublicField: "value",
		},
	}

	strategy.PrepareForUpdate(ctx, newObj, oldObj)

	if newObj.DeletionTimestamp == nil {
		t.Fatal("expected DeletionTimestamp to be preserved, got nil")
	}
	if !newObj.DeletionTimestamp.Equal(&deletionTime) {
		t.Errorf("expected DeletionTimestamp %v, got %v", deletionTime, newObj.DeletionTimestamp)
	}
}

func TestPrepareForUpdate_NoDeletionTimestamp(t *testing.T) {
	strategy := newTestStrategy(t, true)
	ctx := context.Background()

	oldObj := &testv1.Object{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "test-obj",
			Namespace:         "default",
			UID:               "old-uid",
			Generation:        2,
			CreationTimestamp: metav1.Time{Time: metav1.Now().Add(-1 * 60 * 1e9)},
		},
		Spec: testv1.ObjectSpec{
			PublicField: "value",
		},
	}

	newObj := &testv1.Object{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-obj",
			Namespace: "default",
		},
		Spec: testv1.ObjectSpec{
			PublicField: "value",
		},
	}

	strategy.PrepareForUpdate(ctx, newObj, oldObj)

	if newObj.DeletionTimestamp != nil {
		t.Errorf("expected DeletionTimestamp to remain nil, got %v", newObj.DeletionTimestamp)
	}
}

// defaulterObject wraps testv1.Object to implement types.CustomDefaulter.
type defaulterObject struct {
	testv1.Object
	defaultCalled bool
	defaultErr    error
}

func (d *defaulterObject) Default(_ context.Context) error {
	d.defaultCalled = true
	return d.defaultErr
}

func (d *defaulterObject) DeepCopyObject() runtime.Object {
	cp := *d
	cp.Object = *d.DeepCopy()
	return &cp
}

func TestPrepareForCreate_CallsCustomDefaulter(t *testing.T) {
	strategy := newTestStrategy(t, true)
	ctx := context.Background()

	obj := &defaulterObject{
		Object: testv1.Object{
			TypeMeta: metav1.TypeMeta{
				APIVersion: testv1.GroupVersion.String(),
				Kind:       "Object",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-obj",
				Namespace: "default",
			},
		},
	}

	strategy.PrepareForCreate(ctx, obj)

	if !obj.defaultCalled {
		t.Error("expected CustomDefaulter.Default to be called during PrepareForCreate")
	}
}

func TestPrepareForUpdate_CallsCustomDefaulter(t *testing.T) {
	strategy := newTestStrategy(t, true)
	ctx := context.Background()

	oldObj := &testv1.Object{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "test-obj",
			Namespace:         "default",
			UID:               "old-uid",
			Generation:        1,
			CreationTimestamp: metav1.Time{Time: metav1.Now().Add(-1 * 60 * 1e9)},
		},
		Spec: testv1.ObjectSpec{PublicField: "value"},
	}

	newObj := &defaulterObject{
		Object: testv1.Object{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-obj",
				Namespace: "default",
			},
			Spec: testv1.ObjectSpec{PublicField: "value"},
		},
	}

	strategy.PrepareForUpdate(ctx, newObj, oldObj)

	if !newObj.defaultCalled {
		t.Error("expected CustomDefaulter.Default to be called during PrepareForUpdate")
	}
}

// validatorObject wraps testv1.Object to implement types.CustomValidator.
type validatorObject struct {
	testv1.Object
	validateCreateErr error
	validateUpdateErr error
}

func (v *validatorObject) ValidateCreate(_ context.Context) error {
	return v.validateCreateErr
}

func (v *validatorObject) ValidateUpdate(_ context.Context, _ runtime.Object) error {
	return v.validateUpdateErr
}

func (v *validatorObject) ValidateDelete(_ context.Context) error {
	return nil
}

func (v *validatorObject) DeepCopyObject() runtime.Object {
	cp := *v
	cp.Object = *v.DeepCopy()
	return &cp
}

func TestValidate_CallsCustomValidator(t *testing.T) {
	strategy := newTestStrategy(t, true)
	ctx := context.Background()

	obj := &validatorObject{
		Object: testv1.Object{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-obj",
				Namespace: "default",
			},
		},
	}

	// No error case - should return no errors.
	errs := strategy.Validate(ctx, obj)
	if len(errs) != 0 {
		t.Errorf("expected no errors, got %v", errs)
	}

	// Error case - should return error in ErrorList.
	obj.validateCreateErr = fmt.Errorf("custom validation failed")
	errs = strategy.Validate(ctx, obj)
	if len(errs) != 1 {
		t.Fatalf("expected 1 error, got %d: %v", len(errs), errs)
	}
	if errs[0].Detail != "custom validation failed" {
		t.Errorf("expected error detail %q, got %q", "custom validation failed", errs[0].Detail)
	}
}

func TestValidateUpdate_CallsCustomValidator(t *testing.T) {
	strategy := newTestStrategy(t, true)
	ctx := context.Background()

	oldObj := &testv1.Object{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-obj",
			Namespace: "default",
		},
	}

	obj := &validatorObject{
		Object: testv1.Object{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-obj",
				Namespace: "default",
			},
		},
	}

	// No error case.
	errs := strategy.ValidateUpdate(ctx, obj, oldObj)
	if len(errs) != 0 {
		t.Errorf("expected no errors, got %v", errs)
	}

	// Error case.
	obj.validateUpdateErr = fmt.Errorf("update validation failed")
	errs = strategy.ValidateUpdate(ctx, obj, oldObj)
	if len(errs) != 1 {
		t.Fatalf("expected 1 error, got %d: %v", len(errs), errs)
	}
	if errs[0].Detail != "update validation failed" {
		t.Errorf("expected error detail %q, got %q", "update validation failed", errs[0].Detail)
	}
}

func TestGenerateName(t *testing.T) {
	strategy := newTestStrategy(t, true)

	name := strategy.GenerateName("foo-")
	if len(name) < len("foo-")+5 {
		t.Errorf("generated name too short: %q", name)
	}
	if name[:4] != "foo-" {
		t.Errorf("expected prefix %q, got %q", "foo-", name[:4])
	}

	// Two calls should produce different names.
	name2 := strategy.GenerateName("foo-")
	if name == name2 {
		t.Errorf("expected distinct names, got %q twice", name)
	}
}

func TestPrepareForCreate_SetsCreatedByFromUserInfo(t *testing.T) {
	strategy := newTestStrategy(t, true)

	obj := &testv1.Object{
		TypeMeta: metav1.TypeMeta{
			APIVersion: testv1.GroupVersion.String(),
			Kind:       "Object",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-obj",
			Namespace: "default",
		},
	}

	ctx := request.WithUser(context.Background(), &user.DefaultInfo{
		Name: "admin@example.com",
	})

	strategy.PrepareForCreate(ctx, obj)

	got := obj.GetAnnotations()[constants.AnnotationCreatedBy]
	if got != "admin@example.com" {
		t.Errorf("created-by annotation = %q, want %q", got, "admin@example.com")
	}
}

func TestPrepareForCreate_NoUserInfo_NoCreatedByAnnotation(t *testing.T) {
	strategy := newTestStrategy(t, true)

	obj := &testv1.Object{
		TypeMeta: metav1.TypeMeta{
			APIVersion: testv1.GroupVersion.String(),
			Kind:       "Object",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-obj",
			Namespace: "default",
		},
	}

	strategy.PrepareForCreate(context.Background(), obj)

	annotations := obj.GetAnnotations()
	if annotations != nil {
		if _, ok := annotations[constants.AnnotationCreatedBy]; ok {
			t.Error("expected no created-by annotation when no user info in context")
		}
	}
}

func TestPrepareForUpdate_PreservesCreatedByAnnotation(t *testing.T) {
	strategy := newTestStrategy(t, true)

	oldObj := &testv1.Object{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "test-obj",
			Namespace:         "default",
			UID:               "old-uid",
			Generation:        1,
			CreationTimestamp: metav1.Time{Time: metav1.Now().Add(-1 * 60 * 1e9)},
			Annotations: map[string]string{
				constants.AnnotationCreatedBy: "original@example.com",
			},
		},
		Spec: testv1.ObjectSpec{PublicField: "value"},
	}

	newObj := &testv1.Object{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-obj",
			Namespace: "default",
			// No annotations set — PrepareForUpdate must copy created-by from old.
		},
		Spec: testv1.ObjectSpec{PublicField: "value"},
	}

	strategy.PrepareForUpdate(context.Background(), newObj, oldObj)

	got := newObj.GetAnnotations()[constants.AnnotationCreatedBy]
	if got != "original@example.com" {
		t.Errorf("created-by annotation = %q, want %q", got, "original@example.com")
	}
}
