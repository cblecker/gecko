package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/go-logr/logr"

	"github.com/openshift-online/gecko/orlop/pkg/apiserver"
	"github.com/openshift-online/gecko/orlop/pkg/apiserver/constants"
	"github.com/openshift-online/gecko/orlop/pkg/apiserver/storage"
	"github.com/openshift-online/gecko/orlop/pkg/apiserver/storage/memory"
	privatev1 "github.com/openshift-online/gecko/platform-api/api/private/v1"
	"github.com/openshift-online/gecko/platform-api/pkg/authn"
	"github.com/openshift-online/gecko/platform-api/pkg/authz"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	runtimeschema "k8s.io/apimachinery/pkg/runtime/schema"
)

type publicAuthorizationRequester func(method, path string, body []byte, contentType string) (int, string)

//nolint:kubeapilinter // Test-only HTTP harness, not a serialized API type.
type publicAuthorizationTestServer struct {
	apiPrefix string
	request   publicAuthorizationRequester
}

func TestPublicAPIObjectStateAuthorization(t *testing.T) {
	server := newPublicAuthorizationTestServer(t)
	t.Run("object-state get and not found", func(t *testing.T) {
		testObjectStateGet(t, server)
	})
	t.Run("create update patch and delete use effective rolebinding state", func(t *testing.T) {
		testRoleBindingObjectState(t, server)
	})
}

func newPublicAuthorizationTestServer(t *testing.T) publicAuthorizationTestServer {
	t.Helper()
	privatePort := freePublicAPITestPort(t)
	publicPort := freePublicAPITestPort(t)
	privateScheme := getPrivateScheme()
	logger := logr.Discard()
	backingStores := map[runtimeschema.GroupVersionKind]storage.ResourceStore{}
	storageFactory := func(resourceType string, scheme *runtime.Scheme, gvk runtimeschema.GroupVersionKind) (storage.ResourceStore, error) {
		if store, found := backingStores[gvk]; found {
			return store, nil
		}
		store := memory.NewMemoryStore(resourceType, scheme, gvk)
		backingStores[gvk] = store
		return store, nil
	}

	stores, err := authz.NewStores(storageFactory, privateScheme)
	if err != nil {
		t.Fatal(err)
	}
	seedPlatformRole(t, stores, "service-admin", []string{
		"rolebinding.create", "rolebinding.get", "rolebinding.update", "rolebinding.delete", "rolebinding.list",
	})
	seedPlatformRole(t, stores, "cluster-viewer", []string{"cluster.get", "cluster.list"})
	serviceAdminCondition := `context.spec.subject == "bob@example.com"`
	clusterRegionCondition := `context.spec.platform.gcp.region == "us-east1"`
	seedRoleBinding(t, stores, "project-a", "alice-service-admin", "alice@example.com", "service-admin", &serviceAdminCondition)
	seedRoleBinding(t, stores, "project-a", "alice-cluster-viewer", "alice@example.com", "cluster-viewer", &clusterRegionCondition)
	seedRoleBinding(t, stores, "project-a", "existing-eve", "eve@example.com", "cluster-viewer", nil)

	clusterGVK := privatev1.GroupVersion.WithKind("Cluster")
	clusterStore, err := storageFactory(apiserver.GroupKindResourceType(clusterGVK.GroupKind()), privateScheme, clusterGVK)
	if err != nil {
		t.Fatal(err)
	}
	for _, cluster := range []*privatev1.Cluster{
		testCluster("project-a", "east", "us-east1"),
		testCluster("project-a", "west", "us-west1"),
		testCluster("project-a", "east-b", "us-east1"),
	} {
		if err := clusterStore.Create(context.Background(), cluster); err != nil {
			t.Fatal(err)
		}
	}

	authorizer, err := authz.NewAuthorizer(context.Background(), stores, logger)
	if err != nil {
		t.Fatal(err)
	}
	privatev1.SetValidatorDeps(privatev1.ValidatorDeps{
		RoleExists:         stores.RoleExists,
		PlatformRoleExists: stores.PlatformRoleExists,
	})
	t.Cleanup(func() { privatev1.SetValidatorDeps(privatev1.ValidatorDeps{}) })

	publicResources := getPublicResources()
	server, err := apiserver.New(apiserver.Options{
		Address: "127.0.0.1",
		Private: apiserver.PrivateAPIOptions{
			Port:        privatePort,
			Scheme:      privateScheme,
			Resources:   getPrivateResources(),
			DisableAuth: true,
		},
		Public: apiserver.PublicAPIOptions{
			Enable:    true,
			Address:   "127.0.0.1",
			Port:      publicPort,
			Scheme:    getPublicScheme(),
			Resources: publicResources,
			Middleware: []func(http.Handler) http.Handler{
				authn.Middleware(authn.Config{}),
				authz.Middleware(authorizer, logger, publicResources),
			},
		},
		StorageFactory: storageFactory,
		Logger:         logger,
	})
	if err != nil {
		t.Fatal(err)
	}

	runDone := make(chan struct{})
	var runErr error
	go func() {
		runErr = server.Run()
		close(runDone)
	}()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			t.Errorf("shutdown API server: %v", err)
		}
		select {
		case <-runDone:
			if runErr != nil {
				t.Errorf("API server stopped with error: %v", runErr)
			}
		case <-ctx.Done():
			t.Errorf("API server did not stop: %v", ctx.Err())
		}
	})

	baseURL := fmt.Sprintf("http://127.0.0.1:%d", publicPort)
	client := &http.Client{Timeout: 3 * time.Second}
	waitForPublicAPITestServer(t, client, baseURL+"/apis", runDone, &runErr)
	return publicAuthorizationTestServer{
		apiPrefix: "/apis/gcp.managed.openshift.io/v1",
		request:   publicAuthorizationRequest(t, client, baseURL),
	}
}

func testObjectStateGet(t *testing.T, server publicAuthorizationTestServer) {
	t.Helper()
	if status, body := server.request(http.MethodGet, server.apiPrefix+"/namespaces/project-a/clusters/east", nil, ""); status != http.StatusOK {
		t.Fatalf("allowed get = %d, want 200: %s", status, body)
	}
	if status, body := server.request(http.MethodGet, server.apiPrefix+"/namespaces/project-a/clusters/west", nil, ""); status != http.StatusForbidden {
		t.Fatalf("denied get = %d, want 403: %s", status, body)
	}
	if status, body := server.request(http.MethodGet, server.apiPrefix+"/namespaces/project-a/clusters/missing", nil, ""); status != http.StatusNotFound {
		t.Fatalf("missing get = %d, want 404: %s", status, body)
	}
}

func testRoleBindingObjectState(t *testing.T, server publicAuthorizationTestServer) {
	t.Helper()
	roleRef := `{"kind":"PlatformRole","name":"cluster-viewer","apiGroup":"gcp.managed.openshift.io"}`
	roleBindingPath := server.apiPrefix + "/namespaces/project-a/rolebindings"
	assertAuthorizationStatus(t, server.request, http.MethodPost, roleBindingPath,
		[]byte(`{"metadata":{"name":"allowed-binding"},"spec":{"subject":"bob@example.com","roleRef":`+roleRef+`}}`), constants.ContentTypeJSON, http.StatusCreated, "allowed create")
	assertAuthorizationStatus(t, server.request, http.MethodPost, roleBindingPath,
		[]byte(`{"metadata":{"name":"denied-binding"},"spec":{"subject":"eve@example.com","roleRef":`+roleRef+`}}`), constants.ContentTypeJSON, http.StatusForbidden, "denied create")
	assertAuthorizationStatus(t, server.request, http.MethodPost, roleBindingPath,
		[]byte(`{"metadata":{"name":"invalid-condition"},"spec":{"subject":"bob@example.com","roleRef":`+roleRef+`,"condition":"context.spec =="}}`), constants.ContentTypeJSON, http.StatusBadRequest, "invalid condition create")
	assertAuthorizationStatus(t, server.request, http.MethodPost, roleBindingPath,
		[]byte(`{"metadata":{"name":"namespace-reference"},"spec":{"subject":"bob@example.com","roleRef":`+roleRef+`,"condition":"resource in Namespace::\"other\""}}`), constants.ContentTypeJSON, http.StatusBadRequest, "Namespace entity condition create")

	existingPath := roleBindingPath + "/existing-eve"
	assertAuthorizationStatus(t, server.request, http.MethodGet, existingPath, nil, "", http.StatusForbidden, "stored disallowed get")
	assertAuthorizationStatus(t, server.request, http.MethodPut, existingPath,
		[]byte(`{"metadata":{"name":"existing-eve"},"spec":{"subject":"bob@example.com","roleRef":`+roleRef+`}}`), constants.ContentTypeJSON, http.StatusOK, "allowed replacement update")
	assertAuthorizationStatus(t, server.request, http.MethodPatch, existingPath,
		[]byte(`{"spec":{"subject":"eve@example.com"}}`), constants.ContentTypeMergePatch, http.StatusForbidden, "post-patch denied update")
	assertAuthorizationStatus(t, server.request, http.MethodDelete, existingPath, nil, "", http.StatusOK, "allowed delete")
	assertAuthorizationStatus(t, server.request, http.MethodGet, existingPath, nil, "", http.StatusNotFound, "missing rolebinding get")
}

func assertAuthorizationStatus(t *testing.T, request publicAuthorizationRequester, method, path string, body []byte, contentType string, wantStatus int, operation string) {
	t.Helper()
	if status, responseBody := request(method, path, body, contentType); status != wantStatus {
		t.Fatalf("%s = %d, want %d: %s", operation, status, wantStatus, responseBody)
	}
}

func publicAuthorizationRequest(t *testing.T, client *http.Client, baseURL string) func(string, string, []byte, string) (int, string) {
	t.Helper()
	userInfo := platformAPITestUserInfo(t)
	return func(method, path string, body []byte, contentType string) (int, string) {
		t.Helper()
		req, err := http.NewRequestWithContext(context.Background(), method, baseURL+path, bytes.NewReader(body))
		if err != nil {
			t.Fatalf("create request %s %s: %v", method, path, err)
		}
		req.Header.Set(constants.HeaderEndpointAPIUserInfo, userInfo)
		if contentType != "" {
			req.Header.Set(constants.HeaderContentType, contentType)
		}
		response, err := client.Do(req)
		if err != nil {
			t.Fatalf("request %s %s: %v", method, path, err)
		}
		defer response.Body.Close()
		responseBody, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatalf("read response %s %s: %v", method, path, err)
		}
		return response.StatusCode, string(responseBody)
	}
}

func seedPlatformRole(t *testing.T, stores authz.Stores, name string, permissions []string) {
	t.Helper()
	role := &privatev1.PlatformRole{
		TypeMeta:   metav1.TypeMeta{APIVersion: privatev1.GroupVersion.String(), Kind: "PlatformRole"},
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       privatev1.PlatformRoleSpec{Permissions: permissions},
	}
	role.SetGroupVersionKind(privatev1.GroupVersion.WithKind("PlatformRole"))
	if err := stores.PlatformRoles.Create(context.Background(), role); err != nil {
		t.Fatal(err)
	}
}

func seedRoleBinding(t *testing.T, stores authz.Stores, namespace, name, subject, roleName string, condition *string) {
	t.Helper()
	binding := &privatev1.RoleBinding{
		TypeMeta:   metav1.TypeMeta{APIVersion: privatev1.GroupVersion.String(), Kind: "RoleBinding"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: privatev1.RoleBindingSpec{
			Subject:   subject,
			RoleRef:   privatev1.RoleRef{Kind: "PlatformRole", Name: roleName, APIGroup: privatev1.GroupVersion.Group},
			Condition: condition,
		},
	}
	binding.SetGroupVersionKind(privatev1.GroupVersion.WithKind("RoleBinding"))
	if err := stores.RoleBindings.Create(context.Background(), binding); err != nil {
		t.Fatal(err)
	}
}

func testCluster(namespace, name, region string) *privatev1.Cluster {
	cluster := &privatev1.Cluster{
		TypeMeta:   metav1.TypeMeta{APIVersion: privatev1.GroupVersion.String(), Kind: "Cluster"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: privatev1.ClusterSpec{Platform: privatev1.ClusterPlatformSpec{
			Type: "GCP",
			GCP:  &privatev1.GCPClusterPlatform{Region: region},
		}},
	}
	cluster.SetGroupVersionKind(privatev1.GroupVersion.WithKind("Cluster"))
	return cluster
}
