package aggregated

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/go-logr/logr"
	testv1 "github.com/openshift-online/gecko/orlop/apis/private/test/v1"
	"github.com/openshift-online/gecko/orlop/pkg/apiserver/storage/memory"
	"k8s.io/apimachinery/pkg/api/errors"
	metainternalversion "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/registry/rest"
)

type storageTestEnv struct {
	scheme   *runtime.Scheme
	store    *memory.MemoryStore
	strategy *ResourceStrategy
	storage  *ResourceStorage
}

func setupStorageTest(t *testing.T) *storageTestEnv {
	t.Helper()

	scheme := newTestScheme(t)
	store := memory.NewMemoryStore("objects", scheme, testGVK)
	strategy := NewResourceStrategy(scheme, nil, true, testGVK, logr.Discard())
	rs := NewResourceStorage(store, strategy, testGVK, "objects", "object", scheme, logr.Discard(), nil)

	return &storageTestEnv{
		scheme:   scheme,
		store:    store,
		strategy: strategy,
		storage:  rs,
	}
}

func testCtx(namespace string) context.Context {
	return genericapirequest.WithNamespace(context.Background(), namespace)
}

func newTestObject(name, namespace string) *testv1.Object {
	return &testv1.Object{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: testv1.ObjectSpec{
			PublicField: "value1",
		},
	}
}

func TestResourceStorage_Create(t *testing.T) {
	env := setupStorageTest(t)
	ctx := testCtx("test-ns")
	obj := newTestObject("test-obj", "test-ns")

	result, err := env.storage.Create(ctx, obj, nil, &metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	created, ok := result.(*testv1.Object)
	if !ok {
		t.Fatalf("expected *testv1.Object, got %T", result)
	}

	if created.Name != "test-obj" {
		t.Errorf("expected name %q, got %q", "test-obj", created.Name)
	}
	if created.Namespace != "test-ns" {
		t.Errorf("expected namespace %q, got %q", "test-ns", created.Namespace)
	}
	if created.UID == "" {
		t.Error("expected UID to be set")
	}
	if created.Generation != 1 {
		t.Errorf("expected generation 1, got %d", created.Generation)
	}
	if created.ResourceVersion == "" {
		t.Error("expected resourceVersion to be set")
	}

	// Verify GVK is set on the returned object.
	gvk := created.GetObjectKind().GroupVersionKind()
	if gvk != testGVK {
		t.Errorf("expected GVK %v, got %v", testGVK, gvk)
	}

	// Verify object is retrievable from the store.
	got, err := env.storage.Get(ctx, "test-obj", &metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get after Create failed: %v", err)
	}
	gotObj := got.(*testv1.Object)
	if gotObj.Spec.PublicField != "value1" {
		t.Errorf("expected spec.publicField %q, got %q", "value1", gotObj.Spec.PublicField)
	}
}

func TestResourceStorage_Get(t *testing.T) {
	env := setupStorageTest(t)
	ctx := testCtx("test-ns")
	obj := newTestObject("get-test", "test-ns")
	obj.Spec.PublicField = "hello"

	if _, err := env.storage.Create(ctx, obj, nil, &metav1.CreateOptions{}); err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	result, err := env.storage.Get(ctx, "get-test", &metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	got := result.(*testv1.Object)
	if got.Name != "get-test" {
		t.Errorf("expected name %q, got %q", "get-test", got.Name)
	}
	if got.Spec.PublicField != "hello" {
		t.Errorf("expected spec.publicField %q, got %q", "hello", got.Spec.PublicField)
	}

	// Verify GVK is set on Get response.
	gvk := got.GetObjectKind().GroupVersionKind()
	if gvk != testGVK {
		t.Errorf("expected GVK %v, got %v", testGVK, gvk)
	}
}

func TestResourceStorage_Get_NotFound(t *testing.T) {
	env := setupStorageTest(t)
	ctx := testCtx("test-ns")

	_, err := env.storage.Get(ctx, "nonexistent", &metav1.GetOptions{})
	if err == nil {
		t.Fatal("expected error for nonexistent object")
	}
	if !errors.IsNotFound(err) {
		t.Errorf("expected NotFound error, got: %v", err)
	}
}

func TestResourceStorage_List(t *testing.T) {
	env := setupStorageTest(t)
	ctx := testCtx("test-ns")

	for i := 0; i < 3; i++ {
		obj := newTestObject(fmt.Sprintf("list-obj-%d", i), "test-ns")
		if _, err := env.storage.Create(ctx, obj, nil, &metav1.CreateOptions{}); err != nil {
			t.Fatalf("Create %d failed: %v", i, err)
		}
	}

	result, err := env.storage.List(ctx, &metainternalversion.ListOptions{})
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}

	list, ok := result.(*testv1.ObjectList)
	if !ok {
		t.Fatalf("expected *testv1.ObjectList, got %T", result)
	}
	if len(list.Items) != 3 {
		t.Errorf("expected 3 items, got %d", len(list.Items))
	}

	// Verify list GVK.
	listGVK := list.GetObjectKind().GroupVersionKind()
	expectedListGVK := testGVK.GroupVersion().WithKind("ObjectList")
	if listGVK != expectedListGVK {
		t.Errorf("expected list GVK %v, got %v", expectedListGVK, listGVK)
	}
}

func TestResourceStorage_List_FieldSelector(t *testing.T) {
	env := setupStorageTest(t)
	ctx := testCtx("test-ns")

	for i := 0; i < 3; i++ {
		obj := newTestObject(fmt.Sprintf("fs-obj-%d", i), "test-ns")
		if _, err := env.storage.Create(ctx, obj, nil, &metav1.CreateOptions{}); err != nil {
			t.Fatalf("Create %d failed: %v", i, err)
		}
	}

	// Filter by metadata.name using FieldSelector.
	selector := fields.OneTermEqualSelector("metadata.name", "fs-obj-1")
	result, err := env.storage.List(ctx, &metainternalversion.ListOptions{
		FieldSelector: selector,
	})
	if err != nil {
		t.Fatalf("List with FieldSelector failed: %v", err)
	}

	list := result.(*testv1.ObjectList)
	if len(list.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(list.Items))
	}
	if list.Items[0].Name != "fs-obj-1" {
		t.Errorf("expected name %q, got %q", "fs-obj-1", list.Items[0].Name)
	}
}

func TestResourceStorage_Delete_WithPropagationPolicy(t *testing.T) {
	env := setupStorageTest(t)
	ctx := testCtx("test-ns")
	obj := newTestObject("prop-test", "test-ns")

	if _, err := env.storage.Create(ctx, obj, nil, &metav1.CreateOptions{}); err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	orphan := metav1.DeletePropagationOrphan
	_, deleted, err := env.storage.Delete(ctx, "prop-test", nil, &metav1.DeleteOptions{
		PropagationPolicy: &orphan,
	})
	if err != nil {
		t.Fatalf("Delete with PropagationPolicy failed: %v", err)
	}
	if !deleted {
		t.Error("expected deleted=true for object without finalizers")
	}
}

func TestResourceStorage_Delete_WithGracePeriod(t *testing.T) {
	env := setupStorageTest(t)
	ctx := testCtx("test-ns")
	obj := newTestObject("grace-test", "test-ns")

	if _, err := env.storage.Create(ctx, obj, nil, &metav1.CreateOptions{}); err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	gracePeriod := int64(30)
	_, deleted, err := env.storage.Delete(ctx, "grace-test", nil, &metav1.DeleteOptions{
		GracePeriodSeconds: &gracePeriod,
	})
	if err != nil {
		t.Fatalf("Delete with GracePeriodSeconds failed: %v", err)
	}
	if !deleted {
		t.Error("expected deleted=true (grace period not supported, proceeds immediately)")
	}
}

func TestResourceStorage_Update(t *testing.T) {
	env := setupStorageTest(t)
	ctx := testCtx("test-ns")
	obj := newTestObject("update-test", "test-ns")

	created, err := env.storage.Create(ctx, obj, nil, &metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	createdObj := created.(*testv1.Object)
	origGeneration := createdObj.Generation

	// Build an UpdatedObjectInfo that modifies the spec.
	updater := rest.DefaultUpdatedObjectInfo(nil, func(ctx context.Context, newObj, oldObj runtime.Object) (runtime.Object, error) {
		o := oldObj.DeepCopyObject().(*testv1.Object)
		o.Spec.PublicField = "updated"
		return o, nil
	})

	result, _, err := env.storage.Update(ctx, "update-test", updater, nil, nil, false, &metav1.UpdateOptions{})
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	updated := result.(*testv1.Object)
	if updated.Spec.PublicField != "updated" {
		t.Errorf("expected spec.publicField %q, got %q", "updated", updated.Spec.PublicField)
	}
	if updated.Generation != origGeneration+1 {
		t.Errorf("expected generation %d, got %d", origGeneration+1, updated.Generation)
	}

	// Verify persisted.
	got, err := env.storage.Get(ctx, "update-test", &metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get after Update failed: %v", err)
	}
	gotObj := got.(*testv1.Object)
	if gotObj.Spec.PublicField != "updated" {
		t.Errorf("persisted spec.publicField = %q, want %q", gotObj.Spec.PublicField, "updated")
	}
}

func TestResourceStorage_Update_Conflict(t *testing.T) {
	env := setupStorageTest(t)
	ctx := testCtx("test-ns")
	obj := newTestObject("conflict-test", "test-ns")

	if _, err := env.storage.Create(ctx, obj, nil, &metav1.CreateOptions{}); err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Build updater that sets a wrong resourceVersion.
	updater := rest.DefaultUpdatedObjectInfo(nil, func(ctx context.Context, newObj, oldObj runtime.Object) (runtime.Object, error) {
		o := oldObj.DeepCopyObject().(*testv1.Object)
		o.ResourceVersion = "999999" // wrong version
		o.Spec.PublicField = "conflict"
		return o, nil
	})

	_, _, err := env.storage.Update(ctx, "conflict-test", updater, nil, nil, false, &metav1.UpdateOptions{})
	if err == nil {
		t.Fatal("expected conflict error")
	}
	if !errors.IsConflict(err) {
		t.Errorf("expected Conflict error, got: %v", err)
	}
}

func TestResourceStorage_Delete(t *testing.T) {
	env := setupStorageTest(t)
	ctx := testCtx("test-ns")
	obj := newTestObject("delete-test", "test-ns")

	if _, err := env.storage.Create(ctx, obj, nil, &metav1.CreateOptions{}); err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	_, deleted, err := env.storage.Delete(ctx, "delete-test", nil, &metav1.DeleteOptions{})
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	if !deleted {
		t.Error("expected deleted=true for object without finalizers")
	}

	_, err = env.storage.Get(ctx, "delete-test", &metav1.GetOptions{})
	if err == nil {
		t.Fatal("expected NotFound after Delete")
	}
	if !errors.IsNotFound(err) {
		t.Errorf("expected NotFound error, got: %v", err)
	}
}

func TestResourceStorage_Delete_WithFinalizers(t *testing.T) {
	env := setupStorageTest(t)
	ctx := testCtx("test-ns")
	obj := newTestObject("finalize-test", "test-ns")
	obj.Finalizers = []string{"test.io/cleanup"}

	if _, err := env.storage.Create(ctx, obj, nil, &metav1.CreateOptions{}); err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	result, deleted, err := env.storage.Delete(ctx, "finalize-test", nil, &metav1.DeleteOptions{})
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	if deleted {
		t.Error("expected deleted=false when finalizers present")
	}

	resultObj := result.(*testv1.Object)
	if resultObj.DeletionTimestamp == nil {
		t.Error("expected deletionTimestamp to be set")
	}

	// Object should still be retrievable.
	got, err := env.storage.Get(ctx, "finalize-test", &metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get after soft-delete failed: %v", err)
	}
	gotObj := got.(*testv1.Object)
	if gotObj.DeletionTimestamp == nil {
		t.Error("expected deletionTimestamp on persisted object")
	}
}

func TestResourceStorage_Watch(t *testing.T) {
	env := setupStorageTest(t)
	ctx := testCtx("test-ns")

	w, err := env.storage.Watch(ctx, &metainternalversion.ListOptions{})
	if err != nil {
		t.Fatalf("Watch failed: %v", err)
	}
	defer w.Stop()

	// Create an object to trigger an ADDED event.
	obj := newTestObject("watch-test", "test-ns")
	if _, err := env.storage.Create(ctx, obj, nil, &metav1.CreateOptions{}); err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	select {
	case event := <-w.ResultChan():
		if event.Type != watch.Added {
			t.Errorf("expected ADDED event, got %s", event.Type)
		}
		eventObj := event.Object.(*testv1.Object)
		if eventObj.Name != "watch-test" {
			t.Errorf("expected event object name %q, got %q", "watch-test", eventObj.Name)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for watch event")
	}
}

func TestResourceStorage_NamespaceScoped(t *testing.T) {
	env := setupStorageTest(t)
	if !env.storage.NamespaceScoped() {
		t.Error("expected NamespaceScoped=true")
	}

	// Create a non-namespaced strategy and storage.
	strategy := NewResourceStrategy(env.scheme, nil, false, testGVK, logr.Discard())
	rs := NewResourceStorage(env.store, strategy, testGVK, "objects", "object", env.scheme, logr.Discard(), nil)
	if rs.NamespaceScoped() {
		t.Error("expected NamespaceScoped=false")
	}
}

func TestResourceStorage_SingularName(t *testing.T) {
	env := setupStorageTest(t)
	if got := env.storage.GetSingularName(); got != "object" {
		t.Errorf("expected singular name %q, got %q", "object", got)
	}
}

func TestStatusStorage_Update(t *testing.T) {
	env := setupStorageTest(t)
	ctx := testCtx("test-ns")
	statusStorage := NewStatusStorage(env.storage)

	obj := newTestObject("status-test", "test-ns")
	obj.Spec.PublicField = "original-spec"

	created, err := env.storage.Create(ctx, obj, nil, &metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	createdObj := created.(*testv1.Object)

	// Build an UpdatedObjectInfo that modifies both spec and status.
	// Only status should be persisted by StatusStorage.
	updater := rest.DefaultUpdatedObjectInfo(nil, func(ctx context.Context, newObj, oldObj runtime.Object) (runtime.Object, error) {
		o := oldObj.DeepCopyObject().(*testv1.Object)
		o.Spec.PublicField = "changed-spec-should-be-ignored"
		o.Status.Conditions = []string{"Ready"}
		return o, nil
	})

	result, _, err := statusStorage.Update(ctx, "status-test", updater, nil, nil, false, &metav1.UpdateOptions{})
	if err != nil {
		t.Fatalf("StatusStorage Update failed: %v", err)
	}

	updated := result.(*testv1.Object)

	// Status should be updated.
	if len(updated.Status.Conditions) != 1 || updated.Status.Conditions[0] != "Ready" {
		t.Errorf("expected status.conditions=[Ready], got %v", updated.Status.Conditions)
	}

	// Spec should remain unchanged (status-only update).
	if updated.Spec.PublicField != createdObj.Spec.PublicField {
		t.Errorf("expected spec.publicField to remain %q, got %q", createdObj.Spec.PublicField, updated.Spec.PublicField)
	}

	// Verify persisted state.
	got, err := env.storage.Get(ctx, "status-test", &metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get after status update failed: %v", err)
	}
	gotObj := got.(*testv1.Object)
	if gotObj.Spec.PublicField != "original-spec" {
		t.Errorf("persisted spec.publicField = %q, want %q", gotObj.Spec.PublicField, "original-spec")
	}
	if len(gotObj.Status.Conditions) != 1 || gotObj.Status.Conditions[0] != "Ready" {
		t.Errorf("persisted status.conditions = %v, want [Ready]", gotObj.Status.Conditions)
	}
}

func TestResourceStorage_New_PanicsOnBadGVK(t *testing.T) {
	env := setupStorageTest(t)

	// Create a storage with a GVK that is not registered in the scheme.
	badGVK := schema.GroupVersionKind{Group: "bad.example.com", Version: "v1", Kind: "DoesNotExist"}
	rs := NewResourceStorage(env.store, env.strategy, badGVK, "doesnotexists", "doesnotexist", env.scheme, logr.Discard(), nil)

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected New() to panic on unregistered GVK")
		}
		msg, ok := r.(string)
		if !ok {
			t.Fatalf("expected panic value to be a string, got %T", r)
		}
		if msg == "" {
			t.Error("expected non-empty panic message")
		}
	}()
	rs.New()
}

func TestResourceStorage_NewList_PanicsOnBadGVK(t *testing.T) {
	env := setupStorageTest(t)

	// Create a storage with a GVK whose list kind is not registered.
	badGVK := schema.GroupVersionKind{Group: "bad.example.com", Version: "v1", Kind: "DoesNotExist"}
	rs := NewResourceStorage(env.store, env.strategy, badGVK, "doesnotexists", "doesnotexist", env.scheme, logr.Discard(), nil)

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected NewList() to panic on unregistered GVK")
		}
		msg, ok := r.(string)
		if !ok {
			t.Fatalf("expected panic value to be a string, got %T", r)
		}
		if msg == "" {
			t.Error("expected non-empty panic message")
		}
	}()
	rs.NewList()
}

func TestResourceStorage_Get_InternalErrorOnNonNotFound(t *testing.T) {
	// Use a MemoryStore with a context filter key to trigger a non-NotFound error.
	// When the context filter key is set but missing from context, Get returns a
	// non-NotFound error.
	scheme := newTestScheme(t)

	type ctxKey string
	filterKey := ctxKey("filter")
	store := memory.NewMemoryStore("objects", scheme, testGVK, memory.WithContextFilter(filterKey))
	strategy := NewResourceStrategy(scheme, nil, true, testGVK, logr.Discard())
	rs := NewResourceStorage(store, strategy, testGVK, "objects", "object", scheme, logr.Discard(), nil)

	// Use a context without the filter key set -- this causes the store to
	// return a non-NotFound error (context filter key not found in context).
	ctx := genericapirequest.WithNamespace(context.Background(), "test-ns")

	_, err := rs.Get(ctx, "any-name", &metav1.GetOptions{})
	if err == nil {
		t.Fatal("expected error when context filter key is missing")
	}
	if errors.IsNotFound(err) {
		t.Error("expected non-NotFound error (InternalError), got NotFound")
	}
	if !errors.IsInternalError(err) {
		t.Errorf("expected InternalError, got: %v", err)
	}
}

func TestResourceStorage_Update_FinalizerRemovalHardDelete(t *testing.T) {
	env := setupStorageTest(t)
	ctx := testCtx("test-ns")

	// Step 1: Create object with a finalizer.
	obj := newTestObject("finalizer-update-test", "test-ns")
	obj.Finalizers = []string{"test.io/cleanup"}

	if _, err := env.storage.Create(ctx, obj, nil, &metav1.CreateOptions{}); err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Step 2: Delete it (soft delete -- sets deletionTimestamp).
	result, deleted, err := env.storage.Delete(ctx, "finalizer-update-test", nil, &metav1.DeleteOptions{})
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	if deleted {
		t.Fatal("expected soft delete (deleted=false) when finalizers present")
	}
	softDeleted := result.(*testv1.Object)
	if softDeleted.DeletionTimestamp == nil {
		t.Fatal("expected deletionTimestamp to be set after soft delete")
	}

	// Object should still be retrievable.
	got, err := env.storage.Get(ctx, "finalizer-update-test", &metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get after soft delete failed: %v", err)
	}
	gotObj := got.(*testv1.Object)
	if gotObj.DeletionTimestamp == nil {
		t.Fatal("expected deletionTimestamp on persisted object")
	}

	// Step 3: Update removing the finalizer.
	updater := rest.DefaultUpdatedObjectInfo(nil, func(ctx context.Context, newObj, oldObj runtime.Object) (runtime.Object, error) {
		o := oldObj.DeepCopyObject().(*testv1.Object)
		o.Finalizers = nil // Remove all finalizers.
		return o, nil
	})

	updateResult, created, err := env.storage.Update(ctx, "finalizer-update-test", updater, nil, nil, false, &metav1.UpdateOptions{})
	if err != nil {
		t.Fatalf("Update (remove finalizer) failed: %v", err)
	}
	if created {
		t.Error("expected created=false after finalizer removal (object was updated, not created)")
	}
	updatedObj := updateResult.(*testv1.Object)
	if updatedObj.DeletionTimestamp == nil {
		t.Error("expected deletionTimestamp to still be present on returned object")
	}
	if len(updatedObj.GetFinalizers()) != 0 {
		t.Errorf("expected no finalizers on returned object, got %v", updatedObj.GetFinalizers())
	}

	// Step 4: Verify object is hard deleted (Get returns NotFound).
	_, err = env.storage.Get(ctx, "finalizer-update-test", &metav1.GetOptions{})
	if err == nil {
		t.Fatal("expected NotFound after hard delete via finalizer removal")
	}
	if !errors.IsNotFound(err) {
		t.Errorf("expected NotFound error, got: %v", err)
	}
}

func TestFieldSelectorToFilters_RejectsInequality(t *testing.T) {
	sel, err := fields.ParseSelector("metadata.name!=forbidden")
	if err != nil {
		t.Fatalf("failed to parse selector: %v", err)
	}
	_, err = fieldSelectorToFilters(sel)
	if err == nil {
		t.Fatal("expected error for != operator")
	}
}

func TestFieldSelectorToFilters_RejectsInvalidPath(t *testing.T) {
	for _, path := range []string{"'; DROP TABLE--", "a..b", ".leading", "123start"} {
		sel := fields.OneTermEqualSelector(path, "val")
		_, err := fieldSelectorToFilters(sel)
		if err == nil {
			t.Errorf("expected error for invalid field path %q", path)
		}
	}
}

func TestFieldSelectorToFilters_AcceptsValidPaths(t *testing.T) {
	for _, path := range []string{"metadata.name", "spec.clusterID", "status.phase"} {
		sel := fields.OneTermEqualSelector(path, "val")
		filters, err := fieldSelectorToFilters(sel)
		if err != nil {
			t.Errorf("unexpected error for valid path %q: %v", path, err)
		}
		if filters[path] != "val" {
			t.Errorf("expected filter[%q]=%q, got %q", path, "val", filters[path])
		}
	}
}

func TestStatusStorage_Update_ConflictOnStaleResourceVersion(t *testing.T) {
	env := setupStorageTest(t)
	ctx := testCtx("test-ns")
	statusStorage := NewStatusStorage(env.storage)

	obj := newTestObject("rv-conflict-test", "test-ns")
	if _, err := env.storage.Create(ctx, obj, nil, &metav1.CreateOptions{}); err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	updater := rest.DefaultUpdatedObjectInfo(nil, func(ctx context.Context, newObj, oldObj runtime.Object) (runtime.Object, error) {
		o := oldObj.DeepCopyObject().(*testv1.Object)
		o.ResourceVersion = "stale-version"
		o.Status.Conditions = []string{"Ready"}
		return o, nil
	})

	_, _, err := statusStorage.Update(ctx, "rv-conflict-test", updater, nil, nil, false, &metav1.UpdateOptions{})
	if err == nil {
		t.Fatal("expected conflict error for stale resourceVersion")
	}
	if !errors.IsConflict(err) {
		t.Errorf("expected Conflict error, got: %v", err)
	}
}

func TestResourceStorage_Delete_CallsValidateDelete(t *testing.T) {
	env := setupStorageTest(t)
	ctx := testCtx("test-ns")

	rejectDelete := func(ctx context.Context, obj runtime.Object) error {
		return errors.NewForbidden(env.storage.groupResource, "reject-test", fmt.Errorf("deletion blocked by policy"))
	}

	obj := newTestObject("reject-test", "test-ns")
	if _, err := env.storage.Create(ctx, obj, nil, &metav1.CreateOptions{}); err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	_, _, err := env.storage.Delete(ctx, "reject-test", rejectDelete, &metav1.DeleteOptions{})
	if err == nil {
		t.Fatal("expected error from deleteValidation")
	}

	// Object should still exist.
	_, err = env.storage.Get(ctx, "reject-test", &metav1.GetOptions{})
	if err != nil {
		t.Fatalf("object should still exist after rejected delete: %v", err)
	}
}
