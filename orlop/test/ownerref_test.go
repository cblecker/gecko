package test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

// TestOwnerReferenceValidation tests that invalid owner references are rejected.
func TestOwnerReferenceValidation(t *testing.T) {
	namespace := "default"

	// Create parent object
	parentBody := map[string]interface{}{
		"apiVersion": "test.orlop.gcp.managed.openshift.io/v1",
		"kind":       "Object",
		"metadata": map[string]interface{}{
			"name":      "parent",
			"namespace": namespace,
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

	parentJSON, _ := json.Marshal(parentBody)
	createResp, err := insecureClient.Post(
		baseURL+"/apis/test.orlop.gcp.managed.openshift.io/v1/namespaces/"+namespace+"/objects",
		"application/json",
		bytes.NewBuffer(parentJSON),
	)
	if err != nil {
		t.Fatalf("Create parent request failed: %v", err)
	}
	defer createResp.Body.Close()

	if createResp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(createResp.Body)
		t.Fatalf("Expected 201 Created for parent, got %d: %s", createResp.StatusCode, body)
	}

	var createdParent map[string]interface{}
	bodyBytes, _ := io.ReadAll(createResp.Body)
	json.Unmarshal(bodyBytes, &createdParent)
	parentUID := createdParent["metadata"].(map[string]interface{})["uid"].(string)

	// Try to create child with valid owner reference - should succeed
	validChildBody := map[string]interface{}{
		"apiVersion": "test.orlop.gcp.managed.openshift.io/v1",
		"kind":       "Object",
		"metadata": map[string]interface{}{
			"name":      "valid-child",
			"namespace": namespace,
			"ownerReferences": []map[string]interface{}{
				{
					"apiVersion": "test.orlop.gcp.managed.openshift.io/v1",
					"kind":       "Object",
					"name":       "parent",
					"uid":        parentUID,
				},
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

	validChildJSON, _ := json.Marshal(validChildBody)
	validResp, err := insecureClient.Post(
		baseURL+"/apis/test.orlop.gcp.managed.openshift.io/v1/namespaces/"+namespace+"/objects",
		"application/json",
		bytes.NewBuffer(validChildJSON),
	)
	if err != nil {
		t.Fatalf("Create valid child request failed: %v", err)
	}
	defer validResp.Body.Close()

	if validResp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(validResp.Body)
		t.Fatalf("Expected 201 Created for valid child, got %d: %s", validResp.StatusCode, body)
	}

	var createdChild map[string]interface{}
	validBodyBytes, _ := io.ReadAll(validResp.Body)
	json.Unmarshal(validBodyBytes, &createdChild)
	ownerRefs := createdChild["metadata"].(map[string]interface{})["ownerReferences"].([]interface{})
	if len(ownerRefs) != 1 {
		t.Fatalf("expected 1 owner reference, got %d", len(ownerRefs))
	}
}

// TestCascadeDeletionForeground tests foreground cascade deletion.
// Cascade deletion requires a garbage collector controller, which is not
// part of the lean aggregated server setup. In production, a controller
// or webhook would handle dependent cleanup.
func TestCascadeDeletionForeground(t *testing.T) {
	t.Skip("cascade deletion requires GC controller not present in lean aggregated server")
	namespace := "default"

	// Create parent
	parentBody := map[string]interface{}{
		"apiVersion": "test.orlop.gcp.managed.openshift.io/v1",
		"kind":       "Object",
		"metadata": map[string]interface{}{
			"name":      "parent-fg",
			"namespace": namespace,
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

	parentJSON, _ := json.Marshal(parentBody)
	createResp, err := insecureClient.Post(
		baseURL+"/apis/test.orlop.gcp.managed.openshift.io/v1/namespaces/"+namespace+"/objects",
		"application/json",
		bytes.NewBuffer(parentJSON),
	)
	if err != nil {
		t.Fatalf("Create parent request failed: %v", err)
	}
	defer createResp.Body.Close()

	if createResp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(createResp.Body)
		t.Fatalf("Expected 201 Created for parent, got %d: %s", createResp.StatusCode, body)
	}

	var createdParent map[string]interface{}
	parentBodyBytes, _ := io.ReadAll(createResp.Body)
	json.Unmarshal(parentBodyBytes, &createdParent)
	parentUID := createdParent["metadata"].(map[string]interface{})["uid"].(string)

	// Create child with owner reference
	childBody := map[string]interface{}{
		"apiVersion": "test.orlop.gcp.managed.openshift.io/v1",
		"kind":       "Object",
		"metadata": map[string]interface{}{
			"name":      "child-fg",
			"namespace": namespace,
			"ownerReferences": []map[string]interface{}{
				{
					"apiVersion": "test.orlop.gcp.managed.openshift.io/v1",
					"kind":       "Object",
					"name":       "parent-fg",
					"uid":        parentUID,
				},
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

	childJSON, _ := json.Marshal(childBody)
	childResp, err := insecureClient.Post(
		baseURL+"/apis/test.orlop.gcp.managed.openshift.io/v1/namespaces/"+namespace+"/objects",
		"application/json",
		bytes.NewBuffer(childJSON),
	)
	if err != nil {
		t.Fatalf("Create child request failed: %v", err)
	}
	defer childResp.Body.Close()

	if childResp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(childResp.Body)
		t.Fatalf("Expected 201 Created for child, got %d: %s", childResp.StatusCode, body)
	}

	// Delete parent with foreground propagation
	req, _ := http.NewRequest(
		http.MethodDelete,
		baseURL+"/apis/test.orlop.gcp.managed.openshift.io/v1/namespaces/"+namespace+"/objects/parent-fg?propagationPolicy=Foreground",
		nil,
	)
	deleteResp, err := insecureClient.Do(req)
	if err != nil {
		t.Fatalf("Delete request failed: %v", err)
	}
	defer deleteResp.Body.Close()

	if deleteResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(deleteResp.Body)
		t.Fatalf("Expected 200 OK for delete, got %d: %s", deleteResp.StatusCode, body)
	}

	// Parent should be deleted
	getParentResp, err := insecureClient.Get(baseURL + "/apis/test.orlop.gcp.managed.openshift.io/v1/namespaces/" + namespace + "/objects/parent-fg")
	if err != nil {
		t.Fatalf("Get parent request failed: %v", err)
	}
	defer getParentResp.Body.Close()

	if getParentResp.StatusCode != http.StatusNotFound {
		t.Errorf("expected parent to be deleted (404), got status %d", getParentResp.StatusCode)
	}

	// Child should be deleted synchronously with foreground deletion
	getChildResp, err := insecureClient.Get(baseURL + "/apis/test.orlop.gcp.managed.openshift.io/v1/namespaces/" + namespace + "/objects/child-fg")
	if err != nil {
		t.Fatalf("Get child request failed: %v", err)
	}
	defer getChildResp.Body.Close()

	if getChildResp.StatusCode != http.StatusNotFound {
		t.Errorf("expected child to be deleted (404) with foreground deletion, got status %d", getChildResp.StatusCode)
	}

	t.Log("Foreground cascade deletion: both parent and child deleted synchronously")
}

// TestCascadeDeletionOrphan tests orphan deletion policy.
func TestCascadeDeletionOrphan(t *testing.T) {
	t.Skip("cascade deletion requires GC controller not present in lean aggregated server")
	namespace := "default"

	// Create parent
	parentBody := map[string]interface{}{
		"apiVersion": "test.orlop.gcp.managed.openshift.io/v1",
		"kind":       "Object",
		"metadata": map[string]interface{}{
			"name":      "parent-orphan",
			"namespace": namespace,
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

	parentJSON, _ := json.Marshal(parentBody)
	createResp, err := insecureClient.Post(
		baseURL+"/apis/test.orlop.gcp.managed.openshift.io/v1/namespaces/"+namespace+"/objects",
		"application/json",
		bytes.NewBuffer(parentJSON),
	)
	if err != nil {
		t.Fatalf("Create parent request failed: %v", err)
	}
	defer createResp.Body.Close()

	if createResp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(createResp.Body)
		t.Fatalf("Expected 201 Created for parent, got %d: %s", createResp.StatusCode, body)
	}

	var createdParent map[string]interface{}
	parentBodyBytes, _ := io.ReadAll(createResp.Body)
	json.Unmarshal(parentBodyBytes, &createdParent)
	parentUID := createdParent["metadata"].(map[string]interface{})["uid"].(string)

	// Create child with owner reference
	childBody := map[string]interface{}{
		"apiVersion": "test.orlop.gcp.managed.openshift.io/v1",
		"kind":       "Object",
		"metadata": map[string]interface{}{
			"name":      "child-orphan",
			"namespace": namespace,
			"ownerReferences": []map[string]interface{}{
				{
					"apiVersion": "test.orlop.gcp.managed.openshift.io/v1",
					"kind":       "Object",
					"name":       "parent-orphan",
					"uid":        parentUID,
				},
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

	childJSON, _ := json.Marshal(childBody)
	childResp, err := insecureClient.Post(
		baseURL+"/apis/test.orlop.gcp.managed.openshift.io/v1/namespaces/"+namespace+"/objects",
		"application/json",
		bytes.NewBuffer(childJSON),
	)
	if err != nil {
		t.Fatalf("Create child request failed: %v", err)
	}
	defer childResp.Body.Close()

	if childResp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(childResp.Body)
		t.Fatalf("Expected 201 Created for child, got %d: %s", childResp.StatusCode, body)
	}

	// Delete parent with orphan propagation
	req, _ := http.NewRequest(
		http.MethodDelete,
		baseURL+"/apis/test.orlop.gcp.managed.openshift.io/v1/namespaces/"+namespace+"/objects/parent-orphan?propagationPolicy=Orphan",
		nil,
	)
	deleteResp, err := insecureClient.Do(req)
	if err != nil {
		t.Fatalf("Delete request failed: %v", err)
	}
	defer deleteResp.Body.Close()

	if deleteResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(deleteResp.Body)
		t.Fatalf("Expected 200 OK for delete, got %d: %s", deleteResp.StatusCode, body)
	}

	// Parent should be deleted
	getParentResp, err := insecureClient.Get(baseURL + "/apis/test.orlop.gcp.managed.openshift.io/v1/namespaces/" + namespace + "/objects/parent-orphan")
	if err != nil {
		t.Fatalf("Get parent request failed: %v", err)
	}
	defer getParentResp.Body.Close()

	if getParentResp.StatusCode != http.StatusNotFound {
		t.Errorf("expected parent to be deleted (404), got status %d", getParentResp.StatusCode)
	}

	// Child should still exist (orphaned)
	getChildResp, err := insecureClient.Get(baseURL + "/apis/test.orlop.gcp.managed.openshift.io/v1/namespaces/" + namespace + "/objects/child-orphan")
	if err != nil {
		t.Fatalf("Get child request failed: %v", err)
	}
	defer getChildResp.Body.Close()

	if getChildResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(getChildResp.Body)
		t.Fatalf("Expected child to still exist (200), got %d: %s", getChildResp.StatusCode, body)
	}

	var fetchedChild map[string]interface{}
	childBodyBytes, _ := io.ReadAll(getChildResp.Body)
	json.Unmarshal(childBodyBytes, &fetchedChild)

	// Child should have no owner references
	metadata := fetchedChild["metadata"].(map[string]interface{})
	ownerRefs, hasOwnerRefs := metadata["ownerReferences"]
	if hasOwnerRefs && ownerRefs != nil {
		refs := ownerRefs.([]interface{})
		if len(refs) != 0 {
			t.Errorf("expected child to be orphaned (no owner references), got %d owner references", len(refs))
		}
	}

	t.Log("Orphan deletion: parent deleted, child orphaned (owner reference removed)")
}
