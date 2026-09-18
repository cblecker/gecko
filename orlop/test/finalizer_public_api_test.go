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

// TestFinalizerDeletionPublicAPI tests the finalizer deletion flow via the public API
// This verifies the fix for GCP-1060 where ConvertingResourceHandler.Delete bypassed finalizers
func TestFinalizerDeletionPublicAPI(t *testing.T) {
	// Setup dedicated test server with public API enabled
	privateScheme := runtime.NewScheme()
	if err := privatev1.AddToScheme(privateScheme); err != nil {
		t.Fatalf("failed to register private API types: %v", err)
	}

	publicScheme := runtime.NewScheme()
	if err := publicv1.AddToScheme(publicScheme); err != nil {
		t.Fatalf("failed to register public API types: %v", err)
	}

	privateResources := []apiserver.ResourceInfo{
		{
			GVK: runtimeschema.GroupVersionKind{
				Group:   "test.orlop.gcp.managed.openshift.io",
				Version: "v1",
				Kind:    "Object",
			},
			Plural:     privatev1.ObjectResourceInfo.Plural,
			Singular:   "object",
			Namespaced: true,
			SchemaYAML: privatev1.ObjectSchemaYAML,
		},
	}

	publicResources := []apiserver.ResourceInfo{
		{
			GVK: runtimeschema.GroupVersionKind{
				Group:   "test.orlop.gcp.managed.openshift.io",
				Version: "v1",
				Kind:    "Object",
			},
			Plural:     publicv1.ObjectResourceInfo.Plural,
			Singular:   "object",
			Namespaced: true,
			SchemaYAML: publicv1.ObjectSchemaYAML,
		},
	}

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

	testServer, err := apiserver.New(opts)
	if err != nil {
		t.Fatalf("Failed to create test server: %v", err)
	}

	publicURL := fmt.Sprintf("http://%s", testServer.PublicAddress())

	// Start server
	go func() {
		if err := testServer.Run(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Logf("Test server error: %v", err)
		}
	}()

	// Wait for server ready
	// Private API uses HTTPS, public API uses HTTP
	privateClient := &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
		Timeout:   2 * time.Second,
	}
	publicClient := &http.Client{
		Timeout: 2 * time.Second,
	}
	deadline := time.Now().Add(10 * time.Second)
	privateURL := fmt.Sprintf("https://localhost%s", testServer.PrivateAddress())
	for time.Now().Before(deadline) {
		resp, err := privateClient.Get(privateURL + "/healthz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Also wait for public API listener
	for time.Now().Before(deadline) {
		resp, err := publicClient.Get(publicURL + "/healthz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Cleanup
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		testServer.Shutdown(ctx)
	})

	t.Run("Public API: Cannot persist finalizers via create", func(t *testing.T) {
		namespace := "default"
		name := "public-api-no-finalizers"

		// Create object with finalizers via public API
		createBody := map[string]interface{}{
			"apiVersion": "test.orlop.gcp.managed.openshift.io/v1",
			"kind":       "Object",
			"metadata": map[string]interface{}{
				"name": name,
				"finalizers": []string{
					"test.orlop.gcp.managed.openshift.io/should-be-stripped",
				},
			},
			"spec": map[string]interface{}{
				"publicField": "value",
				"nested": map[string]interface{}{
					"publicField": "nested-value",
				},
			},
		}
		createJSON, _ := json.Marshal(createBody)
		createResp, err := publicClient.Post(
			publicURL+"/apis/test.orlop.gcp.managed.openshift.io/v1/namespaces/"+namespace+"/objects",
			"application/json",
			bytes.NewBuffer(createJSON),
		)
		if err != nil {
			t.Fatalf("Create request failed: %v", err)
		}
		defer createResp.Body.Close()

		if createResp.StatusCode != http.StatusCreated {
			body, _ := io.ReadAll(createResp.Body)
			t.Fatalf("Expected 201 Created, got %d: %s", createResp.StatusCode, body)
		}

		// Verify public API response does not expose finalizers
		var created map[string]interface{}
		json.NewDecoder(createResp.Body).Decode(&created)
		createdMeta := created["metadata"].(map[string]interface{})
		if _, exists := createdMeta["finalizers"]; exists {
			t.Errorf("Public API create response should not expose finalizers, but found: %v", createdMeta["finalizers"])
		}

		// Read via PRIVATE API to check if finalizers were actually persisted
		privateURL := fmt.Sprintf("https://localhost%s", testServer.PrivateAddress())
		privateGetResp, err := privateClient.Get(privateURL + "/apis/test.orlop.gcp.managed.openshift.io/v1/namespaces/" + namespace + "/objects/" + name)
		if err != nil {
			t.Fatalf("Private API get request failed: %v", err)
		}
		defer privateGetResp.Body.Close()

		if privateGetResp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(privateGetResp.Body)
			t.Fatalf("Expected 200 OK from private API, got %d: %s", privateGetResp.StatusCode, body)
		}

		var privateObj map[string]interface{}
		json.NewDecoder(privateGetResp.Body).Decode(&privateObj)
		privateMeta := privateObj["metadata"].(map[string]interface{})

		// Finalizers sent via public API should NOT have been persisted
		if finalizers, exists := privateMeta["finalizers"]; exists {
			if arr, ok := finalizers.([]interface{}); ok && len(arr) > 0 {
				t.Errorf("Finalizers sent via public API should not be persisted in storage, but found: %v", finalizers)
			}
		}
	})

	t.Run("Public API: Finalizers not exposed in responses", func(t *testing.T) {
		namespace := "default"
		name := "public-finalizer-test-not-exposed"

		// Create object via public API (without finalizers initially)
		createBody := map[string]interface{}{
			"apiVersion": "test.orlop.gcp.managed.openshift.io/v1",
			"kind":       "Object",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": namespace,
			},
			"spec": map[string]interface{}{
				"publicField": "value",
				"nested": map[string]interface{}{
					"publicField": "nested-value",
				},
			},
		}
		createJSON, _ := json.Marshal(createBody)
		createResp, err := publicClient.Post(
			publicURL+"/apis/test.orlop.gcp.managed.openshift.io/v1/namespaces/"+namespace+"/objects",
			"application/json",
			bytes.NewBuffer(createJSON),
		)
		if err != nil {
			t.Fatalf("Create request failed: %v", err)
		}
		defer createResp.Body.Close()

		if createResp.StatusCode != http.StatusCreated {
			body, _ := io.ReadAll(createResp.Body)
			t.Fatalf("Create via public API failed: %d - %s", createResp.StatusCode, body)
		}

		// Add finalizer via private API (mimicking controller behavior)
		privateURL := fmt.Sprintf("https://localhost%s", testServer.PrivateAddress())
		privateGetResp, err := privateClient.Get(privateURL + "/apis/test.orlop.gcp.managed.openshift.io/v1/namespaces/" + namespace + "/objects/" + name)
		if err != nil {
			t.Fatalf("Private API GET failed: %v", err)
		}
		if privateGetResp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(privateGetResp.Body)
			t.Fatalf("Private API GET returned %d: %s", privateGetResp.StatusCode, body)
		}
		var privateObj map[string]interface{}
		json.NewDecoder(privateGetResp.Body).Decode(&privateObj)
		if err := privateGetResp.Body.Close(); err != nil {
			t.Logf("warning: failed to close response body: %v", err)
		}

		privateMetadata := privateObj["metadata"].(map[string]interface{})
		privateMetadata["finalizers"] = []string{"test.orlop.gcp.managed.openshift.io/my-finalizer"}

		updateJSON, _ := json.Marshal(privateObj)
		updateReq, _ := http.NewRequest(http.MethodPut, privateURL+"/apis/test.orlop.gcp.managed.openshift.io/v1/namespaces/"+namespace+"/objects/"+name, bytes.NewBuffer(updateJSON))
		updateReq.Header.Set("Content-Type", "application/json")
		updateResp, err := privateClient.Do(updateReq)
		if err != nil {
			t.Fatalf("HTTP request failed: %v", err)
		}
		if updateResp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(updateResp.Body)
			t.Fatalf("Failed to add finalizer via private API: %d - %s", updateResp.StatusCode, body)
		}
		var updateResult map[string]interface{}
		json.NewDecoder(updateResp.Body).Decode(&updateResult)
		if err := updateResp.Body.Close(); err != nil {
			t.Logf("warning: failed to close response body: %v", err)
		}

		// Debug: check if finalizer was actually set
		updateMeta := updateResult["metadata"].(map[string]interface{})
		if fins, ok := updateMeta["finalizers"]; !ok {
			t.Fatalf("Update response missing finalizers: %+v", updateMeta)
		} else {
			t.Logf("Update response finalizers: %v", fins)
		}

		// Now verify finalizers are NOT exposed via public API
		getResp, err := publicClient.Get(publicURL + "/apis/test.orlop.gcp.managed.openshift.io/v1/namespaces/" + namespace + "/objects/" + name)
		if err != nil {
			t.Fatalf("HTTP request failed: %v", err)
		}
		var publicObj map[string]interface{}
		json.NewDecoder(getResp.Body).Decode(&publicObj)
		if err := getResp.Body.Close(); err != nil {
			t.Logf("warning: failed to close response body: %v", err)
		}

		publicMetadata := publicObj["metadata"].(map[string]interface{})
		if _, exists := publicMetadata["finalizers"]; exists {
			t.Errorf("Finalizers should not be exposed in public API GET response, but found: %v", publicMetadata["finalizers"])
		}

		// Verify finalizers ARE visible in private API
		privateGetResp2, err := privateClient.Get(privateURL + "/apis/test.orlop.gcp.managed.openshift.io/v1/namespaces/" + namespace + "/objects/" + name)
		if err != nil {
			t.Fatalf("HTTP request failed: %v", err)
		}
		var privateObj2 map[string]interface{}
		json.NewDecoder(privateGetResp2.Body).Decode(&privateObj2)
		if err := privateGetResp2.Body.Close(); err != nil {
			t.Logf("warning: failed to close response body: %v", err)
		}

		privateMetadata2 := privateObj2["metadata"].(map[string]interface{})
		finalizers, exists := privateMetadata2["finalizers"]
		if !exists {
			t.Errorf("Finalizers should exist in private API response")
		} else if len(finalizers.([]interface{})) != 1 {
			t.Errorf("Expected 1 finalizer in private API, got %d", len(finalizers.([]interface{})))
		}

		// Cleanup - remove finalizer via private API
		privateMetadata2["finalizers"] = []string{}
		cleanupJSON, _ := json.Marshal(privateObj2)
		cleanupReq, _ := http.NewRequest(http.MethodPut, privateURL+"/apis/test.orlop.gcp.managed.openshift.io/v1/namespaces/"+namespace+"/objects/"+name, bytes.NewBuffer(cleanupJSON))
		cleanupReq.Header.Set("Content-Type", "application/json")
		cleanupResp, err := privateClient.Do(cleanupReq)
		if err != nil {
			t.Fatalf("HTTP request failed: %v", err)
		}
		if err := cleanupResp.Body.Close(); err != nil {
			t.Logf("warning: failed to close response body: %v", err)
		}
	})

	t.Run("Public API: Delete without finalizers - immediate deletion", func(t *testing.T) {
		namespace := "default"
		name := "public-finalizer-test-no-finalizers"

		// Create object without finalizers via public API
		createBody := map[string]interface{}{
			"apiVersion": "test.orlop.gcp.managed.openshift.io/v1",
			"kind":       "Object",
			"metadata": map[string]interface{}{
				"name": name,
			},
			"spec": map[string]interface{}{
				"publicField": "value",
				"nested": map[string]interface{}{
					"publicField": "nested-value",
				},
			},
		}
		createJSON, _ := json.Marshal(createBody)
		createResp, err := publicClient.Post(
			publicURL+"/apis/test.orlop.gcp.managed.openshift.io/v1/namespaces/"+namespace+"/objects",
			"application/json",
			bytes.NewBuffer(createJSON),
		)
		if err != nil {
			t.Fatalf("Create request failed: %v", err)
		}
		defer createResp.Body.Close()

		if createResp.StatusCode != http.StatusCreated {
			body, _ := io.ReadAll(createResp.Body)
			t.Fatalf("Expected 201 Created, got %d: %s", createResp.StatusCode, body)
		}

		// Delete object via public API
		req, _ := http.NewRequest(
			http.MethodDelete,
			publicURL+"/apis/test.orlop.gcp.managed.openshift.io/v1/namespaces/"+namespace+"/objects/"+name,
			nil,
		)
		resp, err := publicClient.Do(req)
		if err != nil {
			t.Fatalf("Delete request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("Expected 200 OK, got %d: %s", resp.StatusCode, body)
		}

		// Verify object is deleted
		getResp, err := publicClient.Get(publicURL + "/apis/test.orlop.gcp.managed.openshift.io/v1/namespaces/" + namespace + "/objects/" + name)
		if err != nil {
			t.Fatalf("Get request failed: %v", err)
		}
		defer getResp.Body.Close()

		if getResp.StatusCode != http.StatusNotFound {
			t.Errorf("Expected 404 Not Found after deletion, got %d", getResp.StatusCode)
		}
	})

	t.Run("Public API: Delete with finalizers - soft deletion (GCP-1060 fix)", func(t *testing.T) {
		namespace := "default"
		name := "public-finalizer-test-with-finalizers"

		// Step 1: Create object via public API (without finalizers)
		createBody := map[string]interface{}{
			"apiVersion": "test.orlop.gcp.managed.openshift.io/v1",
			"kind":       "Object",
			"metadata": map[string]interface{}{
				"name": name,
			},
			"spec": map[string]interface{}{
				"publicField": "value",
				"nested": map[string]interface{}{
					"publicField": "nested-value",
				},
			},
		}
		createJSON, _ := json.Marshal(createBody)
		createResp, err := publicClient.Post(
			publicURL+"/apis/test.orlop.gcp.managed.openshift.io/v1/namespaces/"+namespace+"/objects",
			"application/json",
			bytes.NewBuffer(createJSON),
		)
		if err != nil {
			t.Fatalf("Create request failed: %v", err)
		}
		defer createResp.Body.Close()

		if createResp.StatusCode != http.StatusCreated {
			body, _ := io.ReadAll(createResp.Body)
			t.Fatalf("Expected 201 Created, got %d: %s", createResp.StatusCode, body)
		}

		// Step 2: Add finalizers via private API (mimicking controller behavior)
		privateURL := fmt.Sprintf("https://localhost%s", testServer.PrivateAddress())
		privateGetResp, err := privateClient.Get(privateURL + "/apis/test.orlop.gcp.managed.openshift.io/v1/namespaces/" + namespace + "/objects/" + name)
		if err != nil {
			t.Fatalf("Private API GET failed: %v", err)
		}
		var privateObj map[string]interface{}
		json.NewDecoder(privateGetResp.Body).Decode(&privateObj)
		if err := privateGetResp.Body.Close(); err != nil {
			t.Logf("warning: failed to close response body: %v", err)
		}

		privateMetadata := privateObj["metadata"].(map[string]interface{})
		privateMetadata["finalizers"] = []string{
			"test.orlop.gcp.managed.openshift.io/finalizer-1",
			"test.orlop.gcp.managed.openshift.io/finalizer-2",
		}

		updateJSON, _ := json.Marshal(privateObj)
		updateReq, _ := http.NewRequest(http.MethodPut, privateURL+"/apis/test.orlop.gcp.managed.openshift.io/v1/namespaces/"+namespace+"/objects/"+name, bytes.NewBuffer(updateJSON))
		updateReq.Header.Set("Content-Type", "application/json")
		updateResp, err := privateClient.Do(updateReq)
		if err != nil {
			t.Fatalf("Failed to add finalizers via private API: %v", err)
		}
		if err := updateResp.Body.Close(); err != nil {
			t.Logf("warning: failed to close response body: %v", err)
		}

		if updateResp.StatusCode != http.StatusOK {
			t.Fatalf("Expected 200 OK adding finalizers via private API, got %d", updateResp.StatusCode)
		}

		// Step 3: Delete object via public API (should set deletionTimestamp, NOT hard delete)
		req, _ := http.NewRequest(
			http.MethodDelete,
			publicURL+"/apis/test.orlop.gcp.managed.openshift.io/v1/namespaces/"+namespace+"/objects/"+name,
			nil,
		)
		resp, err := publicClient.Do(req)
		if err != nil {
			t.Fatalf("Delete request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("Expected 200 OK, got %d: %s", resp.StatusCode, body)
		}

		// Verify response contains the object with deletionTimestamp
		var deletedObj map[string]interface{}
		if err := json.NewDecoder(resp.Body).Decode(&deletedObj); err != nil {
			t.Fatalf("Failed to decode response: %v", err)
		}

		metadata := deletedObj["metadata"].(map[string]interface{})
		if metadata["deletionTimestamp"] == nil {
			t.Errorf("GCP-1060 regression: Expected deletionTimestamp to be set for object with finalizers")
		}

		// Finalizers should NOT be exposed in public API response
		if _, exists := metadata["finalizers"]; exists {
			t.Errorf("Finalizers should not be exposed in public API, but found: %v", metadata["finalizers"])
		}

		// Verify object still exists (soft deleted, NOT hard deleted)
		getResp, err := publicClient.Get(publicURL + "/apis/test.orlop.gcp.managed.openshift.io/v1/namespaces/" + namespace + "/objects/" + name)
		if err != nil {
			t.Fatalf("Get request failed: %v", err)
		}
		defer getResp.Body.Close()

		if getResp.StatusCode != http.StatusOK {
			t.Errorf("GCP-1060 regression: Object with finalizers was hard-deleted instead of soft-deleted (expected 200 OK, got %d)", getResp.StatusCode)
		}

		var getObj map[string]interface{}
		if err := json.NewDecoder(getResp.Body).Decode(&getObj); err != nil {
			t.Fatalf("Failed to decode get response: %v", err)
		}

		getMetadata := getObj["metadata"].(map[string]interface{})
		if getMetadata["deletionTimestamp"] == nil {
			t.Errorf("Expected deletionTimestamp to be present on soft-deleted object")
		}
	})

	t.Run("Public API: Remove finalizers - triggers hard deletion (GCP-1060 fix)", func(t *testing.T) {
		namespace := "default"
		name := "public-finalizer-test-removal"

		// Step 1: Create object via public API (without finalizers)
		createBody := map[string]interface{}{
			"apiVersion": "test.orlop.gcp.managed.openshift.io/v1",
			"kind":       "Object",
			"metadata": map[string]interface{}{
				"name": name,
			},
			"spec": map[string]interface{}{
				"publicField": "value",
				"nested": map[string]interface{}{
					"publicField": "nested-value",
				},
			},
		}
		createJSON, _ := json.Marshal(createBody)
		createResp, err := publicClient.Post(
			publicURL+"/apis/test.orlop.gcp.managed.openshift.io/v1/namespaces/"+namespace+"/objects",
			"application/json",
			bytes.NewBuffer(createJSON),
		)
		if err != nil {
			t.Fatalf("Create request failed: %v", err)
		}
		defer createResp.Body.Close()

		// Step 2: Add finalizer via private API (mimicking controller behavior)
		privateURL := fmt.Sprintf("https://localhost%s", testServer.PrivateAddress())
		privateGetResp, err := privateClient.Get(privateURL + "/apis/test.orlop.gcp.managed.openshift.io/v1/namespaces/" + namespace + "/objects/" + name)
		if err != nil {
			t.Fatalf("HTTP request failed: %v", err)
		}
		var privateObj map[string]interface{}
		json.NewDecoder(privateGetResp.Body).Decode(&privateObj)
		if err := privateGetResp.Body.Close(); err != nil {
			t.Logf("warning: failed to close response body: %v", err)
		}

		privateMetadata := privateObj["metadata"].(map[string]interface{})
		privateMetadata["finalizers"] = []string{
			"test.orlop.gcp.managed.openshift.io/my-finalizer",
		}

		addFinJSON, _ := json.Marshal(privateObj)
		addFinReq, _ := http.NewRequest(http.MethodPut, privateURL+"/apis/test.orlop.gcp.managed.openshift.io/v1/namespaces/"+namespace+"/objects/"+name, bytes.NewBuffer(addFinJSON))
		addFinReq.Header.Set("Content-Type", "application/json")
		addFinResp, err := privateClient.Do(addFinReq)
		if err != nil {
			t.Fatalf("Failed to add finalizer via private API: %v", err)
		}
		if err := addFinResp.Body.Close(); err != nil {
			t.Logf("warning: failed to close response body: %v", err)
		}

		// Step 3: Soft delete via public API (set deletionTimestamp)
		req, _ := http.NewRequest(
			http.MethodDelete,
			publicURL+"/apis/test.orlop.gcp.managed.openshift.io/v1/namespaces/"+namespace+"/objects/"+name,
			nil,
		)
		resp, err := publicClient.Do(req)
		if err != nil {
			t.Fatalf("Delete request failed: %v", err)
		}
		if err := resp.Body.Close(); err != nil {
			t.Logf("warning: failed to close response body: %v", err)
		}

		// Get the object to verify soft deletion
		getResp, err := publicClient.Get(publicURL + "/apis/test.orlop.gcp.managed.openshift.io/v1/namespaces/" + namespace + "/objects/" + name)
		if err != nil {
			t.Fatalf("HTTP request failed: %v", err)
		}
		var softDeleted map[string]interface{}
		json.NewDecoder(getResp.Body).Decode(&softDeleted)
		if err := getResp.Body.Close(); err != nil {
			t.Logf("warning: failed to close response body: %v", err)
		}

		metadata := softDeleted["metadata"].(map[string]interface{})
		if metadata["deletionTimestamp"] == nil {
			t.Fatalf("Expected deletionTimestamp to be set after soft delete via public API")
		}

		// Step 4: Now remove finalizers via PRIVATE API (mimicking controller behavior)
		privateGetResp2, err := privateClient.Get(privateURL + "/apis/test.orlop.gcp.managed.openshift.io/v1/namespaces/" + namespace + "/objects/" + name)
		if err != nil {
			t.Fatalf("HTTP request failed: %v", err)
		}
		var privateObj2 map[string]interface{}
		json.NewDecoder(privateGetResp2.Body).Decode(&privateObj2)
		if err := privateGetResp2.Body.Close(); err != nil {
			t.Logf("warning: failed to close response body: %v", err)
		}

		// Remove finalizers
		privateMetadata2 := privateObj2["metadata"].(map[string]interface{})
		privateMetadata2["finalizers"] = []string{}

		updateJSON, _ := json.Marshal(privateObj2)
		updateReq, _ := http.NewRequest(
			http.MethodPut,
			privateURL+"/apis/test.orlop.gcp.managed.openshift.io/v1/namespaces/"+namespace+"/objects/"+name,
			bytes.NewBuffer(updateJSON),
		)
		updateReq.Header.Set("Content-Type", "application/json")

		updateResp, err := privateClient.Do(updateReq)
		if err != nil {
			t.Fatalf("Private API update request failed: %v", err)
		}
		defer updateResp.Body.Close()

		if updateResp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(updateResp.Body)
			t.Fatalf("Expected 200 OK for finalizer removal via private API, got %d: %s", updateResp.StatusCode, body)
		}

		// Step 5: Verify object is hard deleted (check via public API)
		finalGetResp, err := publicClient.Get(publicURL + "/apis/test.orlop.gcp.managed.openshift.io/v1/namespaces/" + namespace + "/objects/" + name)
		if err != nil {
			t.Fatalf("Final get request failed: %v", err)
		}
		defer finalGetResp.Body.Close()

		if finalGetResp.StatusCode != http.StatusNotFound {
			body, _ := io.ReadAll(finalGetResp.Body)
			t.Errorf("GCP-1060 fix validation: Expected 404 Not Found after finalizer removal, got %d: %s", finalGetResp.StatusCode, body)
		}
	})

	t.Run("Public API: Second delete on soft-deleted object - idempotent", func(t *testing.T) {
		namespace := "default"
		name := "public-finalizer-test-idempotent"

		// Step 1: Create object via public API (without finalizers)
		createBody := map[string]interface{}{
			"apiVersion": "test.orlop.gcp.managed.openshift.io/v1",
			"kind":       "Object",
			"metadata": map[string]interface{}{
				"name": name,
			},
			"spec": map[string]interface{}{
				"publicField": "value",
				"nested": map[string]interface{}{
					"publicField": "nested-value",
				},
			},
		}
		createJSON, _ := json.Marshal(createBody)
		createResp, err := publicClient.Post(
			publicURL+"/apis/test.orlop.gcp.managed.openshift.io/v1/namespaces/"+namespace+"/objects",
			"application/json",
			bytes.NewBuffer(createJSON),
		)
		if err != nil {
			t.Fatalf("Create request failed: %v", err)
		}
		if err := createResp.Body.Close(); err != nil {
			t.Logf("warning: failed to close response body: %v", err)
		}

		// Step 2: Add finalizer via private API (mimicking controller behavior)
		privateURL := fmt.Sprintf("https://localhost%s", testServer.PrivateAddress())
		privateGetResp, err := privateClient.Get(privateURL + "/apis/test.orlop.gcp.managed.openshift.io/v1/namespaces/" + namespace + "/objects/" + name)
		if err != nil {
			t.Fatalf("HTTP request failed: %v", err)
		}
		var privateObj map[string]interface{}
		json.NewDecoder(privateGetResp.Body).Decode(&privateObj)
		if err := privateGetResp.Body.Close(); err != nil {
			t.Logf("warning: failed to close response body: %v", err)
		}

		privateMetadata := privateObj["metadata"].(map[string]interface{})
		privateMetadata["finalizers"] = []string{
			"test.orlop.gcp.managed.openshift.io/my-finalizer",
		}

		addFinJSON, _ := json.Marshal(privateObj)
		addFinReq, _ := http.NewRequest(http.MethodPut, privateURL+"/apis/test.orlop.gcp.managed.openshift.io/v1/namespaces/"+namespace+"/objects/"+name, bytes.NewBuffer(addFinJSON))
		addFinReq.Header.Set("Content-Type", "application/json")
		addFinResp, err := privateClient.Do(addFinReq)
		if err != nil {
			t.Fatalf("HTTP request failed: %v", err)
		}
		if err := addFinResp.Body.Close(); err != nil {
			t.Logf("warning: failed to close response body: %v", err)
		}

		// Step 3: First delete via public API — should soft-delete (set deletionTimestamp)
		req1, _ := http.NewRequest(
			http.MethodDelete,
			publicURL+"/apis/test.orlop.gcp.managed.openshift.io/v1/namespaces/"+namespace+"/objects/"+name,
			nil,
		)
		resp1, err := publicClient.Do(req1)
		if err != nil {
			t.Fatalf("First delete request failed: %v", err)
		}
		var resp1Body map[string]interface{}
		json.NewDecoder(resp1.Body).Decode(&resp1Body)
		if err := resp1.Body.Close(); err != nil {
			t.Logf("warning: failed to close response body: %v", err)
		}
		if resp1.StatusCode != http.StatusOK {
			t.Fatalf("First delete: expected 200, got %d: %v", resp1.StatusCode, resp1Body)
		}
		resp1Meta, _ := resp1Body["metadata"].(map[string]interface{})
		if resp1Meta["deletionTimestamp"] == nil {
			t.Error("First delete response should contain deletionTimestamp (soft-delete)")
		}

		// Step 4: Second delete (should still succeed but not change anything)
		req2, _ := http.NewRequest(
			http.MethodDelete,
			publicURL+"/apis/test.orlop.gcp.managed.openshift.io/v1/namespaces/"+namespace+"/objects/"+name,
			nil,
		)
		resp2, err := publicClient.Do(req2)
		if err != nil {
			t.Fatalf("Second delete request failed: %v", err)
		}
		defer resp2.Body.Close()

		if resp2.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp2.Body)
			t.Fatalf("Expected 200 OK for second delete, got %d: %s", resp2.StatusCode, body)
		}

		// Object should still exist with deletionTimestamp
		getResp, err := publicClient.Get(publicURL + "/apis/test.orlop.gcp.managed.openshift.io/v1/namespaces/" + namespace + "/objects/" + name)
		if err != nil {
			t.Fatalf("HTTP request failed: %v", err)
		}
		defer getResp.Body.Close()

		if getResp.StatusCode != http.StatusOK {
			t.Errorf("Expected object to still exist after second delete, got %d", getResp.StatusCode)
		}
	})

	t.Run("Public API: Patch preserves deletionTimestamp on soft-deleted objects", func(t *testing.T) {
		namespace := "default"
		name := "public-patch-deletion-test"

		// Step 1: Create object via public API
		createBody := map[string]interface{}{
			"apiVersion": "test.orlop.gcp.managed.openshift.io/v1",
			"kind":       "Object",
			"metadata": map[string]interface{}{
				"name": name,
			},
			"spec": map[string]interface{}{
				"publicField": "value",
				"nested": map[string]interface{}{
					"publicField": "nested-value",
				},
			},
		}
		createJSON, _ := json.Marshal(createBody)
		createResp, err := publicClient.Post(
			publicURL+"/apis/test.orlop.gcp.managed.openshift.io/v1/namespaces/"+namespace+"/objects",
			"application/json",
			bytes.NewBuffer(createJSON),
		)
		if err != nil {
			t.Fatalf("Create request failed: %v", err)
		}
		if err := createResp.Body.Close(); err != nil {
			t.Logf("warning: failed to close response body: %v", err)
		}

		// Step 2: Add finalizer via private API
		privateURL := fmt.Sprintf("https://localhost%s", testServer.PrivateAddress())
		privateGetResp, err := privateClient.Get(privateURL + "/apis/test.orlop.gcp.managed.openshift.io/v1/namespaces/" + namespace + "/objects/" + name)
		if err != nil {
			t.Fatalf("HTTP request failed: %v", err)
		}
		var privateObj map[string]interface{}
		json.NewDecoder(privateGetResp.Body).Decode(&privateObj)
		if err := privateGetResp.Body.Close(); err != nil {
			t.Logf("warning: failed to close response body: %v", err)
		}

		privateMetadata := privateObj["metadata"].(map[string]interface{})
		privateMetadata["finalizers"] = []string{
			"test.orlop.gcp.managed.openshift.io/my-finalizer",
		}

		addFinJSON, _ := json.Marshal(privateObj)
		addFinReq, _ := http.NewRequest(http.MethodPut, privateURL+"/apis/test.orlop.gcp.managed.openshift.io/v1/namespaces/"+namespace+"/objects/"+name, bytes.NewBuffer(addFinJSON))
		addFinReq.Header.Set("Content-Type", "application/json")
		addFinResp, err := privateClient.Do(addFinReq)
		if err != nil {
			t.Fatalf("HTTP request failed: %v", err)
		}
		if err := addFinResp.Body.Close(); err != nil {
			t.Logf("warning: failed to close response body: %v", err)
		}

		// Step 3: Soft delete via public API
		delReq, _ := http.NewRequest(http.MethodDelete, publicURL+"/apis/test.orlop.gcp.managed.openshift.io/v1/namespaces/"+namespace+"/objects/"+name, nil)
		delResp, err := publicClient.Do(delReq)
		if err != nil {
			t.Fatalf("HTTP request failed: %v", err)
		}
		if err := delResp.Body.Close(); err != nil {
			t.Logf("warning: failed to close response body: %v", err)
		}

		// Step 4: Patch the object via public API — should preserve deletionTimestamp
		patchBody := map[string]interface{}{
			"spec": map[string]interface{}{
				"publicField": "patched-value",
			},
		}
		patchJSON, _ := json.Marshal(patchBody)
		patchReq, _ := http.NewRequest(
			http.MethodPatch,
			publicURL+"/apis/test.orlop.gcp.managed.openshift.io/v1/namespaces/"+namespace+"/objects/"+name,
			bytes.NewBuffer(patchJSON),
		)
		patchReq.Header.Set("Content-Type", "application/merge-patch+json")
		patchResp, err := publicClient.Do(patchReq)
		if err != nil {
			t.Fatalf("Patch request failed: %v", err)
		}
		defer patchResp.Body.Close()

		if patchResp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(patchResp.Body)
			t.Fatalf("Expected 200 OK for patch, got %d: %s", patchResp.StatusCode, body)
		}

		// Step 5: Verify via private API that deletionTimestamp is still set
		privateGetResp2, err := privateClient.Get(privateURL + "/apis/test.orlop.gcp.managed.openshift.io/v1/namespaces/" + namespace + "/objects/" + name)
		if err != nil {
			t.Fatalf("HTTP request failed: %v", err)
		}
		var privateObj2 map[string]interface{}
		json.NewDecoder(privateGetResp2.Body).Decode(&privateObj2)
		if err := privateGetResp2.Body.Close(); err != nil {
			t.Logf("warning: failed to close response body: %v", err)
		}

		privateMeta2 := privateObj2["metadata"].(map[string]interface{})
		if privateMeta2["deletionTimestamp"] == nil {
			t.Error("deletionTimestamp should be preserved after patch on soft-deleted object")
		}

		// Step 6: Remove finalizers via private API — should trigger hard delete
		privateMeta2["finalizers"] = []string{}
		removeFinJSON, _ := json.Marshal(privateObj2)
		removeFinReq, _ := http.NewRequest(http.MethodPut, privateURL+"/apis/test.orlop.gcp.managed.openshift.io/v1/namespaces/"+namespace+"/objects/"+name, bytes.NewBuffer(removeFinJSON))
		removeFinReq.Header.Set("Content-Type", "application/json")
		removeFinResp, err := privateClient.Do(removeFinReq)
		if err != nil {
			t.Fatalf("HTTP request failed: %v", err)
		}
		if err := removeFinResp.Body.Close(); err != nil {
			t.Logf("warning: failed to close response body: %v", err)
		}

		// Step 7: Verify hard deletion
		finalGetResp, err := publicClient.Get(publicURL + "/apis/test.orlop.gcp.managed.openshift.io/v1/namespaces/" + namespace + "/objects/" + name)
		if err != nil {
			t.Fatalf("HTTP request failed: %v", err)
		}
		defer finalGetResp.Body.Close()

		if finalGetResp.StatusCode != http.StatusNotFound {
			body, _ := io.ReadAll(finalGetResp.Body)
			t.Errorf("Expected 404 after finalizers removed, got %d: %s", finalGetResp.StatusCode, body)
		}
	})
}
