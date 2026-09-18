package apiserver

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/go-logr/logr"

	privatetestv1 "github.com/openshift-online/gecko/orlop/apis/private/test/v1"
	publictestv1 "github.com/openshift-online/gecko/orlop/apis/public/test/v1"
	"github.com/openshift-online/gecko/orlop/pkg/apiserver/storage"
	"github.com/openshift-online/gecko/orlop/pkg/apiserver/storage/memory"
	"github.com/openshift-online/gecko/orlop/pkg/apiserver/types"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	runtimeschema "k8s.io/apimachinery/pkg/runtime/schema"
)

// --- Helpers ---

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to find free port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port
}

func waitForServer(t *testing.T, client *http.Client, url string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("server at %s did not become ready within %s", url, timeout)
}

func makePrivateScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := privatetestv1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add private test types to scheme: %v", err)
	}
	return scheme
}

func makePublicScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := publictestv1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add public test types to scheme: %v", err)
	}
	return scheme
}

func memoryStorageFactory() StorageFactory {
	return func(resourceType string, s *runtime.Scheme, gvk runtimeschema.GroupVersionKind) (storage.ResourceStore, error) {
		return memory.NewMemoryStore(resourceType, s, gvk), nil
	}
}

func doRequest(client *http.Client, method, url string, body []byte) (*http.Response, error) {
	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, url, bodyReader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return client.Do(req)
}

func privateResources() []types.ResourceInfo {
	return []types.ResourceInfo{privatetestv1.ObjectResourceInfo}
}

func publicResources() []types.ResourceInfo {
	return []types.ResourceInfo{publictestv1.ObjectResourceInfo}
}

func httpClient() *http.Client {
	return &http.Client{Timeout: 5 * time.Second}
}

func httpsClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
		Timeout: 5 * time.Second,
	}
}

func privatePath() string {
	return fmt.Sprintf("/apis/%s/%s/namespaces/default/objects",
		privatetestv1.GroupVersion.Group, privatetestv1.GroupVersion.Version)
}

func publicPath() string {
	return fmt.Sprintf("/apis/%s/%s/namespaces/default/objects",
		publictestv1.GroupVersion.Group, publictestv1.GroupVersion.Version)
}

// --- Server creation / validation tests ---

func TestNew(t *testing.T) {
	opts := Options{
		Address: "127.0.0.1",
		Private: PrivateAPIOptions{
			Port:        freePort(t),
			Scheme:      makePrivateScheme(t),
			Resources:   privateResources(),
			DisableAuth: true,
		},
		StorageFactory: memoryStorageFactory(),
		Logger:         logr.Discard(),
	}

	server, err := New(opts)
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}
	if server == nil {
		t.Fatal("New() returned nil server")
	}
	if server.aggregatedServer == nil {
		t.Error("expected aggregatedServer to be set")
	}
}

func TestNew_MissingScheme(t *testing.T) {
	opts := Options{
		Address: "127.0.0.1",
		Private: PrivateAPIOptions{
			Port:        freePort(t),
			Resources:   privateResources(),
			DisableAuth: true,
		},
		StorageFactory: memoryStorageFactory(),
		Logger:         logr.Discard(),
	}

	_, err := New(opts)
	if err == nil {
		t.Fatal("expected error when private scheme is missing")
	}
}

func TestNew_MissingResources(t *testing.T) {
	opts := Options{
		Address: "127.0.0.1",
		Private: PrivateAPIOptions{
			Port:        freePort(t),
			Scheme:      makePrivateScheme(t),
			DisableAuth: true,
		},
		StorageFactory: memoryStorageFactory(),
		Logger:         logr.Discard(),
	}

	_, err := New(opts)
	if err == nil {
		t.Fatal("expected error when resources are missing")
	}
}

func TestNew_MissingStorageFactory(t *testing.T) {
	opts := Options{
		Address: "127.0.0.1",
		Private: PrivateAPIOptions{
			Port:        freePort(t),
			Scheme:      makePrivateScheme(t),
			Resources:   privateResources(),
			DisableAuth: true,
		},
		Logger: logr.Discard(),
	}

	_, err := New(opts)
	if err == nil {
		t.Fatal("expected error when storage factory is missing")
	}
}

func TestNew_PublicAPI_MissingScheme(t *testing.T) {
	opts := Options{
		Address: "127.0.0.1",
		Private: PrivateAPIOptions{
			Port:        freePort(t),
			Scheme:      makePrivateScheme(t),
			Resources:   privateResources(),
			DisableAuth: true,
		},
		Public: PublicAPIOptions{
			Enable:    true,
			Port:      freePort(t),
			Resources: publicResources(),
			// Scheme intentionally omitted
		},
		StorageFactory: memoryStorageFactory(),
		Logger:         logr.Discard(),
	}

	_, err := New(opts)
	if err == nil {
		t.Fatal("expected error when public scheme is missing")
	}
}

func TestNew_PublicAPI_MissingResources(t *testing.T) {
	opts := Options{
		Address: "127.0.0.1",
		Private: PrivateAPIOptions{
			Port:        freePort(t),
			Scheme:      makePrivateScheme(t),
			Resources:   privateResources(),
			DisableAuth: true,
		},
		Public: PublicAPIOptions{
			Enable: true,
			Port:   freePort(t),
			Scheme: makePublicScheme(t),
			// Resources intentionally omitted
		},
		StorageFactory: memoryStorageFactory(),
		Logger:         logr.Discard(),
	}

	_, err := New(opts)
	if err == nil {
		t.Fatal("expected error when public resources are missing")
	}
}

// --- Address method tests ---

func TestPrivateAddress(t *testing.T) {
	port := freePort(t)
	opts := Options{
		Address: "127.0.0.1",
		Private: PrivateAPIOptions{
			Port:        port,
			Scheme:      makePrivateScheme(t),
			Resources:   privateResources(),
			DisableAuth: true,
		},
		StorageFactory: memoryStorageFactory(),
		Logger:         logr.Discard(),
	}

	server, err := New(opts)
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}

	expected := fmt.Sprintf(":%d", port)
	if got := server.PrivateAddress(); got != expected {
		t.Errorf("PrivateAddress() = %q, want %q", got, expected)
	}
}

func TestPublicAddress(t *testing.T) {
	t.Run("public enabled", func(t *testing.T) {
		publicPort := freePort(t)
		opts := Options{
			Address: "127.0.0.1",
			Private: PrivateAPIOptions{
				Port:        freePort(t),
				Scheme:      makePrivateScheme(t),
				Resources:   privateResources(),
				DisableAuth: true,
			},
			Public: PublicAPIOptions{
				Enable:    true,
				Port:      publicPort,
				Scheme:    makePublicScheme(t),
				Resources: publicResources(),
			},
			StorageFactory: memoryStorageFactory(),
			Logger:         logr.Discard(),
		}

		server, err := New(opts)
		if err != nil {
			t.Fatalf("New() returned error: %v", err)
		}

		expected := fmt.Sprintf("127.0.0.1:%d", publicPort)
		if got := server.PublicAddress(); got != expected {
			t.Errorf("PublicAddress() = %q, want %q", got, expected)
		}
	})

	t.Run("public disabled", func(t *testing.T) {
		opts := Options{
			Address: "127.0.0.1",
			Private: PrivateAPIOptions{
				Port:        freePort(t),
				Scheme:      makePrivateScheme(t),
				Resources:   privateResources(),
				DisableAuth: true,
			},
			StorageFactory: memoryStorageFactory(),
			Logger:         logr.Discard(),
		}

		server, err := New(opts)
		if err != nil {
			t.Fatalf("New() returned error: %v", err)
		}

		if got := server.PublicAddress(); got != "" {
			t.Errorf("PublicAddress() = %q, want empty string when public API is disabled", got)
		}
	})
}

// --- Integration tests ---

func TestServer_CRUD(t *testing.T) {
	privatePort := freePort(t)
	opts := Options{
		Address: "127.0.0.1",
		Private: PrivateAPIOptions{
			Port:        privatePort,
			Scheme:      makePrivateScheme(t),
			Resources:   privateResources(),
			DisableAuth: true,
		},
		StorageFactory: memoryStorageFactory(),
		Logger:         logr.Discard(),
	}

	server, err := New(opts)
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}

	go func() {
		_ = server.Run()
	}()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		server.Shutdown(ctx)
	})

	client := httpsClient()
	baseURL := fmt.Sprintf("https://localhost:%d", privatePort)
	waitForServer(t, client, baseURL+"/healthz", 10*time.Second)

	// CREATE
	obj := &privatetestv1.Object{
		TypeMeta: metav1.TypeMeta{
			APIVersion: privatetestv1.GroupVersion.String(),
			Kind:       "Object",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "aggregated-test",
			Namespace: "default",
		},
		Spec: privatetestv1.ObjectSpec{
			PublicField:   "public-value",
			InternalField: "internal-value",
		},
	}

	body, _ := json.Marshal(obj)
	resp, err := doRequest(client, http.MethodPost, baseURL+privatePath(), body)
	if err != nil {
		t.Fatalf("CREATE failed: %v", err)
	}
	respBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("CREATE expected 201, got %d: %s", resp.StatusCode, string(respBody))
	}

	var created privatetestv1.Object
	if err := json.Unmarshal(respBody, &created); err != nil {
		t.Fatalf("failed to unmarshal created object: %v", err)
	}
	if created.Name != "aggregated-test" {
		t.Errorf("expected name %q, got %q", "aggregated-test", created.Name)
	}
	if created.UID == "" {
		t.Error("expected UID to be set on created object")
	}

	// GET
	resp, err = client.Get(baseURL + privatePath() + "/aggregated-test")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	respBody, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET expected 200, got %d: %s", resp.StatusCode, string(respBody))
	}

	var fetched privatetestv1.Object
	json.Unmarshal(respBody, &fetched)
	if fetched.Spec.PublicField != "public-value" {
		t.Errorf("GET expected publicField %q, got %q", "public-value", fetched.Spec.PublicField)
	}
	if fetched.Spec.InternalField != "internal-value" {
		t.Errorf("GET expected internalField %q, got %q", "internal-value", fetched.Spec.InternalField)
	}

	// LIST
	resp, err = client.Get(baseURL + privatePath())
	if err != nil {
		t.Fatalf("LIST failed: %v", err)
	}
	respBody, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("LIST expected 200, got %d: %s", resp.StatusCode, string(respBody))
	}

	var list privatetestv1.ObjectList
	json.Unmarshal(respBody, &list)
	if len(list.Items) != 1 {
		t.Errorf("LIST expected 1 item, got %d", len(list.Items))
	}

	// UPDATE
	fetched.Spec.PublicField = "updated-value"
	updateBody, _ := json.Marshal(fetched)
	resp, err = doRequest(client, http.MethodPut, baseURL+privatePath()+"/aggregated-test", updateBody)
	if err != nil {
		t.Fatalf("UPDATE failed: %v", err)
	}
	respBody, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("UPDATE expected 200, got %d: %s", resp.StatusCode, string(respBody))
	}

	var updated privatetestv1.Object
	json.Unmarshal(respBody, &updated)
	if updated.Spec.PublicField != "updated-value" {
		t.Errorf("UPDATE expected publicField %q, got %q", "updated-value", updated.Spec.PublicField)
	}

	// DELETE
	resp, err = doRequest(client, http.MethodDelete, baseURL+privatePath()+"/aggregated-test", nil)
	if err != nil {
		t.Fatalf("DELETE failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("DELETE expected 200, got %d", resp.StatusCode)
	}

	// Verify deleted
	resp, err = client.Get(baseURL + privatePath() + "/aggregated-test")
	if err != nil {
		t.Fatalf("GET after DELETE failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET after DELETE expected 404, got %d", resp.StatusCode)
	}
}

func TestServer_PublicAPIConversion(t *testing.T) {
	privatePort := freePort(t)
	publicPort := freePort(t)

	opts := Options{
		Address: "127.0.0.1",
		Private: PrivateAPIOptions{
			Port:        privatePort,
			Scheme:      makePrivateScheme(t),
			Resources:   privateResources(),
			DisableAuth: true,
		},
		Public: PublicAPIOptions{
			Enable:    true,
			Port:      publicPort,
			Scheme:    makePublicScheme(t),
			Resources: publicResources(),
		},
		StorageFactory: memoryStorageFactory(),
		Logger:         logr.Discard(),
	}

	server, err := New(opts)
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}

	go func() {
		_ = server.Run()
	}()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		server.Shutdown(ctx)
	})

	privateClient := httpsClient()
	publicClient := httpClient()

	privateBase := fmt.Sprintf("https://localhost:%d", privatePort)
	publicBase := fmt.Sprintf("http://127.0.0.1:%d", publicPort)

	waitForServer(t, privateClient, privateBase+"/healthz", 10*time.Second)
	waitForServer(t, publicClient, publicBase+publicPath(), 10*time.Second)

	// Create object via aggregated private API with both public and internal fields
	obj := &privatetestv1.Object{
		TypeMeta: metav1.TypeMeta{
			APIVersion: privatetestv1.GroupVersion.String(),
			Kind:       "Object",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "conversion-test",
			Namespace: "default",
		},
		Spec: privatetestv1.ObjectSpec{
			PublicField:   "visible-to-all",
			InternalField: "secret-internal",
		},
	}

	body, _ := json.Marshal(obj)
	resp, err := doRequest(privateClient, http.MethodPost, privateBase+privatePath(), body)
	if err != nil {
		t.Fatalf("CREATE via private API failed: %v", err)
	}
	respBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("CREATE expected 201, got %d: %s", resp.StatusCode, string(respBody))
	}

	// Read object via public API
	resp, err = publicClient.Get(publicBase + publicPath() + "/conversion-test")
	if err != nil {
		t.Fatalf("GET via public API failed: %v", err)
	}
	respBody, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET via public API expected 200, got %d: %s", resp.StatusCode, string(respBody))
	}

	// Verify public field is present
	var publicObj map[string]interface{}
	if err := json.Unmarshal(respBody, &publicObj); err != nil {
		t.Fatalf("failed to unmarshal public response: %v", err)
	}

	spec, ok := publicObj["spec"].(map[string]interface{})
	if !ok {
		t.Fatal("expected spec in public response")
	}

	if pf, ok := spec["publicField"].(string); !ok || pf != "visible-to-all" {
		t.Errorf("expected publicField %q, got %v", "visible-to-all", spec["publicField"])
	}

	// Verify internal field is absent (conversion stripped it)
	if _, exists := spec["internalField"]; exists {
		t.Error("expected internalField to be absent in public API response, but it was present")
	}
}

func TestServer_Shutdown(t *testing.T) {
	privatePort := freePort(t)
	opts := Options{
		Address: "127.0.0.1",
		Private: PrivateAPIOptions{
			Port:        privatePort,
			Scheme:      makePrivateScheme(t),
			Resources:   privateResources(),
			DisableAuth: true,
		},
		StorageFactory: memoryStorageFactory(),
		Logger:         logr.Discard(),
	}

	server, err := New(opts)
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}

	go func() {
		_ = server.Run()
	}()

	client := httpsClient()
	baseURL := fmt.Sprintf("https://localhost:%d", privatePort)
	waitForServer(t, client, baseURL+"/healthz", 10*time.Second)

	// Shutdown gracefully
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown() returned error: %v", err)
	}

	// After shutdown, connections should eventually fail
	time.Sleep(500 * time.Millisecond)
	_, err = client.Get(baseURL + "/healthz")
	if err == nil {
		t.Error("expected error after shutdown, but request succeeded")
	}
}

func TestServer_PublicAPI_SharedStore(t *testing.T) {
	privatePort := freePort(t)
	publicPort := freePort(t)

	opts := Options{
		Address: "127.0.0.1",
		Private: PrivateAPIOptions{
			Port:        privatePort,
			Scheme:      makePrivateScheme(t),
			Resources:   privateResources(),
			DisableAuth: true,
		},
		Public: PublicAPIOptions{
			Enable:    true,
			Port:      publicPort,
			Scheme:    makePublicScheme(t),
			Resources: publicResources(),
		},
		StorageFactory: memoryStorageFactory(),
		Logger:         logr.Discard(),
	}

	server, err := New(opts)
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}

	go func() {
		_ = server.Run()
	}()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		server.Shutdown(ctx)
	})

	privateClient := httpsClient()
	publicClient := httpClient()

	privateBase := fmt.Sprintf("https://localhost:%d", privatePort)
	publicBase := fmt.Sprintf("http://127.0.0.1:%d", publicPort)

	waitForServer(t, privateClient, privateBase+"/healthz", 10*time.Second)
	waitForServer(t, publicClient, publicBase+publicPath(), 10*time.Second)

	// Create multiple objects via private API
	for i := 0; i < 3; i++ {
		obj := &privatetestv1.Object{
			TypeMeta: metav1.TypeMeta{
				APIVersion: privatetestv1.GroupVersion.String(),
				Kind:       "Object",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("shared-store-%d", i),
				Namespace: "default",
			},
			Spec: privatetestv1.ObjectSpec{
				PublicField:   fmt.Sprintf("value-%d", i),
				InternalField: fmt.Sprintf("internal-%d", i),
			},
		}
		body, _ := json.Marshal(obj)
		resp, err := doRequest(privateClient, http.MethodPost, privateBase+privatePath(), body)
		if err != nil {
			t.Fatalf("CREATE %d via private API failed: %v", i, err)
		}
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("CREATE %d expected 201, got %d: %s", i, resp.StatusCode, string(respBody))
		}
	}

	// List via public API and verify count matches
	resp, err := publicClient.Get(publicBase + publicPath())
	if err != nil {
		t.Fatalf("LIST via public API failed: %v", err)
	}
	respBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("LIST via public API expected 200, got %d: %s", resp.StatusCode, string(respBody))
	}

	var publicList map[string]interface{}
	if err := json.Unmarshal(respBody, &publicList); err != nil {
		t.Fatalf("failed to unmarshal public list: %v", err)
	}

	items, ok := publicList["items"].([]interface{})
	if !ok {
		t.Fatal("expected items array in public list response")
	}
	if len(items) != 3 {
		t.Errorf("expected 3 items via public API (shared store), got %d", len(items))
	}

	// Verify each item has public field but not internal field
	for i, item := range items {
		objMap, ok := item.(map[string]interface{})
		if !ok {
			t.Errorf("item %d: expected map, got %T", i, item)
			continue
		}
		spec, ok := objMap["spec"].(map[string]interface{})
		if !ok {
			t.Errorf("item %d: expected spec map", i)
			continue
		}
		if _, exists := spec["publicField"]; !exists {
			t.Errorf("item %d: expected publicField to be present", i)
		}
		if _, exists := spec["internalField"]; exists {
			t.Errorf("item %d: expected internalField to be absent in public API", i)
		}
	}

	// Also verify private API returns all 3 with internal fields
	resp, err = privateClient.Get(privateBase + privatePath())
	if err != nil {
		t.Fatalf("LIST via private API failed: %v", err)
	}
	respBody, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("LIST via private API expected 200, got %d: %s", resp.StatusCode, string(respBody))
	}

	var privateList privatetestv1.ObjectList
	if err := json.Unmarshal(respBody, &privateList); err != nil {
		t.Fatalf("failed to unmarshal private list: %v", err)
	}
	if len(privateList.Items) != 3 {
		t.Errorf("expected 3 items via private API, got %d", len(privateList.Items))
	}
	for i, item := range privateList.Items {
		if item.Spec.InternalField == "" {
			t.Errorf("item %d: expected internalField to be set in private API response", i)
		}
	}
}
