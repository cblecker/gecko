package test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"
)

const (
	v1APIPath = "/apis/test.orlop.gcp.managed.openshift.io/v1"
	v2APIPath = "/apis/test.orlop.gcp.managed.openshift.io/v2"
)

func TestVersionConversion_CreateV1ReadV2(t *testing.T) {
	namespace := "default"
	name := "conv-v1-to-v2"

	createPayload := map[string]interface{}{
		"apiVersion": "test.orlop.gcp.managed.openshift.io/v1",
		"kind":       "Object",
		"metadata": map[string]interface{}{
			"name":      name,
			"namespace": namespace,
		},
		"spec": map[string]interface{}{
			"publicField":   "from-v1",
			"internalField": "internal-v1",
			"nested": map[string]interface{}{
				"publicField":   "nested-v1",
				"internalField": "nested-internal-v1",
			},
		},
	}

	resp, body := doRequest(t, http.MethodPost, fmt.Sprintf("%s/namespaces/%s/objects", v1APIPath, namespace), createPayload)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("Expected 201, got %d: %s", resp.StatusCode, body)
	}

	var created map[string]interface{}
	json.Unmarshal([]byte(body), &created)
	if created["apiVersion"] != "test.orlop.gcp.managed.openshift.io/v1" {
		t.Errorf("Created apiVersion = %v, want v1", created["apiVersion"])
	}

	// Read the same object through the v2 endpoint
	resp, body = doRequest(t, http.MethodGet, fmt.Sprintf("%s/namespaces/%s/objects/%s", v2APIPath, namespace, name), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Expected 200, got %d: %s", resp.StatusCode, body)
	}

	var got map[string]interface{}
	json.Unmarshal([]byte(body), &got)

	if got["apiVersion"] != "test.orlop.gcp.managed.openshift.io/v2" {
		t.Errorf("apiVersion = %v, want test.orlop.gcp.managed.openshift.io/v2", got["apiVersion"])
	}
	if got["kind"] != "Object" {
		t.Errorf("kind = %v, want Object", got["kind"])
	}

	spec := got["spec"].(map[string]interface{})
	if spec["publicField"] != "from-v1" {
		t.Errorf("publicField = %v, want from-v1", spec["publicField"])
	}
	if spec["internalField"] != "internal-v1" {
		t.Errorf("internalField = %v, want internal-v1", spec["internalField"])
	}

	nested := spec["nested"].(map[string]interface{})
	if nested["publicField"] != "nested-v1" {
		t.Errorf("nested.publicField = %v, want nested-v1", nested["publicField"])
	}

	// Cleanup
	doRequest(t, http.MethodDelete, fmt.Sprintf("%s/namespaces/%s/objects/%s", v1APIPath, namespace, name), nil)
}

func TestVersionConversion_CreateV2ReadV1(t *testing.T) {
	namespace := "default"
	name := "conv-v2-to-v1"

	createPayload := map[string]interface{}{
		"apiVersion": "test.orlop.gcp.managed.openshift.io/v2",
		"kind":       "Object",
		"metadata": map[string]interface{}{
			"name":      name,
			"namespace": namespace,
		},
		"spec": map[string]interface{}{
			"publicField":   "from-v2",
			"internalField": "internal-v2",
			"nested": map[string]interface{}{
				"publicField":   "nested-v2",
				"internalField": "nested-internal-v2",
			},
		},
	}

	resp, body := doRequest(t, http.MethodPost, fmt.Sprintf("%s/namespaces/%s/objects", v2APIPath, namespace), createPayload)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("Expected 201, got %d: %s", resp.StatusCode, body)
	}

	var created map[string]interface{}
	json.Unmarshal([]byte(body), &created)
	if created["apiVersion"] != "test.orlop.gcp.managed.openshift.io/v2" {
		t.Errorf("Created apiVersion = %v, want v2", created["apiVersion"])
	}

	// Read through v1 endpoint
	resp, body = doRequest(t, http.MethodGet, fmt.Sprintf("%s/namespaces/%s/objects/%s", v1APIPath, namespace, name), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Expected 200, got %d: %s", resp.StatusCode, body)
	}

	var got map[string]interface{}
	json.Unmarshal([]byte(body), &got)

	if got["apiVersion"] != "test.orlop.gcp.managed.openshift.io/v1" {
		t.Errorf("apiVersion = %v, want test.orlop.gcp.managed.openshift.io/v1", got["apiVersion"])
	}

	spec := got["spec"].(map[string]interface{})
	if spec["publicField"] != "from-v2" {
		t.Errorf("publicField = %v, want from-v2", spec["publicField"])
	}

	// Cleanup
	doRequest(t, http.MethodDelete, fmt.Sprintf("%s/namespaces/%s/objects/%s", v1APIPath, namespace, name), nil)
}

func TestVersionConversion_ListV2(t *testing.T) {
	// TODO: aggregated GenericAPIServer doesn't override per-item GVK in list responses yet.
	t.Skip("aggregated mode: list items retain storage version GVK")
	namespace := "conv-list-ns"

	// Create two objects via v1
	for _, name := range []string{"list-a", "list-b"} {
		payload := map[string]interface{}{
			"apiVersion": "test.orlop.gcp.managed.openshift.io/v1",
			"kind":       "Object",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": namespace,
			},
			"spec": map[string]interface{}{
				"publicField":   name + "-pub",
				"internalField": name + "-int",
				"nested": map[string]interface{}{
					"publicField":   "n",
					"internalField": "n",
				},
			},
		}
		resp, body := doRequest(t, http.MethodPost, fmt.Sprintf("%s/namespaces/%s/objects", v1APIPath, namespace), payload)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("Create %s: expected 201, got %d: %s", name, resp.StatusCode, body)
		}
	}

	// List through v2
	resp, body := doRequest(t, http.MethodGet, fmt.Sprintf("%s/namespaces/%s/objects", v2APIPath, namespace), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Expected 200, got %d: %s", resp.StatusCode, body)
	}

	var list map[string]interface{}
	json.Unmarshal([]byte(body), &list)

	if list["apiVersion"] != "test.orlop.gcp.managed.openshift.io/v2" {
		t.Errorf("list apiVersion = %v, want v2", list["apiVersion"])
	}

	items := list["items"].([]interface{})
	if len(items) < 2 {
		t.Fatalf("Expected at least 2 items, got %d", len(items))
	}

	for _, item := range items {
		obj := item.(map[string]interface{})
		if obj["apiVersion"] != "test.orlop.gcp.managed.openshift.io/v2" {
			t.Errorf("item apiVersion = %v, want v2", obj["apiVersion"])
		}
	}

	// Cleanup
	for _, name := range []string{"list-a", "list-b"} {
		doRequest(t, http.MethodDelete, fmt.Sprintf("%s/namespaces/%s/objects/%s", v1APIPath, namespace, name), nil)
	}
}

func TestVersionConversion_UpdateViaV2ReadV1(t *testing.T) {
	namespace := "default"
	name := "conv-update"

	// Create via v1
	createPayload := map[string]interface{}{
		"apiVersion": "test.orlop.gcp.managed.openshift.io/v1",
		"kind":       "Object",
		"metadata": map[string]interface{}{
			"name":      name,
			"namespace": namespace,
		},
		"spec": map[string]interface{}{
			"publicField":   "original",
			"internalField": "original-int",
			"nested": map[string]interface{}{
				"publicField":   "n",
				"internalField": "n",
			},
		},
	}

	resp, body := doRequest(t, http.MethodPost, fmt.Sprintf("%s/namespaces/%s/objects", v1APIPath, namespace), createPayload)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("Expected 201, got %d: %s", resp.StatusCode, body)
	}

	// Read via v2 to get resourceVersion
	resp, body = doRequest(t, http.MethodGet, fmt.Sprintf("%s/namespaces/%s/objects/%s", v2APIPath, namespace, name), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Expected 200, got %d: %s", resp.StatusCode, body)
	}

	var v2Get map[string]interface{}
	json.Unmarshal([]byte(body), &v2Get)
	rv := v2Get["metadata"].(map[string]interface{})["resourceVersion"].(string)

	// Update via v2
	updatePayload := map[string]interface{}{
		"apiVersion": "test.orlop.gcp.managed.openshift.io/v2",
		"kind":       "Object",
		"metadata": map[string]interface{}{
			"name":            name,
			"namespace":       namespace,
			"resourceVersion": rv,
		},
		"spec": map[string]interface{}{
			"publicField":   "updated-via-v2",
			"internalField": "updated-int-v2",
			"nested": map[string]interface{}{
				"publicField":   "n-updated",
				"internalField": "n-updated",
			},
		},
	}

	resp, body = doRequest(t, http.MethodPut, fmt.Sprintf("%s/namespaces/%s/objects/%s", v2APIPath, namespace, name), updatePayload)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Expected 200, got %d: %s", resp.StatusCode, body)
	}

	var updateResp map[string]interface{}
	json.Unmarshal([]byte(body), &updateResp)
	if updateResp["apiVersion"] != "test.orlop.gcp.managed.openshift.io/v2" {
		t.Errorf("update response apiVersion = %v, want v2", updateResp["apiVersion"])
	}

	// Read via v1 — should see the update
	resp, body = doRequest(t, http.MethodGet, fmt.Sprintf("%s/namespaces/%s/objects/%s", v1APIPath, namespace, name), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Expected 200, got %d: %s", resp.StatusCode, body)
	}

	var v1Get map[string]interface{}
	json.Unmarshal([]byte(body), &v1Get)

	if v1Get["apiVersion"] != "test.orlop.gcp.managed.openshift.io/v1" {
		t.Errorf("v1 get apiVersion = %v, want v1", v1Get["apiVersion"])
	}
	spec := v1Get["spec"].(map[string]interface{})
	if spec["publicField"] != "updated-via-v2" {
		t.Errorf("publicField = %v, want updated-via-v2", spec["publicField"])
	}

	// Cleanup
	doRequest(t, http.MethodDelete, fmt.Sprintf("%s/namespaces/%s/objects/%s", v1APIPath, namespace, name), nil)
}

func TestVersionConversion_PatchViaV2(t *testing.T) {
	namespace := "default"
	name := "conv-patch"

	// Create via v1
	createPayload := map[string]interface{}{
		"apiVersion": "test.orlop.gcp.managed.openshift.io/v1",
		"kind":       "Object",
		"metadata": map[string]interface{}{
			"name":      name,
			"namespace": namespace,
		},
		"spec": map[string]interface{}{
			"publicField":   "before-patch",
			"internalField": "int",
			"nested": map[string]interface{}{
				"publicField":   "n",
				"internalField": "n",
			},
		},
	}

	resp, body := doRequest(t, http.MethodPost, fmt.Sprintf("%s/namespaces/%s/objects", v1APIPath, namespace), createPayload)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("Expected 201, got %d: %s", resp.StatusCode, body)
	}

	// Patch via v2 endpoint using merge patch
	patchPayload := map[string]interface{}{
		"spec": map[string]interface{}{
			"publicField": "patched-via-v2",
		},
	}

	resp, body = doMergePatchRequest(t, fmt.Sprintf("%s/namespaces/%s/objects/%s", v2APIPath, namespace, name), patchPayload)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Expected 200, got %d: %s", resp.StatusCode, body)
	}

	var patchResp map[string]interface{}
	json.Unmarshal([]byte(body), &patchResp)
	if patchResp["apiVersion"] != "test.orlop.gcp.managed.openshift.io/v2" {
		t.Errorf("patch response apiVersion = %v, want v2", patchResp["apiVersion"])
	}

	// Read via v1 — should see the patch
	resp, body = doRequest(t, http.MethodGet, fmt.Sprintf("%s/namespaces/%s/objects/%s", v1APIPath, namespace, name), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Expected 200, got %d: %s", resp.StatusCode, body)
	}

	var v1Get map[string]interface{}
	json.Unmarshal([]byte(body), &v1Get)
	spec := v1Get["spec"].(map[string]interface{})
	if spec["publicField"] != "patched-via-v2" {
		t.Errorf("publicField = %v, want patched-via-v2", spec["publicField"])
	}

	// Cleanup
	doRequest(t, http.MethodDelete, fmt.Sprintf("%s/namespaces/%s/objects/%s", v1APIPath, namespace, name), nil)
}

func TestVersionConversion_StatusViaV2(t *testing.T) {
	// TODO: aggregated mode status subresource requires name in request body.
	t.Skip("aggregated mode: status subresource name extraction differs")
	namespace := "default"
	name := "conv-status"

	// Create via v1
	createPayload := map[string]interface{}{
		"apiVersion": "test.orlop.gcp.managed.openshift.io/v1",
		"kind":       "Object",
		"metadata": map[string]interface{}{
			"name":      name,
			"namespace": namespace,
		},
		"spec": map[string]interface{}{
			"publicField":   "val",
			"internalField": "int",
			"nested": map[string]interface{}{
				"publicField":   "n",
				"internalField": "n",
			},
		},
	}

	resp, body := doRequest(t, http.MethodPost, fmt.Sprintf("%s/namespaces/%s/objects", v1APIPath, namespace), createPayload)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("Expected 201, got %d: %s", resp.StatusCode, body)
	}

	var created map[string]interface{}
	json.Unmarshal([]byte(body), &created)
	rv := created["metadata"].(map[string]interface{})["resourceVersion"].(string)

	// Update status via v2
	statusPayload := map[string]interface{}{
		"metadata": map[string]interface{}{
			"resourceVersion": rv,
		},
		"status": map[string]interface{}{
			"conditions": []string{"Ready"},
		},
	}

	resp, body = doRequest(t, http.MethodPut, fmt.Sprintf("%s/namespaces/%s/objects/%s/status", v2APIPath, namespace, name), statusPayload)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Expected 200, got %d: %s", resp.StatusCode, body)
	}

	var statusResp map[string]interface{}
	json.Unmarshal([]byte(body), &statusResp)
	if statusResp["apiVersion"] != "test.orlop.gcp.managed.openshift.io/v2" {
		t.Errorf("status response apiVersion = %v, want v2", statusResp["apiVersion"])
	}

	// Read status via v1
	resp, body = doRequest(t, http.MethodGet, fmt.Sprintf("%s/namespaces/%s/objects/%s", v1APIPath, namespace, name), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Expected 200, got %d: %s", resp.StatusCode, body)
	}

	var v1Get map[string]interface{}
	json.Unmarshal([]byte(body), &v1Get)
	status := v1Get["status"].(map[string]interface{})
	conditions := status["conditions"].([]interface{})
	if len(conditions) != 1 || conditions[0] != "Ready" {
		t.Errorf("conditions = %v, want [Ready]", conditions)
	}

	// Cleanup
	doRequest(t, http.MethodDelete, fmt.Sprintf("%s/namespaces/%s/objects/%s", v1APIPath, namespace, name), nil)
}

func TestVersionConversion_WatchV2(t *testing.T) {
	namespace := "default"

	// Start a v2 watch
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	watchURL := fmt.Sprintf("%s%s/namespaces/%s/objects?watch=true", baseURL, v2APIPath, namespace)
	watchReq, err := http.NewRequestWithContext(ctx, http.MethodGet, watchURL, nil)
	if err != nil {
		t.Fatalf("Failed to create watch request: %v", err)
	}

	type respResult struct {
		resp *http.Response
		err  error
	}
	respCh := make(chan respResult, 1)

	go func() {
		resp, err := insecureClient.Do(watchReq)
		respCh <- respResult{resp, err}
	}()

	var watchResp *http.Response
	select {
	case result := <-respCh:
		if result.err != nil {
			t.Fatalf("Failed to start watch: %v", result.err)
		}
		watchResp = result.resp
	case <-time.After(2 * time.Second):
		t.Fatal("Timeout waiting for watch connection")
	}
	defer watchResp.Body.Close()

	events := make(chan map[string]interface{}, 10)
	go func() {
		decoder := json.NewDecoder(watchResp.Body)
		for {
			var event map[string]interface{}
			if err := decoder.Decode(&event); err != nil {
				return
			}
			events <- event
		}
	}()

	time.Sleep(100 * time.Millisecond)

	// Create an object via v1
	createPayload := map[string]interface{}{
		"apiVersion": "test.orlop.gcp.managed.openshift.io/v1",
		"kind":       "Object",
		"metadata": map[string]interface{}{
			"name":      "conv-watch",
			"namespace": namespace,
		},
		"spec": map[string]interface{}{
			"publicField":   "watch-test",
			"internalField": "int",
			"nested": map[string]interface{}{
				"publicField":   "n",
				"internalField": "n",
			},
		},
	}

	doRequest(t, http.MethodPost, fmt.Sprintf("%s/namespaces/%s/objects", v1APIPath, namespace), createPayload)

	// v2 watch should receive the event with v2 apiVersion
	select {
	case event := <-events:
		if event["type"] != "ADDED" {
			t.Errorf("Expected ADDED event, got %s", event["type"])
		}
		obj := event["object"].(map[string]interface{})
		if obj["apiVersion"] != "test.orlop.gcp.managed.openshift.io/v2" {
			t.Errorf("watch event apiVersion = %v, want v2", obj["apiVersion"])
		}
		if obj["kind"] != "Object" {
			t.Errorf("watch event kind = %v, want Object", obj["kind"])
		}
		metadata := obj["metadata"].(map[string]interface{})
		if metadata["name"] != "conv-watch" {
			t.Errorf("watch event name = %v, want conv-watch", metadata["name"])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Timeout waiting for ADDED event on v2 watch")
	}

	// Cleanup
	doRequest(t, http.MethodDelete, fmt.Sprintf("%s/namespaces/%s/objects/%s", v1APIPath, namespace, "conv-watch"), nil)
}

func TestVersionConversion_DeleteViaV2(t *testing.T) {
	namespace := "default"
	name := "conv-delete"

	// Create via v1
	createPayload := map[string]interface{}{
		"apiVersion": "test.orlop.gcp.managed.openshift.io/v1",
		"kind":       "Object",
		"metadata": map[string]interface{}{
			"name":      name,
			"namespace": namespace,
		},
		"spec": map[string]interface{}{
			"publicField":   "to-delete",
			"internalField": "int",
			"nested": map[string]interface{}{
				"publicField":   "n",
				"internalField": "n",
			},
		},
	}

	resp, body := doRequest(t, http.MethodPost, fmt.Sprintf("%s/namespaces/%s/objects", v1APIPath, namespace), createPayload)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("Expected 201, got %d: %s", resp.StatusCode, body)
	}

	// Delete via v2
	resp, body = doRequest(t, http.MethodDelete, fmt.Sprintf("%s/namespaces/%s/objects/%s", v2APIPath, namespace, name), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Expected 200, got %d: %s", resp.StatusCode, body)
	}

	// Verify deleted via v1
	resp, _ = doRequest(t, http.MethodGet, fmt.Sprintf("%s/namespaces/%s/objects/%s", v1APIPath, namespace, name), nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("Expected 404 after delete, got %d", resp.StatusCode)
	}
}

func TestVersionConversion_DiscoveryV2(t *testing.T) {
	resp, body := doRequest(t, http.MethodGet, v2APIPath, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Expected 200, got %d: %s", resp.StatusCode, body)
	}

	var resourceList map[string]interface{}
	json.Unmarshal([]byte(body), &resourceList)

	if resourceList["groupVersion"] != "test.orlop.gcp.managed.openshift.io/v2" {
		t.Errorf("groupVersion = %v, want test.orlop.gcp.managed.openshift.io/v2", resourceList["groupVersion"])
	}

	resources := resourceList["resources"].([]interface{})
	foundObjects := false
	for _, r := range resources {
		res := r.(map[string]interface{})
		if res["name"] == "objects" {
			foundObjects = true
			if res["kind"] != "Object" {
				t.Errorf("kind = %v, want Object", res["kind"])
			}
		}
	}
	if !foundObjects {
		t.Error("objects resource not found in v2 discovery")
	}
}

func doMergePatchRequest(t *testing.T, path string, body interface{}) (*http.Response, string) {
	t.Helper()
	jsonData, _ := json.Marshal(body)

	req, err := http.NewRequest(http.MethodPatch, baseURL+path, bytes.NewReader(jsonData))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/merge-patch+json")

	resp, err := insecureClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	return resp, string(respBody)
}
