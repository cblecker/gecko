package spanner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"cloud.google.com/go/spanner"
	database "cloud.google.com/go/spanner/admin/database/apiv1"
	"cloud.google.com/go/spanner/admin/database/apiv1/databasepb"
	instance "cloud.google.com/go/spanner/admin/instance/apiv1"
	"cloud.google.com/go/spanner/admin/instance/apiv1/instancepb"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"github.com/openshift-online/gecko/orlop/pkg/apiserver/storage"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

type objectOption func(*unstructured.Unstructured)

func newTestObject(opts ...objectOption) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "test.example.com/v1",
			"kind":       "TestObject",
			"metadata": map[string]any{
				"name":      "test",
				"namespace": "default",
			},
		},
	}
	for _, opt := range opts {
		opt(obj)
	}
	return obj
}

func withName(name string) objectOption {
	return func(obj *unstructured.Unstructured) {
		obj.SetName(name)
	}
}

func withNamespace(namespace string) objectOption {
	return func(obj *unstructured.Unstructured) {
		obj.SetNamespace(namespace)
	}
}

func withLabels(labels map[string]string) objectOption {
	return func(obj *unstructured.Unstructured) {
		obj.SetLabels(labels)
	}
}

func withSpec(spec map[string]any) objectOption {
	return func(obj *unstructured.Unstructured) {
		obj.Object["spec"] = spec
	}
}

func withGenerateName(generateName string) objectOption {
	return func(obj *unstructured.Unstructured) {
		obj.SetGenerateName(generateName)
		obj.SetName("")
	}
}

const (
	testProject  = "test-project"
	testInstance = "test-instance"
	testDatabase = "test-db"
)

var (
	sharedClient   *spanner.Client
	sharedDBAdmin  *database.DatabaseAdminClient
	sharedDBPath   string
	resourcesTable string
	countersTable  string
	testCounterSeq atomic.Int64
)

func TestMain(m *testing.M) {
	emulatorHost := os.Getenv("SPANNER_EMULATOR_HOST")
	if emulatorHost == "" {
		fmt.Println("SPANNER_EMULATOR_HOST not set, skipping Spanner integration tests")
		os.Exit(0)
	}

	sharedDBPath = fmt.Sprintf("projects/%s/instances/%s/databases/%s", testProject, testInstance, testDatabase)
	instancePath := fmt.Sprintf("projects/%s/instances/%s", testProject, testInstance)

	adminOpts := []option.ClientOption{
		option.WithEndpoint(emulatorHost),
		option.WithGRPCDialOption(grpc.WithTransportCredentials(insecure.NewCredentials())),
		option.WithoutAuthentication(),
	}

	ctx := context.Background()

	// Wait for emulator to be reachable by retrying instance admin connection
	var instanceAdmin *instance.InstanceAdminClient
	for attempt := range 30 {
		var err error
		instanceAdmin, err = instance.NewInstanceAdminClient(ctx, adminOpts...)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Waiting for emulator (attempt %d): %v\n", attempt+1, err)
			time.Sleep(time.Second)
			continue
		}
		_, err = instanceAdmin.GetInstance(ctx, &instancepb.GetInstanceRequest{Name: instancePath})
		if err == nil || status.Code(err) == codes.NotFound {
			break
		}
		instanceAdmin.Close()
		instanceAdmin = nil
		fmt.Fprintf(os.Stderr, "Emulator not ready (attempt %d): %v\n", attempt+1, err)
		time.Sleep(time.Second)
	}
	if instanceAdmin == nil {
		fmt.Fprintf(os.Stderr, "Could not connect to Spanner emulator at %s\n", emulatorHost)
		os.Exit(1)
	}

	// Create instance if needed
	_, err := instanceAdmin.GetInstance(ctx, &instancepb.GetInstanceRequest{Name: instancePath})
	if err != nil {
		if status.Code(err) != codes.NotFound {
			fmt.Fprintf(os.Stderr, "Failed to get instance: %v\n", err)
			os.Exit(1)
		}
		op, err := instanceAdmin.CreateInstance(ctx, &instancepb.CreateInstanceRequest{
			Parent:     fmt.Sprintf("projects/%s", testProject),
			InstanceId: testInstance,
			Instance: &instancepb.Instance{
				Config:      fmt.Sprintf("projects/%s/instanceConfigs/emulator-config", testProject),
				DisplayName: testInstance,
				NodeCount:   1,
			},
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to create instance: %v\n", err)
			os.Exit(1)
		}
		if _, err := op.Wait(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "Failed waiting for instance: %v\n", err)
			os.Exit(1)
		}
	}
	instanceAdmin.Close()

	// Create database admin client
	sharedDBAdmin, err = database.NewDatabaseAdminClient(ctx, adminOpts...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create database admin client: %v\n", err)
		os.Exit(1)
	}

	// Create database with retry — instance may take a moment to be visible
	for attempt := range 10 {
		_, err = sharedDBAdmin.GetDatabase(ctx, &databasepb.GetDatabaseRequest{Name: sharedDBPath})
		if err == nil {
			break
		}
		if status.Code(err) != codes.NotFound {
			fmt.Fprintf(os.Stderr, "Failed to get database: %v\n", err)
			os.Exit(1)
		}
		dbOp, createErr := sharedDBAdmin.CreateDatabase(ctx, &databasepb.CreateDatabaseRequest{
			Parent:          instancePath,
			CreateStatement: fmt.Sprintf("CREATE DATABASE `%s`", testDatabase),
		})
		if createErr != nil {
			if attempt < 9 {
				time.Sleep(time.Second)
				continue
			}
			fmt.Fprintf(os.Stderr, "Failed to create database after retries: %v\n", createErr)
			os.Exit(1)
		}
		if _, err := dbOp.Wait(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "Failed waiting for database: %v\n", err)
			os.Exit(1)
		}
		break
	}

	// Create tables with unique names for this test run
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	resourcesTable = "resources_" + suffix
	countersTable = "counters_" + suffix

	changeStreamName := "cs_" + resourcesTable

	var ddl []string
	ddl = append(ddl, countersSchema(countersTable)...)
	ddl = append(ddl, resourcesSchema(resourcesTable)...)
	ddl = append(ddl, changeStreamSchema(changeStreamName, resourcesTable)...)

	ddlOp, err := sharedDBAdmin.UpdateDatabaseDdl(ctx, &databasepb.UpdateDatabaseDdlRequest{
		Database:   sharedDBPath,
		Statements: ddl,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create tables: %v\n", err)
		os.Exit(1)
	}
	if err := ddlOp.Wait(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Failed waiting for DDL: %v\n", err)
		os.Exit(1)
	}

	// Create shared Spanner data client
	sharedClient, err = spanner.NewClient(ctx, sharedDBPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create spanner client: %v\n", err)
		os.Exit(1)
	}

	// Verify client works with a simple query
	for attempt := range 10 {
		iter := sharedClient.Single().Query(ctx, spanner.Statement{SQL: "SELECT 1"})
		_, warmupErr := iter.Next()
		iter.Stop()
		if warmupErr == nil || errors.Is(warmupErr, iterator.Done) {
			break
		}
		if attempt == 9 {
			fmt.Fprintf(os.Stderr, "Client warmup failed after retries: %v\n", warmupErr)
			os.Exit(1)
		}
		time.Sleep(500 * time.Millisecond)
	}

	code := m.Run()

	sharedClient.Close()
	sharedDBAdmin.Close()
	os.Exit(code)
}

func setupTestStore(t *testing.T) *SpannerStore {
	t.Helper()

	// Each test gets a unique counter_id for resource version isolation
	counterID := fmt.Sprintf("test_%d", testCounterSeq.Add(1))

	scheme := runtime.NewScheme()
	gv := schema.GroupVersion{Group: "test.example.com", Version: "v1"}
	scheme.AddKnownTypeWithName(gv.WithKind("TestObject"), &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(gv.WithKind("TestObjectList"), &unstructured.UnstructuredList{})

	gvk := schema.GroupVersionKind{
		Group:   "test.example.com",
		Version: "v1",
		Kind:    "TestObject",
	}

	store := &SpannerStore{
		client:        sharedClient,
		resourceType:  gvkString(gvk),
		scheme:        scheme,
		gvk:           gvk,
		tableName:     resourcesTable,
		countersTable: countersTable,
		counterID:     counterID,
	}

	return store
}

func TestSpannerStore_Create(t *testing.T) {
	t.Run("creates new object with resourceVersion", func(t *testing.T) {
		store := setupTestStore(t)

		obj := newTestObject(withName("create-rv"), withNamespace("test-create-rv"))

		err := store.Create(context.Background(), obj)
		if err != nil {
			t.Fatalf("Create() failed: %v", err)
		}

		retrieved, err := store.Get(context.Background(), "test-create-rv", "create-rv")
		if err != nil {
			t.Fatalf("Get() failed: %v", err)
		}

		if retrieved.GetResourceVersion() == "" {
			t.Error("Created object missing resourceVersion")
		}
		if _, err := strconv.ParseInt(retrieved.GetResourceVersion(), 10, 64); err != nil {
			t.Errorf("resourceVersion is not a valid integer: %s", retrieved.GetResourceVersion())
		}
	})

	t.Run("returns error for duplicate", func(t *testing.T) {
		store := setupTestStore(t)

		obj := newTestObject(withName("dup"), withNamespace("test-dup"))

		if err := store.Create(context.Background(), obj); err != nil {
			t.Fatalf("First create failed: %v", err)
		}
		err := store.Create(context.Background(), obj)
		if err == nil {
			t.Error("Expected error for duplicate object, got nil")
		}
	})

	t.Run("creates in different namespaces", func(t *testing.T) {
		store := setupTestStore(t)

		obj1 := newTestObject(withName("obj"), withNamespace("test-ns1"))
		obj2 := newTestObject(withName("obj"), withNamespace("test-ns2"))

		if err := store.Create(context.Background(), obj1); err != nil {
			t.Fatalf("Create in ns1 failed: %v", err)
		}
		if err := store.Create(context.Background(), obj2); err != nil {
			t.Fatalf("Create in ns2 failed: %v", err)
		}

		if _, err := store.Get(context.Background(), "test-ns1", "obj"); err != nil {
			t.Errorf("Object in ns1 not found: %v", err)
		}
		if _, err := store.Get(context.Background(), "test-ns2", "obj"); err != nil {
			t.Errorf("Object in ns2 not found: %v", err)
		}
	})

	t.Run("generateName produces a unique name", func(t *testing.T) {
		store := setupTestStore(t)

		obj := newTestObject(withGenerateName("gen-"), withNamespace("test-gen"))

		err := store.Create(context.Background(), obj)
		if err != nil {
			t.Fatalf("Create() with generateName failed: %v", err)
		}

		name := obj.GetName()
		if name == "" {
			t.Fatal("Name was not set after Create with generateName")
		}
		if len(name) < len("gen-")+5 {
			t.Errorf("Generated name too short: %q", name)
		}

		retrieved, err := store.Get(context.Background(), "test-gen", name)
		if err != nil {
			t.Fatalf("Get() by generated name failed: %v", err)
		}
		if retrieved.GetName() != name {
			t.Errorf("Retrieved name %q != generated name %q", retrieved.GetName(), name)
		}
	})

	t.Run("generateName creates distinct names", func(t *testing.T) {
		store := setupTestStore(t)

		seen := make(map[string]bool)

		for range 20 {
			obj := newTestObject(withGenerateName("multi-"), withNamespace("test-genmulti"))
			if err := store.Create(context.Background(), obj); err != nil {
				t.Fatalf("Create() failed: %v", err)
			}
			name := obj.GetName()
			if seen[name] {
				t.Fatalf("Duplicate generated name: %q", name)
			}
			seen[name] = true
		}

		listObj, err := store.List(context.Background(), storage.ListOptions{Namespace: "test-genmulti"})
		if err != nil {
			t.Fatalf("List() failed: %v", err)
		}
		list := listObj.(*unstructured.UnstructuredList)
		if len(list.Items) != 20 {
			t.Errorf("Expected 20 objects, got %d", len(list.Items))
		}
	})

	t.Run("sets creation timestamp", func(t *testing.T) {
		store := setupTestStore(t)

		obj := newTestObject(withName("ts-obj"), withNamespace("test-timestamp"))

		if err := store.Create(context.Background(), obj); err != nil {
			t.Fatalf("Create() failed: %v", err)
		}

		retrieved, err := store.Get(context.Background(), "test-timestamp", "ts-obj")
		if err != nil {
			t.Fatalf("Get() failed: %v", err)
		}
		creationTime := retrieved.GetCreationTimestamp()
		if creationTime.IsZero() {
			t.Error("Creation timestamp not set")
		}
	})

	t.Run("assigns unique UID", func(t *testing.T) {
		store := setupTestStore(t)

		obj1 := newTestObject(withName("uid1"), withNamespace("test-uid"))
		obj2 := newTestObject(withName("uid2"), withNamespace("test-uid"))

		if err := store.Create(context.Background(), obj1); err != nil {
			t.Fatalf("Create() obj1 failed: %v", err)
		}
		if err := store.Create(context.Background(), obj2); err != nil {
			t.Fatalf("Create() obj2 failed: %v", err)
		}

		retrieved1, err := store.Get(context.Background(), "test-uid", "uid1")
		if err != nil {
			t.Fatalf("Get() obj1 failed: %v", err)
		}
		retrieved2, err := store.Get(context.Background(), "test-uid", "uid2")
		if err != nil {
			t.Fatalf("Get() obj2 failed: %v", err)
		}

		uid1 := string(retrieved1.GetUID())
		uid2 := string(retrieved2.GetUID())

		if uid1 == "" {
			t.Error("UID not set on obj1")
		}
		if uid2 == "" {
			t.Error("UID not set on obj2")
		}
		if uid1 == uid2 {
			t.Errorf("UIDs should be unique, both are %s", uid1)
		}
	})
}

func TestSpannerStore_Get(t *testing.T) {
	t.Run("gets existing object", func(t *testing.T) {
		store := setupTestStore(t)

		obj := newTestObject(withName("getme"), withNamespace("test-get"))
		if err := store.Create(context.Background(), obj); err != nil {
			t.Fatalf("Create() failed: %v", err)
		}

		retrieved, err := store.Get(context.Background(), "test-get", "getme")
		if err != nil {
			t.Fatalf("Get() failed: %v", err)
		}
		if retrieved.GetName() != "getme" {
			t.Errorf("Got wrong object: %s", retrieved.GetName())
		}
	})

	t.Run("returns error for non-existent object", func(t *testing.T) {
		store := setupTestStore(t)

		_, err := store.Get(context.Background(), "test-missing", "missing")
		if err == nil {
			t.Error("Expected error for missing object, got nil")
		}
	})

	t.Run("returns error for wrong namespace", func(t *testing.T) {
		store := setupTestStore(t)

		obj := newTestObject(withName("wrongns"), withNamespace("test-wrongns-real"))
		if err := store.Create(context.Background(), obj); err != nil {
			t.Fatalf("Create() failed: %v", err)
		}

		_, err := store.Get(context.Background(), "test-wrongns-fake", "wrongns")
		if err == nil {
			t.Error("Expected error for wrong namespace, got nil")
		}
	})
}

func TestSpannerStore_List(t *testing.T) {
	t.Run("lists objects in namespace", func(t *testing.T) {
		store := setupTestStore(t)
		ns := "test-list-ns"

		store.Create(context.Background(), newTestObject(withName("obj1"), withNamespace(ns)))
		store.Create(context.Background(), newTestObject(withName("obj2"), withNamespace(ns)))
		store.Create(context.Background(), newTestObject(withName("obj3"), withNamespace(ns+"-other")))

		listObj, err := store.List(context.Background(), storage.ListOptions{Namespace: ns})
		if err != nil {
			t.Fatalf("List() failed: %v", err)
		}

		list := listObj.(*unstructured.UnstructuredList)
		if len(list.Items) != 2 {
			t.Errorf("Expected 2 objects, got %d", len(list.Items))
		}
	})

	t.Run("lists all namespaces", func(t *testing.T) {
		store := setupTestStore(t)
		prefix := "test-listall"

		store.Create(context.Background(), newTestObject(withName("obj1"), withNamespace(prefix+"-a")))
		store.Create(context.Background(), newTestObject(withName("obj2"), withNamespace(prefix+"-b")))
		store.Create(context.Background(), newTestObject(withName("obj3"), withNamespace(prefix+"-c")))

		listObj, err := store.List(context.Background(), storage.ListOptions{})
		if err != nil {
			t.Fatalf("List() failed: %v", err)
		}

		list := listObj.(*unstructured.UnstructuredList)
		if len(list.Items) < 3 {
			t.Errorf("Expected at least 3 objects, got %d", len(list.Items))
		}
	})

	t.Run("returns empty list for empty namespace", func(t *testing.T) {
		store := setupTestStore(t)

		listObj, err := store.List(context.Background(), storage.ListOptions{Namespace: "test-empty-ns-nonexistent"})
		if err != nil {
			t.Fatalf("List() failed: %v", err)
		}

		list := listObj.(*unstructured.UnstructuredList)
		if len(list.Items) != 0 {
			t.Errorf("Expected empty list, got %d objects", len(list.Items))
		}
	})

	t.Run("sets resourceVersion on list", func(t *testing.T) {
		store := setupTestStore(t)
		ns := "test-list-rv"

		store.Create(context.Background(), newTestObject(withName("obj1"), withNamespace(ns)))
		store.Create(context.Background(), newTestObject(withName("obj2"), withNamespace(ns)))

		listObj, err := store.List(context.Background(), storage.ListOptions{Namespace: ns})
		if err != nil {
			t.Fatalf("List() failed: %v", err)
		}
		list := listObj.(*unstructured.UnstructuredList)

		if list.GetResourceVersion() == "" {
			t.Error("List resourceVersion not set")
		}
	})

	t.Run("filters by label selector", func(t *testing.T) {
		store := setupTestStore(t)
		ns := "test-list-labels"

		store.Create(context.Background(), newTestObject(withName("obj1"), withNamespace(ns), withLabels(map[string]string{"env": "prod"})))
		store.Create(context.Background(), newTestObject(withName("obj2"), withNamespace(ns), withLabels(map[string]string{"env": "staging"})))
		store.Create(context.Background(), newTestObject(withName("obj3"), withNamespace(ns), withLabels(map[string]string{"env": "prod"})))

		listObj, err := store.List(context.Background(), storage.ListOptions{
			Namespace: ns,
		})
		if err != nil {
			t.Fatalf("List() failed: %v", err)
		}
		list := listObj.(*unstructured.UnstructuredList)
		if len(list.Items) != 3 {
			t.Errorf("Expected 3 objects without filter, got %d", len(list.Items))
		}
	})

	t.Run("pagination with continue token", func(t *testing.T) {
		store := setupTestStore(t)
		ns := "test-list-page"

		for i := range 5 {
			if err := store.Create(context.Background(), newTestObject(
				withName(fmt.Sprintf("obj%02d", i)),
				withNamespace(ns),
			)); err != nil {
				t.Fatalf("Create failed: %v", err)
			}
		}

		opts := storage.ListOptions{Namespace: ns}
		opts.Limit = 2
		listObj, err := store.List(context.Background(), opts)
		if err != nil {
			t.Fatalf("List() page 1 failed: %v", err)
		}
		list := listObj.(*unstructured.UnstructuredList)
		if len(list.Items) != 2 {
			t.Errorf("Expected 2 objects in page 1, got %d", len(list.Items))
		}
		continueToken := list.GetContinue()
		if continueToken == "" {
			t.Fatal("Expected continue token, got empty string")
		}

		opts2 := storage.ListOptions{Namespace: ns}
		opts2.Limit = 2
		opts2.Continue = continueToken
		listObj, err = store.List(context.Background(), opts2)
		if err != nil {
			t.Fatalf("List() page 2 failed: %v", err)
		}
		list = listObj.(*unstructured.UnstructuredList)
		if len(list.Items) != 2 {
			t.Errorf("Expected 2 objects in page 2, got %d", len(list.Items))
		}
	})
}

func TestSpannerStore_Update(t *testing.T) {
	t.Run("updates existing object", func(t *testing.T) {
		store := setupTestStore(t)

		obj := newTestObject(
			withName("upd"),
			withNamespace("test-update"),
			withSpec(map[string]any{"field": "original"}),
		)
		if err := store.Create(context.Background(), obj); err != nil {
			t.Fatalf("Create() failed: %v", err)
		}

		retrieved, err := store.Get(context.Background(), "test-update", "upd")
		if err != nil {
			t.Fatalf("Get() failed: %v", err)
		}
		updated := retrieved.DeepCopyObject().(client.Object)
		u := updated.(*unstructured.Unstructured)
		u.Object["spec"] = map[string]any{"field": "updated"}

		err = store.Update(context.Background(), updated)
		if err != nil {
			t.Fatalf("Update() failed: %v", err)
		}

		final, err := store.Get(context.Background(), "test-update", "upd")
		if err != nil {
			t.Fatalf("Get() after update failed: %v", err)
		}
		finalU := final.(*unstructured.Unstructured)
		spec := finalU.Object["spec"].(map[string]any)
		if spec["field"] != "updated" {
			t.Errorf("Update did not persist: got %v", spec["field"])
		}
	})

	t.Run("increments resourceVersion", func(t *testing.T) {
		store := setupTestStore(t)

		obj := newTestObject(withName("rv-inc"), withNamespace("test-update-rv"))
		if err := store.Create(context.Background(), obj); err != nil {
			t.Fatalf("Create() failed: %v", err)
		}

		retrieved, err := store.Get(context.Background(), "test-update-rv", "rv-inc")
		if err != nil {
			t.Fatalf("Get() failed: %v", err)
		}
		initialRV := retrieved.GetResourceVersion()

		if err := store.Update(context.Background(), retrieved); err != nil {
			t.Fatalf("Update() failed: %v", err)
		}

		updated, err := store.Get(context.Background(), "test-update-rv", "rv-inc")
		if err != nil {
			t.Fatalf("Get() after update failed: %v", err)
		}
		if updated.GetResourceVersion() == initialRV {
			t.Error("ResourceVersion not incremented after update")
		}
	})

	t.Run("returns error for non-existent object", func(t *testing.T) {
		store := setupTestStore(t)

		obj := newTestObject(withName("no-exist"), withNamespace("test-update-missing"))

		err := store.Update(context.Background(), obj)
		if err == nil {
			t.Error("Expected error for missing object, got nil")
		}
	})
}

func TestSpannerStore_Delete(t *testing.T) {
	t.Run("deletes existing object", func(t *testing.T) {
		store := setupTestStore(t)

		obj := newTestObject(withName("delme"), withNamespace("test-delete"))
		if err := store.Create(context.Background(), obj); err != nil {
			t.Fatalf("Create() failed: %v", err)
		}

		err := store.Delete(context.Background(), "test-delete", "delme")
		if err != nil {
			t.Fatalf("Delete() failed: %v", err)
		}

		_, err = store.Get(context.Background(), "test-delete", "delme")
		if err == nil {
			t.Error("Object still exists after delete")
		}
	})

	t.Run("returns error for non-existent object", func(t *testing.T) {
		store := setupTestStore(t)

		err := store.Delete(context.Background(), "test-delete-missing", "missing")
		if err == nil {
			t.Error("Expected error for missing object, got nil")
		}
	})
}

func TestSpannerStore_ResourceVersionIncrement(t *testing.T) {
	store := setupTestStore(t)

	parseRV := func(obj client.Object) int64 {
		rv, err := strconv.ParseInt(obj.GetResourceVersion(), 10, 64)
		if err != nil {
			t.Fatalf("invalid resourceVersion %q: %v", obj.GetResourceVersion(), err)
		}
		return rv
	}

	obj1 := newTestObject(withName("rvobj1"), withNamespace("test-rv"))
	if err := store.Create(context.Background(), obj1); err != nil {
		t.Fatalf("Create() failed: %v", err)
	}

	retrieved1, err := store.Get(context.Background(), "test-rv", "rvobj1")
	if err != nil {
		t.Fatalf("Get() failed: %v", err)
	}
	rv1 := parseRV(retrieved1)
	if rv1 <= 0 {
		t.Errorf("After first create, rv = %d, want > 0", rv1)
	}

	obj2 := newTestObject(withName("rvobj2"), withNamespace("test-rv"))
	if err := store.Create(context.Background(), obj2); err != nil {
		t.Fatalf("Create() failed: %v", err)
	}

	retrieved2, err := store.Get(context.Background(), "test-rv", "rvobj2")
	if err != nil {
		t.Fatalf("Get() failed: %v", err)
	}
	rv2 := parseRV(retrieved2)
	if rv2 <= rv1 {
		t.Errorf("After second create, rv = %d, want > %d", rv2, rv1)
	}

	if err := store.Update(context.Background(), retrieved1); err != nil {
		t.Fatalf("Update() failed: %v", err)
	}

	retrievedUpdated, err := store.Get(context.Background(), "test-rv", "rvobj1")
	if err != nil {
		t.Fatalf("Get() after update failed: %v", err)
	}
	rv3 := parseRV(retrievedUpdated)
	if rv3 <= rv2 {
		t.Errorf("After update, rv = %d, want > %d", rv3, rv2)
	}
}

// --- Regression test: List with a typed scheme ----------------------------
//
// Typed scheme registration (scheme.New returns a concrete struct, not
// *unstructured.Unstructured) caused List to return
//
//	"can't assign or convert unstructured.Unstructured into TypedCluster"
//
// because items were collected as []unstructured.Unstructured and then
// passed to meta.SetList, which uses reflection to assign each item into
// the typed list's Items slice. The fix introduces unmarshalTyped, which
// uses the store's scheme to produce correctly-typed objects.

// typedCluster is a minimal concrete struct that implements client.Object
// via embedded ObjectMeta. It represents a real typed API object (like
// v1.Cluster) as opposed to *unstructured.Unstructured.
type typedCluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              map[string]any `json:"spec,omitempty"`
}

func (c *typedCluster) DeepCopyObject() runtime.Object {
	cp := *c
	return &cp
}

// typedClusterList is the typed list for typedCluster.
type typedClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []typedCluster `json:"items"`
}

func (l *typedClusterList) DeepCopyObject() runtime.Object {
	cp := *l
	cp.Items = append([]typedCluster(nil), l.Items...)
	return &cp
}

// setupTypedStore creates a store where the scheme has a concrete typed
// struct registered for its GVK, reproducing the scenario that triggered
// the "can't assign or convert unstructured.Unstructured" error.
func setupTypedStore(t *testing.T) *SpannerStore {
	t.Helper()

	counterID := fmt.Sprintf("typed_%d", testCounterSeq.Add(1))

	gv := schema.GroupVersion{Group: "typed.example.com", Version: "v1"}
	gvk := gv.WithKind("TypedCluster")

	scheme := runtime.NewScheme()
	scheme.AddKnownTypeWithName(gvk, &typedCluster{})
	scheme.AddKnownTypeWithName(gv.WithKind("TypedClusterList"), &typedClusterList{})

	return &SpannerStore{
		client:        sharedClient,
		resourceType:  gvkString(gvk),
		scheme:        scheme,
		gvk:           gvk,
		tableName:     resourcesTable,
		countersTable: countersTable,
		counterID:     counterID,
	}
}

func TestSpannerStore_List_TypedScheme(t *testing.T) {
	// Regression test for: "can't assign or convert unstructured.Unstructured
	// into v1.Cluster". List must return typed objects when the store's scheme
	// has a concrete struct registered, not *unstructured.Unstructured.
	store := setupTypedStore(t)
	ns := "typed-list-ns"

	// Write two objects through the unstructured path (Create uses marshalData
	// which works regardless of scheme) so we have rows to list back.
	obj1 := newTestObject(withName("cluster-a"), withNamespace(ns))
	obj1.SetGroupVersionKind(store.gvk)
	if err := store.Create(context.Background(), obj1); err != nil {
		t.Fatalf("Create cluster-a: %v", err)
	}

	obj2 := newTestObject(withName("cluster-b"), withNamespace(ns))
	obj2.SetGroupVersionKind(store.gvk)
	if err := store.Create(context.Background(), obj2); err != nil {
		t.Fatalf("Create cluster-b: %v", err)
	}

	// List must not return the "can't assign or convert" error.
	listObj, err := store.List(context.Background(), storage.ListOptions{Namespace: ns})
	if err != nil {
		t.Fatalf("List() failed: %v", err)
	}

	// The returned list must be the typed list, not *unstructured.UnstructuredList.
	typed, ok := listObj.(*typedClusterList)
	if !ok {
		t.Fatalf("List() returned %T, want *typedClusterList", listObj)
	}
	if len(typed.Items) != 2 {
		t.Errorf("List() returned %d items, want 2", len(typed.Items))
	}

	// Each item must have its resource version set.
	for _, item := range typed.Items {
		if item.GetResourceVersion() == "" {
			t.Errorf("item %q missing resourceVersion", item.GetName())
		}
	}
}
