package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"reflect"
	"strings"
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

func TestPublicAPICatalogAuthorizationExemptReads(t *testing.T) {
	for _, enforce := range []bool{true, false} {
		t.Run(fmt.Sprintf("authorization=%t", enforce), func(t *testing.T) {
			testPublicAPICatalogReads(t, enforce)
		})
	}
}

func testPublicAPICatalogReads(t *testing.T, enforce bool) {
	privatePort := freePublicAPITestPort(t)
	publicPort := freePublicAPITestPort(t)
	publicResources := getPublicResources()
	logger := logr.Discard()
	privateScheme := getPrivateScheme()
	versionGVK := privatev1.GroupVersion.WithKind("Version")
	versionStore := memory.NewMemoryStore(apiserver.GroupKindResourceType(versionGVK.GroupKind()), privateScheme, versionGVK)
	version := &privatev1.Version{
		TypeMeta: metav1.TypeMeta{
			APIVersion: privatev1.GroupVersion.String(),
			Kind:       "Version",
		},
		ObjectMeta: metav1.ObjectMeta{Name: "4.22.1"},
		Spec: privatev1.VersionSpec{
			ChannelGroups: []string{"stable"},
			ReleaseImage:  "quay.io/openshift-release-dev/ocp-release@sha256:internal-only",
		},
	}
	version.SetGroupVersionKind(versionGVK)
	if err := versionStore.Create(context.Background(), version); err != nil {
		t.Fatalf("seed Version: %v", err)
	}
	channelGVK := privatev1.GroupVersion.WithKind("Channel")
	channelStore := memory.NewMemoryStore(apiserver.GroupKindResourceType(channelGVK.GroupKind()), privateScheme, channelGVK)
	channel := &privatev1.Channel{
		TypeMeta: metav1.TypeMeta{
			APIVersion: privatev1.GroupVersion.String(),
			Kind:       "Channel",
		},
		ObjectMeta: metav1.ObjectMeta{Name: "stable"},
		Spec: privatev1.ChannelSpec{
			InstallDefaultVersion: "4.22.1",
			FleetMinorVersion:     "4.22",
		},
	}
	channel.SetGroupVersionKind(channelGVK)
	if err := channelStore.Create(context.Background(), channel); err != nil {
		t.Fatalf("seed Channel: %v", err)
	}

	roleGVK := privatev1.GroupVersion.WithKind("PlatformRole")
	roleStore := memory.NewMemoryStore(apiserver.GroupKindResourceType(roleGVK.GroupKind()), privateScheme, roleGVK)
	role := &privatev1.PlatformRole{
		TypeMeta: metav1.TypeMeta{APIVersion: privatev1.GroupVersion.String(), Kind: "PlatformRole"},
		ObjectMeta: metav1.ObjectMeta{
			Name:              "catalog-reader",
			CreationTimestamp: metav1.NewTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
			Annotations:       map[string]string{"argocd.argoproj.io/tracking-id": "internal-tracking"},
		},
		Spec: privatev1.PlatformRoleSpec{Permissions: []string{"cluster.get", "cluster.list", "role.get"}},
	}
	role.SetGroupVersionKind(roleGVK)
	if err := roleStore.Create(context.Background(), role); err != nil {
		t.Fatal(err)
	}
	middleware := []func(http.Handler) http.Handler{authn.Middleware(authn.Config{})}
	if enforce {
		middleware = append(middleware, authz.Middleware(nil, logger, publicResources))
	}

	server, err := apiserver.New(apiserver.Options{
		Address: "127.0.0.1",
		Private: apiserver.PrivateAPIOptions{
			Port:        privatePort,
			Scheme:      privateScheme,
			Resources:   getPrivateResources(),
			DisableAuth: true,
		},
		Public: apiserver.PublicAPIOptions{
			Enable:     true,
			Address:    "127.0.0.1",
			Port:       publicPort,
			Scheme:     getPublicScheme(),
			Resources:  publicResources,
			Middleware: middleware,
		},
		StorageFactory: func(resourceType string, scheme *runtime.Scheme, gvk runtimeschema.GroupVersionKind) (storage.ResourceStore, error) {
			switch gvk.Kind {
			case "PlatformRole":
				return roleStore, nil
			case "Version":
				return versionStore, nil
			case "Channel":
				return channelStore, nil
			}
			return memory.NewMemoryStore(resourceType, scheme, gvk), nil
		},
		Logger: logger,
	})
	if err != nil {
		t.Fatalf("create API server: %v", err)
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

	userInfo := platformAPITestUserInfo(t)
	request := publicCatalogRequest(t, client, baseURL, userInfo)

	apiPrefix := "/apis/gcp.managed.openshift.io/v1"
	for _, path := range []struct {
		name string
		path string
	}{
		{name: "list versions", path: apiPrefix + "/versions"},
		{name: "list channels", path: apiPrefix + "/channels"},
		{name: "list platform roles", path: apiPrefix + "/platformroles"},
	} {
		t.Run(path.name, func(t *testing.T) {
			if got, _ := request(http.MethodGet, path.path, true); got != http.StatusOK {
				t.Errorf("authenticated GET %s = %d, want %d", path.path, got, http.StatusOK)
			}
		})
	}
	for _, path := range []struct {
		name         string
		path         string
		wantContains string
		wantAbsent   string
	}{
		{name: "get version", path: apiPrefix + "/versions/4.22.1", wantContains: "4.22.1", wantAbsent: "releaseImage"},
		{name: "get channel", path: apiPrefix + "/channels/stable", wantContains: "4.22.1"},
		{name: "get platform role", path: apiPrefix + "/platformroles/catalog-reader", wantContains: "role.get"},
	} {
		t.Run(path.name, func(t *testing.T) {
			status, body := request(http.MethodGet, path.path, true)
			if status != http.StatusOK {
				t.Fatalf("authenticated GET %s = %d, want %d: %s", path.path, status, http.StatusOK, body)
			}
			if !strings.Contains(body, path.wantContains) {
				t.Errorf("response %s does not contain %q", path.path, path.wantContains)
			}
			if path.wantAbsent != "" && strings.Contains(body, path.wantAbsent) {
				t.Errorf("response %s unexpectedly contains %q", path.path, path.wantAbsent)
			}
		})
	}
	for _, path := range []string{
		apiPrefix + "/versions/4.22.2",
		apiPrefix + "/channels/fast",
		apiPrefix + "/platformroles/missing",
	} {
		if got, _ := request(http.MethodGet, path, true); got != http.StatusNotFound {
			t.Errorf("authenticated GET %s = %d, want %d for missing catalog item", path, got, http.StatusNotFound)
		}
	}

	deniedRequests := []struct {
		name          string
		method        string
		path          string
		authenticated bool
		wantStatus    int
	}{
		{name: "unauthenticated cluster list", method: http.MethodGet, path: apiPrefix + "/clusters", wantStatus: http.StatusUnauthorized},
		{name: "unauthenticated cluster create", method: http.MethodPost, path: apiPrefix + "/namespaces/project-a/clusters", wantStatus: http.StatusUnauthorized},
		{name: "unauthenticated catalog list", method: http.MethodGet, path: apiPrefix + "/versions", wantStatus: http.StatusUnauthorized},
		{name: "unauthenticated catalog item", method: http.MethodGet, path: apiPrefix + "/versions/4.22.1", wantStatus: http.StatusUnauthorized},
		{name: "unauthenticated catalog write", method: http.MethodPost, path: apiPrefix + "/versions", wantStatus: http.StatusUnauthorized},
		{name: "unauthenticated catalog watch", method: http.MethodGet, path: apiPrefix + "/versions?watch=true", wantStatus: http.StatusUnauthorized},
		{name: "authenticated catalog write", method: http.MethodPost, path: apiPrefix + "/versions", authenticated: true, wantStatus: http.StatusForbidden},
		{name: "authenticated catalog watch", method: http.MethodGet, path: apiPrefix + "/versions?watch=true", authenticated: true, wantStatus: http.StatusForbidden},
	}
	for _, tt := range deniedRequests {
		t.Run(tt.name, func(t *testing.T) {
			if !enforce && tt.authenticated {
				return
			}
			if got, _ := request(tt.method, tt.path, tt.authenticated); got != tt.wantStatus {
				t.Errorf("%s %s = %d, want %d", tt.method, tt.path, got, tt.wantStatus)
			}
		})
	}
	testPublicPlatformRoleReads(t, request, client, baseURL, apiPrefix)
	testPublicPlatformRoleRestrictions(t, request, apiPrefix)

	if _, err := roleStore.Get(context.Background(), "", "public-created"); err == nil {
		t.Error("public POST persisted a role")
	}

	stored, err := roleStore.Get(context.Background(), "", "catalog-reader")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(stored.(*privatev1.PlatformRole).Spec, role.Spec) {
		t.Errorf("public requests changed private permissions: %#v", stored)
	}

}

func publicCatalogRequest(t *testing.T, client *http.Client, baseURL, userInfo string) func(string, string, bool) (int, string) {
	t.Helper()

	return func(method, path string, authenticated bool) (int, string) {
		t.Helper()
		name := "catalog-reader"
		if method == http.MethodPost {
			name = "public-created"
		}
		bodyJSON := fmt.Sprintf(`{"apiVersion":"gcp.managed.openshift.io/v1","kind":"PlatformRole","metadata":{"name":%q},"spec":{"permissions":["role.delete"]}}`, name)
		req, err := http.NewRequestWithContext(context.Background(), method, baseURL+path, strings.NewReader(bodyJSON))
		if err != nil {
			t.Fatalf("create request: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		if method == http.MethodPatch {
			req.Header.Set("Content-Type", "application/merge-patch+json")
		}
		if authenticated {
			req.Header.Set(constants.HeaderEndpointAPIUserInfo, userInfo)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request %s %s: %v", method, path, err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("read response for %s %s: %v", method, path, err)
		}
		return resp.StatusCode, string(body)
	}
}

func testPublicPlatformRoleReads(t *testing.T, request func(string, string, bool) (int, string), client *http.Client, baseURL, apiPrefix string) {
	t.Helper()

	for _, path := range []string{"/platformroles", "/platformroles/catalog-reader"} {
		if got, _ := request(http.MethodGet, apiPrefix+path, false); got != http.StatusUnauthorized {
			t.Errorf("anonymous role read = %d", got)
		}
		status, body := request(http.MethodGet, apiPrefix+path, true)
		if status != http.StatusOK {
			t.Fatalf("role read = %d: %s", status, body)
		}
		for _, permission := range []string{"cluster.get", "cluster.list", "role.get"} {
			if !strings.Contains(body, permission) {
				t.Errorf("missing permission %s: %s", permission, body)
			}
		}
		// A second authenticated caller also needs no namespace grant.
		other, err := http.NewRequestWithContext(context.Background(), http.MethodGet, baseURL+apiPrefix+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		other.Header.Set(constants.HeaderEndpointAPIUserInfo, base64.RawURLEncoding.EncodeToString([]byte(`{"email":"bob@example.com","email_verified":true}`)))
		otherResponse, err := client.Do(other)
		if err != nil {
			t.Fatal(err)
		}
		otherBody, err := io.ReadAll(otherResponse.Body)
		otherResponse.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if otherResponse.StatusCode != http.StatusOK || string(otherBody) != body {
			t.Errorf("catalog differs for another authenticated caller: %s", otherBody)
		}

		for _, identity := range []string{"invalid-base64", base64.RawURLEncoding.EncodeToString([]byte(`{"email":"bob@example.com","email_verified":false}`))} {
			req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, baseURL+apiPrefix+path, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set(constants.HeaderEndpointAPIUserInfo, identity)
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("invalid identity = %d", resp.StatusCode)
			}
		}
	}
}

func testPublicPlatformRoleRestrictions(t *testing.T, request func(string, string, bool) (int, string), apiPrefix string) {
	t.Helper()

	for _, mutation := range []struct{ method, path string }{
		{http.MethodPost, "/platformroles"},
		{http.MethodPut, "/platformroles/catalog-reader"},
		{http.MethodPatch, "/platformroles/catalog-reader"},
		{http.MethodDelete, "/platformroles/catalog-reader"},
		{http.MethodDelete, "/platformroles"},
		{http.MethodGet, "/platformroles?watch=true"},
	} {
		if status, body := request(mutation.method, apiPrefix+mutation.path, true); status != http.StatusForbidden && status != http.StatusNotImplemented && status != http.StatusMethodNotAllowed {
			t.Errorf("unsupported operation %s %s succeeded: %s", mutation.method, mutation.path, body)
		}
	}
	for _, path := range []string{
		apiPrefix + "/namespaces/project-a/platformroles",
		apiPrefix + "/namespaces/project-b/platformroles/catalog-reader",
		apiPrefix + "/platformroles/catalog-reader/status",
		"/apis/other.example.io/v1/platformroles",
		"/apis/gcp.managed.openshift.io/v2/platformroles",
	} {
		if status, body := request(http.MethodGet, path, true); status >= 200 && status < 300 {
			t.Errorf("invalid discovery route %s succeeded: %s", path, body)
		}
	}
}

func freePublicAPITestPort(t *testing.T) int {
	t.Helper()
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve test port: %v", err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

func waitForPublicAPITestServer(t *testing.T, client *http.Client, url string, runDone <-chan struct{}, runErr *error) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-runDone:
			t.Fatalf("API server stopped before becoming ready: %v", *runErr)
		default:
		}
		request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
		if err != nil {
			t.Fatalf("create readiness request: %v", err)
		}
		resp, err := client.Do(request)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("API server at %s did not become ready", url)
}

func platformAPITestUserInfo(t *testing.T) string {
	t.Helper()
	claims, err := json.Marshal(map[string]any{
		"email":          "alice@example.com",
		"email_verified": true,
	})
	if err != nil {
		t.Fatalf("marshal user claims: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(claims)
}
