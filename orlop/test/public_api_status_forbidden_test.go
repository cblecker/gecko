package test

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	privatev1 "github.com/openshift-online/gecko/orlop/apis/private/test/v1"
	publicv1 "github.com/openshift-online/gecko/orlop/apis/public/test/v1"
	"github.com/openshift-online/gecko/orlop/pkg/apiserver"
	"github.com/openshift-online/gecko/orlop/pkg/apiserver/storage"
	"github.com/openshift-online/gecko/orlop/pkg/apiserver/storage/memory"

	"k8s.io/apimachinery/pkg/runtime"
	runtimeschema "k8s.io/apimachinery/pkg/runtime/schema"
)

// TestPublicAPIStatusForbidden verifies that the /status subresource endpoint
// is not accessible on the public API (GCP-1062).
//
// Requirements:
//   - PUT /namespaces/{namespace}/{resource}/{name}/status must return 404 or 405
//   - Private API should still allow status updates (sanity check)
//   - Status updates via private API should still work normally
func TestPublicAPIStatusForbidden(t *testing.T) {
	// Setup test server with private + public APIs
	privateScheme := runtime.NewScheme()
	if err := privatev1.AddToScheme(privateScheme); err != nil {
		t.Fatalf("failed to add private scheme: %v", err)
	}

	publicScheme := runtime.NewScheme()
	if err := publicv1.AddToScheme(publicScheme); err != nil {
		t.Fatalf("failed to add public scheme: %v", err)
	}

	gvk := runtimeschema.GroupVersionKind{
		Group:   "test.orlop.gcp.managed.openshift.io",
		Version: "v1",
		Kind:    "Object",
	}

	privateResources := []apiserver.ResourceInfo{{
		GVK:        gvk,
		Plural:     privatev1.ObjectResourceInfo.Plural,
		Singular:   "object",
		Namespaced: true,
		SchemaYAML: privatev1.ObjectSchemaYAML,
	}}

	publicResources := []apiserver.ResourceInfo{{
		GVK:        gvk,
		Plural:     publicv1.ObjectResourceInfo.Plural,
		Singular:   "object",
		Namespaced: true,
		SchemaYAML: publicv1.ObjectSchemaYAML,
	}}

	storageFactory := func(resourceType string, s *runtime.Scheme, gvk runtimeschema.GroupVersionKind) (storage.ResourceStore, error) {
		return memory.NewMemoryStore(resourceType, s, gvk), nil
	}

	ports := freePorts(t, 2)

	opts := apiserver.Options{
		Address:     "127.0.0.1",
		CORSOrigins: []string{"*"},
		Private: apiserver.PrivateAPIOptions{
			Port:        ports[0],
			Resources:   privateResources,
			Scheme:      privateScheme,
			DisableAuth: true,
		},
		Public: apiserver.PublicAPIOptions{
			Enable:    true,
			Port:      ports[1],
			Resources: publicResources,
			Scheme:    publicScheme,
		},
		StorageFactory: storageFactory,
	}

	server, err := apiserver.New(opts)
	if err != nil {
		t.Fatalf("Failed to create test server: %v", err)
	}

	privateURL := fmt.Sprintf("https://localhost%s", server.PrivateAddress())
	publicURL := fmt.Sprintf("http://%s", server.PublicAddress())

	go func() {
		if err := server.Run(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Logf("Test server error: %v", err)
		}
	}()

	privClient := &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
		Timeout:   5 * time.Second,
	}
	pubClient := &http.Client{Timeout: 5 * time.Second}

	// Wait for both APIs
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := privClient.Get(privateURL + "/healthz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}

	for time.Now().Before(deadline) {
		resp, err := pubClient.Get(publicURL + "/healthz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			t.Errorf("server shutdown failed: %v", err)
		}
	})

	const ns = "default"
	apiPath := "/apis/test.orlop.gcp.managed.openshift.io/v1/namespaces/" + ns + "/objects"

	t.Run("Public API status endpoint returns 404 or 405", func(t *testing.T) {
		// 1. Create resource via public API
		name := "test-obj-public-status"
		createObj := map[string]interface{}{
			"apiVersion": "test.orlop.gcp.managed.openshift.io/v1",
			"kind":       "Object",
			"metadata":   map[string]interface{}{"name": name, "namespace": ns},
			"spec": map[string]interface{}{
				"publicField": "initial-value",
				"nested": map[string]interface{}{
					"publicField": "nested-value",
				},
			},
		}

		createResp := httpDo(t, pubClient, http.MethodPost, publicURL+apiPath, createObj)
		if createResp.StatusCode != http.StatusCreated {
			t.Fatalf("create failed: expected 201, got %d: %s", createResp.StatusCode, createResp.Body)
		}

		var created map[string]interface{}
		if err := json.Unmarshal([]byte(createResp.Body), &created); err != nil {
			t.Fatalf("failed to unmarshal created object: %v", err)
		}

		// 2. Attempt UpdateStatus on public API
		statusURL := publicURL + apiPath + "/" + name + "/status"
		statusObj := map[string]interface{}{
			"apiVersion": "test.orlop.gcp.managed.openshift.io/v1",
			"kind":       "Object",
			"metadata":   created["metadata"],
			"spec":       created["spec"],
			"status": map[string]interface{}{
				"conditions": []interface{}{
					"Ready",
				},
			},
		}

		statusResp := httpDo(t, pubClient, http.MethodPut, statusURL, statusObj)

		// Verify: status endpoint should return 404 Not Found
		if statusResp.StatusCode != http.StatusNotFound {
			t.Errorf("public API UpdateStatus should return 404, got %d: %s",
				statusResp.StatusCode, statusResp.Body)
		}

		t.Logf("✓ Public API UpdateStatus correctly blocked with HTTP 404")

		// Cleanup
		httpDo(t, privClient, http.MethodDelete, privateURL+apiPath+"/"+name, nil)
	})

	t.Run("Private API status endpoint still works", func(t *testing.T) {
		// Sanity check: private API status updates should still function
		name := "test-obj-private-status"
		createObj := map[string]interface{}{
			"apiVersion": "test.orlop.gcp.managed.openshift.io/v1",
			"kind":       "Object",
			"metadata":   map[string]interface{}{"name": name, "namespace": ns},
			"spec": map[string]interface{}{
				"publicField": "initial-value",
				"nested": map[string]interface{}{
					"publicField": "nested-value",
				},
			},
		}

		createResp := httpDo(t, privClient, http.MethodPost, privateURL+apiPath, createObj)
		if createResp.StatusCode != http.StatusCreated {
			t.Fatalf("private create failed: expected 201, got %d", createResp.StatusCode)
		}

		var created map[string]interface{}
		json.Unmarshal([]byte(createResp.Body), &created)

		// Update status via private API
		statusURL := privateURL + apiPath + "/" + name + "/status"
		statusObj := map[string]interface{}{
			"apiVersion": "test.orlop.gcp.managed.openshift.io/v1",
			"kind":       "Object",
			"metadata":   created["metadata"],
			"spec":       created["spec"],
			"status": map[string]interface{}{
				"conditions": []interface{}{
					"Ready",
				},
			},
		}

		statusResp := httpDo(t, privClient, http.MethodPut, statusURL, statusObj)
		if statusResp.StatusCode != http.StatusOK {
			t.Fatalf("private API UpdateStatus should succeed, got %d: %s",
				statusResp.StatusCode, statusResp.Body)
		}

		// Verify status was actually updated
		getResp := httpDo(t, privClient, http.MethodGet, privateURL+apiPath+"/"+name, nil)
		if getResp.StatusCode != http.StatusOK {
			t.Fatalf("private get failed: expected 200, got %d", getResp.StatusCode)
		}

		var retrieved map[string]interface{}
		json.Unmarshal([]byte(getResp.Body), &retrieved)

		status, ok := retrieved["status"].(map[string]interface{})
		if !ok {
			t.Errorf("status field missing: %+v", retrieved)
		}

		conds, ok := status["conditions"].([]interface{})
		if !ok || len(conds) == 0 || conds[0] != "Ready" {
			t.Errorf("status.conditions not updated correctly: %+v", status)
		}

		t.Logf("✓ Private API UpdateStatus works correctly")

		// Cleanup
		httpDo(t, privClient, http.MethodDelete, privateURL+apiPath+"/"+name, nil)
	})

	t.Run("Public API discovery does not advertise status subresource", func(t *testing.T) {
		// Verify discovery endpoint returns valid response
		discoveryURL := publicURL + "/apis/test.orlop.gcp.managed.openshift.io/v1"
		discoveryResp := httpDo(t, pubClient, http.MethodGet, discoveryURL, nil)

		if discoveryResp.StatusCode != http.StatusOK {
			t.Fatalf("discovery failed: expected 200, got %d", discoveryResp.StatusCode)
		}

		var discovery map[string]interface{}
		if err := json.Unmarshal([]byte(discoveryResp.Body), &discovery); err != nil {
			t.Fatalf("failed to unmarshal discovery: %v", err)
		}

		resources, ok := discovery["resources"].([]interface{})
		if !ok {
			t.Fatalf("discovery missing resources field")
		}

		// Verify status subresource NOT advertised (proves router uses advertiseStatus=false)
		for _, r := range resources {
			res := r.(map[string]interface{})
			name := res["name"].(string)
			if name == "objects/status" {
				t.Errorf("public API discovery should NOT advertise status subresource, but found: %s", name)
			}
		}

		t.Logf("✓ Public API discovery correctly omits status subresource")
	})

	t.Run("Private API discovery still advertises status subresource", func(t *testing.T) {
		// Verify discovery endpoint returns valid response
		discoveryURL := privateURL + "/apis/test.orlop.gcp.managed.openshift.io/v1"
		discoveryResp := httpDo(t, privClient, http.MethodGet, discoveryURL, nil)

		if discoveryResp.StatusCode != http.StatusOK {
			t.Fatalf("discovery failed: expected 200, got %d", discoveryResp.StatusCode)
		}

		var discovery map[string]interface{}
		if err := json.Unmarshal([]byte(discoveryResp.Body), &discovery); err != nil {
			t.Fatalf("failed to unmarshal private discovery: %v", err)
		}

		resources, ok := discovery["resources"].([]interface{})
		if !ok {
			t.Fatalf("private discovery missing resources field")
		}
		foundStatus := false
		for _, r := range resources {
			res := r.(map[string]interface{})
			name := res["name"].(string)
			if name == "objects/status" {
				foundStatus = true
				break
			}
		}

		if !foundStatus {
			t.Errorf("private API discovery should advertise status subresource")
		}

		t.Logf("✓ Private API discovery correctly includes status subresource")
	})
}

// httpResponse holds response data
type httpResponse struct {
	StatusCode int
	Body       string
}

// httpDo is a helper for making HTTP requests
func httpDo(t *testing.T, client *http.Client, method, url string, body interface{}) httpResponse {
	t.Helper()

	var reqBody io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal error: %v", err)
		}
		reqBody = bytes.NewBuffer(data)
	}

	req, err := http.NewRequest(method, url, reqBody)
	if err != nil {
		t.Fatalf("request creation error: %v", err)
	}

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("HTTP request error: %v", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body error: %v", err)
	}

	return httpResponse{
		StatusCode: resp.StatusCode,
		Body:       string(respBody),
	}
}
