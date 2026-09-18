package test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"
)

// TestFinalizerDeletion tests the finalizer deletion flow
func TestFinalizerDeletion(t *testing.T) {
	t.Run("Delete without finalizers - immediate deletion", func(t *testing.T) {
		namespace := "default"
		name := "finalizer-test-no-finalizers"

		// Create object without finalizers
		createBody := map[string]interface{}{
			"apiVersion": "test.orlop.gcp.managed.openshift.io/v1",
			"kind":       "Object",
			"metadata": map[string]interface{}{
				"name": name,
			},
			"spec": map[string]interface{}{
				"publicField":   "value",
				"internalField": "value",
				"nested": map[string]interface{}{
					"publicField":   "nested-value",
					"internalField": "nested-value",
				},
			},
		}
		createJSON, _ := json.Marshal(createBody)
		createResp, err := insecureClient.Post(
			baseURL+"/apis/test.orlop.gcp.managed.openshift.io/v1/namespaces/"+namespace+"/objects",
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

		// Delete object
		req, _ := http.NewRequest(
			http.MethodDelete,
			baseURL+"/apis/test.orlop.gcp.managed.openshift.io/v1/namespaces/"+namespace+"/objects/"+name,
			nil,
		)
		resp, err := insecureClient.Do(req)
		if err != nil {
			t.Fatalf("Delete request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("Expected 200 OK, got %d: %s", resp.StatusCode, body)
		}

		// Verify object is deleted
		getResp, err := insecureClient.Get(baseURL + "/apis/test.orlop.gcp.managed.openshift.io/v1/namespaces/" + namespace + "/objects/" + name)
		if err != nil {
			t.Fatalf("Get request failed: %v", err)
		}
		defer getResp.Body.Close()

		if getResp.StatusCode != http.StatusNotFound {
			t.Errorf("Expected 404 Not Found after deletion, got %d", getResp.StatusCode)
		}
	})

	t.Run("Delete with finalizers - soft deletion", func(t *testing.T) {
		namespace := "default"
		name := "finalizer-test-with-finalizers"

		// Create object with finalizers
		createBody := map[string]interface{}{
			"apiVersion": "test.orlop.gcp.managed.openshift.io/v1",
			"kind":       "Object",
			"metadata": map[string]interface{}{
				"name": name,
				"finalizers": []string{
					"test.orlop.gcp.managed.openshift.io/finalizer-1",
					"test.orlop.gcp.managed.openshift.io/finalizer-2",
				},
			},
			"spec": map[string]interface{}{
				"publicField":   "value",
				"internalField": "value",
				"nested": map[string]interface{}{
					"publicField":   "nested-value",
					"internalField": "nested-value",
				},
			},
		}
		createJSON, _ := json.Marshal(createBody)
		createResp, err := insecureClient.Post(
			baseURL+"/apis/test.orlop.gcp.managed.openshift.io/v1/namespaces/"+namespace+"/objects",
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

		// Delete object (should set deletionTimestamp)
		req, _ := http.NewRequest(
			http.MethodDelete,
			baseURL+"/apis/test.orlop.gcp.managed.openshift.io/v1/namespaces/"+namespace+"/objects/"+name,
			nil,
		)
		resp, err := insecureClient.Do(req)
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
			t.Errorf("Expected deletionTimestamp to be set")
		}

		finalizers := metadata["finalizers"].([]interface{})
		if len(finalizers) != 2 {
			t.Errorf("Expected 2 finalizers, got %d", len(finalizers))
		}

		// Verify object still exists (soft deleted)
		getResp, err := insecureClient.Get(baseURL + "/apis/test.orlop.gcp.managed.openshift.io/v1/namespaces/" + namespace + "/objects/" + name)
		if err != nil {
			t.Fatalf("Get request failed: %v", err)
		}
		defer getResp.Body.Close()

		if getResp.StatusCode != http.StatusOK {
			t.Errorf("Expected 200 OK (object should still exist), got %d", getResp.StatusCode)
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

	t.Run("Remove finalizers - triggers hard deletion", func(t *testing.T) {
		namespace := "default"
		name := "finalizer-test-removal"

		// Create object with one finalizer
		createBody := map[string]interface{}{
			"apiVersion": "test.orlop.gcp.managed.openshift.io/v1",
			"kind":       "Object",
			"metadata": map[string]interface{}{
				"name": name,
				"finalizers": []string{
					"test.orlop.gcp.managed.openshift.io/my-finalizer",
				},
			},
			"spec": map[string]interface{}{
				"publicField":   "value",
				"internalField": "value",
				"nested": map[string]interface{}{
					"publicField":   "nested-value",
					"internalField": "nested-value",
				},
			},
		}
		createJSON, _ := json.Marshal(createBody)
		createResp, err := insecureClient.Post(
			baseURL+"/apis/test.orlop.gcp.managed.openshift.io/v1/namespaces/"+namespace+"/objects",
			"application/json",
			bytes.NewBuffer(createJSON),
		)
		if err != nil {
			t.Fatalf("Create request failed: %v", err)
		}
		defer createResp.Body.Close()

		var created map[string]interface{}
		json.NewDecoder(createResp.Body).Decode(&created)
		_ = created["metadata"].(map[string]interface{})["resourceVersion"].(string)

		// Soft delete (set deletionTimestamp)
		req, _ := http.NewRequest(
			http.MethodDelete,
			baseURL+"/apis/test.orlop.gcp.managed.openshift.io/v1/namespaces/"+namespace+"/objects/"+name,
			nil,
		)
		resp, err := insecureClient.Do(req)
		if err != nil {
			t.Fatalf("Delete request failed: %v", err)
		}
		if err := resp.Body.Close(); err != nil {
			t.Logf("warning: failed to close response body: %v", err)
		}

		// Get the object to verify soft deletion
		getResp, _ := insecureClient.Get(baseURL + "/apis/test.orlop.gcp.managed.openshift.io/v1/namespaces/" + namespace + "/objects/" + name)
		var softDeleted map[string]interface{}
		json.NewDecoder(getResp.Body).Decode(&softDeleted)
		if err := getResp.Body.Close(); err != nil {
			t.Logf("warning: failed to close response body: %v", err)
		}

		metadata := softDeleted["metadata"].(map[string]interface{})
		if metadata["deletionTimestamp"] == nil {
			t.Fatalf("Expected deletionTimestamp to be set after soft delete")
		}
		updatedResourceVersion := metadata["resourceVersion"].(string)

		// Remove the finalizer (should trigger hard deletion)
		updateBody := softDeleted
		updateMetadata := updateBody["metadata"].(map[string]interface{})
		updateMetadata["finalizers"] = []string{} // Remove all finalizers
		updateMetadata["resourceVersion"] = updatedResourceVersion

		updateJSON, _ := json.Marshal(updateBody)
		updateReq, _ := http.NewRequest(
			http.MethodPut,
			baseURL+"/apis/test.orlop.gcp.managed.openshift.io/v1/namespaces/"+namespace+"/objects/"+name,
			bytes.NewBuffer(updateJSON),
		)
		updateReq.Header.Set("Content-Type", "application/json")

		updateResp, err := insecureClient.Do(updateReq)
		if err != nil {
			t.Fatalf("Update request failed: %v", err)
		}
		defer updateResp.Body.Close()

		if updateResp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(updateResp.Body)
			t.Fatalf("Expected 200 OK for finalizer removal, got %d: %s", updateResp.StatusCode, body)
		}

		// GenericAPIServer returns the updated object (not a Status) from PUT.
		// The hard deletion happens internally but the response is the last state of the object.
		var result map[string]interface{}
		if err := json.NewDecoder(updateResp.Body).Decode(&result); err != nil {
			t.Fatalf("Failed to decode response: %v", err)
		}

		if result["kind"] != "Object" {
			t.Errorf("Expected Object response after finalizer removal update, got %v", result["kind"])
		}

		// Wait a moment for deletion to complete
		time.Sleep(100 * time.Millisecond)

		// Verify object is hard deleted
		finalGetResp, err := insecureClient.Get(baseURL + "/apis/test.orlop.gcp.managed.openshift.io/v1/namespaces/" + namespace + "/objects/" + name)
		if err != nil {
			t.Fatalf("Final get request failed: %v", err)
		}
		defer finalGetResp.Body.Close()

		if finalGetResp.StatusCode != http.StatusNotFound {
			body, _ := io.ReadAll(finalGetResp.Body)
			t.Errorf("Expected 404 Not Found after finalizer removal, got %d: %s", finalGetResp.StatusCode, body)
		}
	})

	t.Run("Patch preserves deletionTimestamp on soft-deleted object", func(t *testing.T) {
		namespace := "default"
		name := "finalizer-test-patch-preserve-dt"

		// Create object with finalizer
		createBody := map[string]interface{}{
			"apiVersion": "test.orlop.gcp.managed.openshift.io/v1",
			"kind":       "Object",
			"metadata": map[string]interface{}{
				"name": name,
				"finalizers": []string{
					"test.orlop.gcp.managed.openshift.io/keep-finalizer",
				},
			},
			"spec": map[string]interface{}{
				"publicField":   "original",
				"internalField": "internal-value",
				"nested": map[string]interface{}{
					"publicField":   "nested",
					"internalField": "nested-internal",
				},
			},
		}
		createJSON, _ := json.Marshal(createBody)
		createResp, err := insecureClient.Post(
			baseURL+"/apis/test.orlop.gcp.managed.openshift.io/v1/namespaces/"+namespace+"/objects",
			"application/json",
			bytes.NewBuffer(createJSON),
		)
		if err != nil {
			t.Fatalf("Create request failed: %v", err)
		}
		if err := createResp.Body.Close(); err != nil {
			t.Logf("warning: failed to close response body: %v", err)
		}

		if createResp.StatusCode != http.StatusCreated {
			t.Fatalf("Expected 201, got %d", createResp.StatusCode)
		}

		// Soft-delete (sets deletionTimestamp)
		delReq, _ := http.NewRequest(
			http.MethodDelete,
			baseURL+"/apis/test.orlop.gcp.managed.openshift.io/v1/namespaces/"+namespace+"/objects/"+name,
			nil,
		)
		delResp, err := insecureClient.Do(delReq)
		if err != nil {
			t.Fatalf("Delete request failed: %v", err)
		}
		if err := delResp.Body.Close(); err != nil {
			t.Logf("warning: failed to close response body: %v", err)
		}

		// Verify soft-deleted
		getResp, _ := insecureClient.Get(baseURL + "/apis/test.orlop.gcp.managed.openshift.io/v1/namespaces/" + namespace + "/objects/" + name)
		var softDeleted map[string]interface{}
		json.NewDecoder(getResp.Body).Decode(&softDeleted)
		if err := getResp.Body.Close(); err != nil {
			t.Logf("warning: failed to close response body: %v", err)
		}

		metadata := softDeleted["metadata"].(map[string]interface{})
		if metadata["deletionTimestamp"] == nil {
			t.Fatalf("Expected deletionTimestamp after soft delete")
		}

		// Patch spec only (omitting metadata.deletionTimestamp)
		patchBody := map[string]interface{}{
			"spec": map[string]interface{}{
				"publicField": "patched-value",
			},
		}
		patchJSON, _ := json.Marshal(patchBody)
		patchReq, _ := http.NewRequest(
			http.MethodPatch,
			baseURL+"/apis/test.orlop.gcp.managed.openshift.io/v1/namespaces/"+namespace+"/objects/"+name,
			bytes.NewBuffer(patchJSON),
		)
		patchReq.Header.Set("Content-Type", "application/merge-patch+json")

		patchResp, err := insecureClient.Do(patchReq)
		if err != nil {
			t.Fatalf("Patch request failed: %v", err)
		}
		defer patchResp.Body.Close()

		if patchResp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(patchResp.Body)
			t.Fatalf("Expected 200 OK, got %d: %s", patchResp.StatusCode, body)
		}

		var patchedObj map[string]interface{}
		json.NewDecoder(patchResp.Body).Decode(&patchedObj)

		// Verify deletionTimestamp preserved
		patchedMeta := patchedObj["metadata"].(map[string]interface{})
		if patchedMeta["deletionTimestamp"] == nil {
			t.Error("Expected deletionTimestamp to be preserved after Patch — bug #24")
		}

		// Verify spec was actually updated
		spec := patchedObj["spec"].(map[string]interface{})
		if spec["publicField"] != "patched-value" {
			t.Errorf("Expected publicField 'patched-value', got %v", spec["publicField"])
		}

		// Verify finalizers preserved
		finalizers := patchedMeta["finalizers"].([]interface{})
		if len(finalizers) != 1 {
			t.Errorf("Expected 1 finalizer, got %d", len(finalizers))
		}
	})

	t.Run("Remove finalizers via Patch - triggers hard deletion", func(t *testing.T) {
		namespace := "default"
		name := "finalizer-test-patch-removal"

		// Create object with finalizer
		createBody := map[string]interface{}{
			"apiVersion": "test.orlop.gcp.managed.openshift.io/v1",
			"kind":       "Object",
			"metadata": map[string]interface{}{
				"name": name,
				"finalizers": []string{
					"test.orlop.gcp.managed.openshift.io/remove-me",
				},
			},
			"spec": map[string]interface{}{
				"publicField":   "value",
				"internalField": "value",
				"nested": map[string]interface{}{
					"publicField":   "nested-value",
					"internalField": "nested-value",
				},
			},
		}
		createJSON, _ := json.Marshal(createBody)
		createResp, err := insecureClient.Post(
			baseURL+"/apis/test.orlop.gcp.managed.openshift.io/v1/namespaces/"+namespace+"/objects",
			"application/json",
			bytes.NewBuffer(createJSON),
		)
		if err != nil {
			t.Fatalf("Create request failed: %v", err)
		}
		if err := createResp.Body.Close(); err != nil {
			t.Logf("warning: failed to close response body: %v", err)
		}

		// Soft-delete
		delReq, _ := http.NewRequest(
			http.MethodDelete,
			baseURL+"/apis/test.orlop.gcp.managed.openshift.io/v1/namespaces/"+namespace+"/objects/"+name,
			nil,
		)
		delResp, err := insecureClient.Do(delReq)
		if err != nil {
			t.Fatalf("Delete request failed: %v", err)
		}
		if err := delResp.Body.Close(); err != nil {
			t.Logf("warning: failed to close response body: %v", err)
		}

		// Verify soft-deleted
		getResp, _ := insecureClient.Get(baseURL + "/apis/test.orlop.gcp.managed.openshift.io/v1/namespaces/" + namespace + "/objects/" + name)
		var softDeleted map[string]interface{}
		json.NewDecoder(getResp.Body).Decode(&softDeleted)
		if err := getResp.Body.Close(); err != nil {
			t.Logf("warning: failed to close response body: %v", err)
		}

		if softDeleted["metadata"].(map[string]interface{})["deletionTimestamp"] == nil {
			t.Fatalf("Expected deletionTimestamp after soft delete")
		}

		// Remove finalizers via Patch — should trigger hard deletion
		patchBody := map[string]interface{}{
			"metadata": map[string]interface{}{
				"finalizers": []string{}, // empty = remove all
			},
		}
		patchJSON, _ := json.Marshal(patchBody)
		patchReq, _ := http.NewRequest(
			http.MethodPatch,
			baseURL+"/apis/test.orlop.gcp.managed.openshift.io/v1/namespaces/"+namespace+"/objects/"+name,
			bytes.NewBuffer(patchJSON),
		)
		patchReq.Header.Set("Content-Type", "application/merge-patch+json")

		patchResp, err := insecureClient.Do(patchReq)
		if err != nil {
			t.Fatalf("Patch request failed: %v", err)
		}
		defer patchResp.Body.Close()

		if patchResp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(patchResp.Body)
			t.Fatalf("Expected 200 OK, got %d: %s", patchResp.StatusCode, body)
		}

		// Wait for deletion
		time.Sleep(100 * time.Millisecond)

		// Verify hard deleted
		finalGet, err := insecureClient.Get(baseURL + "/apis/test.orlop.gcp.managed.openshift.io/v1/namespaces/" + namespace + "/objects/" + name)
		if err != nil {
			t.Fatalf("Final get request failed: %v", err)
		}
		defer finalGet.Body.Close()

		if finalGet.StatusCode != http.StatusNotFound {
			body, _ := io.ReadAll(finalGet.Body)
			t.Errorf("Expected 404 after finalizer removal via Patch, got %d: %s", finalGet.StatusCode, body)
		}
	})

	t.Run("Second delete on soft-deleted object - idempotent", func(t *testing.T) {
		namespace := "default"
		name := "finalizer-test-idempotent"

		// Create object with finalizer
		createBody := map[string]interface{}{
			"apiVersion": "test.orlop.gcp.managed.openshift.io/v1",
			"kind":       "Object",
			"metadata": map[string]interface{}{
				"name": name,
				"finalizers": []string{
					"test.orlop.gcp.managed.openshift.io/finalizer",
				},
			},
			"spec": map[string]interface{}{
				"publicField":   "value",
				"internalField": "value",
				"nested": map[string]interface{}{
					"publicField":   "nested-value",
					"internalField": "nested-value",
				},
			},
		}
		createJSON, _ := json.Marshal(createBody)
		createResp, err := insecureClient.Post(
			baseURL+"/apis/test.orlop.gcp.managed.openshift.io/v1/namespaces/"+namespace+"/objects",
			"application/json",
			bytes.NewBuffer(createJSON),
		)
		if err != nil {
			t.Fatalf("Create request failed: %v", err)
		}
		if err := createResp.Body.Close(); err != nil {
			t.Logf("warning: failed to close response body: %v", err)
		}

		// First delete
		req1, _ := http.NewRequest(
			http.MethodDelete,
			baseURL+"/apis/test.orlop.gcp.managed.openshift.io/v1/namespaces/"+namespace+"/objects/"+name,
			nil,
		)
		resp1, err := insecureClient.Do(req1)
		if err != nil {
			t.Fatalf("First delete request failed: %v", err)
		}
		if err := resp1.Body.Close(); err != nil {
			t.Logf("warning: failed to close response body: %v", err)
		}

		// Second delete (should still succeed but not change anything)
		req2, _ := http.NewRequest(
			http.MethodDelete,
			baseURL+"/apis/test.orlop.gcp.managed.openshift.io/v1/namespaces/"+namespace+"/objects/"+name,
			nil,
		)
		resp2, err := insecureClient.Do(req2)
		if err != nil {
			t.Fatalf("Second delete request failed: %v", err)
		}
		defer resp2.Body.Close()

		if resp2.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp2.Body)
			t.Fatalf("Expected 200 OK for second delete, got %d: %s", resp2.StatusCode, body)
		}

		// Object should still exist with deletionTimestamp
		getResp, err := insecureClient.Get(baseURL + "/apis/test.orlop.gcp.managed.openshift.io/v1/namespaces/" + namespace + "/objects/" + name)
		if err != nil {
			t.Fatalf("Get request failed: %v", err)
		}
		defer getResp.Body.Close()

		if getResp.StatusCode != http.StatusOK {
			t.Errorf("Expected object to still exist after second delete, got %d", getResp.StatusCode)
		}
	})
}
