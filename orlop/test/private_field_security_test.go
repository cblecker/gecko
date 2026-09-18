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
	"strings"
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

// TestPrivateFieldSecurity is a comprehensive test suite verifying that private
// fields cannot be seen, injected, or tampered with through the public API.
//
// Private field types tested:
//   - Private-prefixed labels (prefix: private.orlop.gcp.managed.openshift.io/)
//   - Private-prefixed annotations (same prefix)
//   - Private-prefixed conditions in status (same prefix)
//   - Finalizers (never exposed on public API)
//   - DeletionTimestamp (never settable via public API)
//   - Private spec fields (internalField, nested.internalField — only in private type)
//
// Attack categories tested for each CRUD operation:
//   - INJECTION: client tries to set/add private fields via public API
//   - LEAKAGE:   private fields visible in public API responses
//   - PRESERVATION: existing private fields survive public API operations
//   - TAMPERING: client tries to modify or remove existing private fields
func TestPrivateFieldSecurity(t *testing.T) {
	// Setup dedicated test server
	privateScheme := runtime.NewScheme()
	if err := privatev1.AddToScheme(privateScheme); err != nil {
		t.Fatalf("failed to register private API types: %v", err)
	}

	publicScheme := runtime.NewScheme()
	if err := publicv1.AddToScheme(publicScheme); err != nil {
		t.Fatalf("failed to register public API types: %v", err)
	}

	gvk := runtimeschema.GroupVersionKind{
		Group:   "test.orlop.gcp.managed.openshift.io",
		Version: "v1",
		Kind:    "Object",
	}

	privateResources := []apiserver.ResourceInfo{{
		GVK: gvk, Plural: privatev1.ObjectResourceInfo.Plural,
		Singular: "object", Namespaced: true, SchemaYAML: privatev1.ObjectSchemaYAML,
	}}

	publicResources := []apiserver.ResourceInfo{{
		GVK: gvk, Plural: publicv1.ObjectResourceInfo.Plural,
		Singular: "object", Namespaced: true, SchemaYAML: publicv1.ObjectSchemaYAML,
	}}

	storageFactory := func(resourceType string, s *runtime.Scheme, gvk runtimeschema.GroupVersionKind) (storage.ResourceStore, error) {
		return memory.NewMemoryStore(resourceType, s, gvk), nil
	}

	ports := freePorts(t, 2)

	opts := apiserver.Options{
		Address:     "127.0.0.1",
		CORSOrigins: []string{"*"},
		Private: apiserver.PrivateAPIOptions{
			Port: ports[0], Resources: privateResources,
			Scheme: privateScheme, DisableAuth: true,
		},
		Public: apiserver.PublicAPIOptions{
			Enable: true, Port: ports[1],
			Resources: publicResources, Scheme: publicScheme,
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

	// Wait for server
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

	// Also wait for public API listener
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
		server.Shutdown(ctx)
	})

	// Helper: API base paths
	const ns = "default"
	apiPath := "/apis/test.orlop.gcp.managed.openshift.io/v1/namespaces/" + ns + "/objects"

	// Helper functions
	do := func(t *testing.T, client *http.Client, method, url string, body interface{}) (int, map[string]interface{}) {
		t.Helper()
		var reqBody io.Reader
		if body != nil {
			data, _ := json.Marshal(body)
			reqBody = bytes.NewBuffer(data)
		}
		req, err := http.NewRequest(method, url, reqBody)
		if err != nil {
			t.Fatalf("request error: %v", err)
		}
		if body != nil {
			if method == http.MethodPatch {
				req.Header.Set("Content-Type", "application/merge-patch+json")
			} else {
				req.Header.Set("Content-Type", "application/json")
			}
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("HTTP error: %v", err)
		}
		defer resp.Body.Close()
		respBody, _ := io.ReadAll(resp.Body)
		var result map[string]interface{}
		if err := json.Unmarshal(respBody, &result); err != nil {
			t.Fatalf("failed to unmarshal response (status %d): %v\nbody: %s", resp.StatusCode, err, respBody)
		}
		return resp.StatusCode, result
	}

	publicCreate := func(t *testing.T, name string, obj map[string]interface{}) map[string]interface{} {
		t.Helper()
		code, result := do(t, pubClient, http.MethodPost, publicURL+apiPath, obj)
		if code != http.StatusCreated {
			t.Fatalf("public create %s: expected 201, got %d: %v", name, code, result)
		}
		return result
	}

	privateCreate := func(t *testing.T, name string, obj map[string]interface{}) map[string]interface{} {
		t.Helper()
		code, result := do(t, privClient, http.MethodPost, privateURL+apiPath, obj)
		if code != http.StatusCreated {
			t.Fatalf("private create %s: expected 201, got %d: %v", name, code, result)
		}
		return result
	}

	publicGet := func(t *testing.T, name string) map[string]interface{} {
		t.Helper()
		code, result := do(t, pubClient, http.MethodGet, publicURL+apiPath+"/"+name, nil)
		if code != http.StatusOK {
			t.Fatalf("public get %s: expected 200, got %d", name, code)
		}
		return result
	}

	privateGet := func(t *testing.T, name string) map[string]interface{} {
		t.Helper()
		code, result := do(t, privClient, http.MethodGet, privateURL+apiPath+"/"+name, nil)
		if code != http.StatusOK {
			t.Fatalf("private get %s: expected 200, got %d", name, code)
		}
		return result
	}

	publicUpdate := func(t *testing.T, name string, obj map[string]interface{}) map[string]interface{} {
		t.Helper()
		code, result := do(t, pubClient, http.MethodPut, publicURL+apiPath+"/"+name, obj)
		if code != http.StatusOK {
			t.Fatalf("public update %s: expected 200, got %d: %v", name, code, result)
		}
		return result
	}

	publicPatch := func(t *testing.T, name string, patch map[string]interface{}) map[string]interface{} {
		t.Helper()
		code, result := do(t, pubClient, http.MethodPatch, publicURL+apiPath+"/"+name, patch)
		if code != http.StatusOK {
			t.Fatalf("public patch %s: expected 200, got %d: %v", name, code, result)
		}
		return result
	}

	privateUpdate := func(t *testing.T, name string, obj map[string]interface{}) map[string]interface{} {
		t.Helper()
		code, result := do(t, privClient, http.MethodPut, privateURL+apiPath+"/"+name, obj)
		if code != http.StatusOK {
			t.Fatalf("private update %s: expected 200, got %d: %v", name, code, result)
		}
		return result
	}

	cleanup := func(t *testing.T, name string) {
		t.Helper()
		do(t, privClient, http.MethodDelete, privateURL+apiPath+"/"+name, nil)
	}

	// Base object for most tests
	baseObj := func(name string) map[string]interface{} {
		return map[string]interface{}{
			"apiVersion": "test.orlop.gcp.managed.openshift.io/v1",
			"kind":       "Object",
			"metadata":   map[string]interface{}{"name": name, "namespace": ns},
			"spec": map[string]interface{}{
				"publicField": "value",
				"nested":      map[string]interface{}{"publicField": "nested-value"},
			},
		}
	}

	rv := func(obj map[string]interface{}) string {
		return obj["metadata"].(map[string]interface{})["resourceVersion"].(string)
	}

	meta := func(obj map[string]interface{}) map[string]interface{} {
		return obj["metadata"].(map[string]interface{})
	}

	spec := func(obj map[string]interface{}) map[string]interface{} {
		return obj["spec"].(map[string]interface{})
	}

	conditions := func(obj map[string]interface{}) []interface{} {
		status, ok := obj["status"].(map[string]interface{})
		if !ok {
			return nil
		}
		conds, ok := status["conditions"].([]interface{})
		if !ok {
			return nil
		}
		return conds
	}

	hasPrivatePrefix := func(s string) bool {
		return strings.HasPrefix(s, "private.orlop.gcp.managed.openshift.io/")
	}

	// =========================================================================
	// CREATE — INJECTION: Client tries to inject private fields via public Create
	// =========================================================================

	t.Run("Create/Injection/private labels stripped", func(t *testing.T) {
		name := "create-inject-labels"
		obj := baseObj(name)
		meta(obj)["labels"] = map[string]interface{}{
			"app": "myapp",
			"private.orlop.gcp.managed.openshift.io/secret": "should-be-stripped",
		}

		resp := publicCreate(t, name, obj)
		defer cleanup(t, name)

		// Response should not contain private label
		respLabels := meta(resp)["labels"].(map[string]interface{})
		if _, exists := respLabels["private.orlop.gcp.managed.openshift.io/secret"]; exists {
			t.Error("Private label visible in create response")
		}

		// Storage should not contain private label
		priv := privateGet(t, name)
		privLabels, _ := meta(priv)["labels"].(map[string]interface{})
		if _, exists := privLabels["private.orlop.gcp.managed.openshift.io/secret"]; exists {
			t.Error("Private label persisted in storage via public create")
		}
		if privLabels["app"] != "myapp" {
			t.Error("Public label not preserved")
		}
	})

	t.Run("Create/Injection/private annotations stripped", func(t *testing.T) {
		name := "create-inject-annot"
		obj := baseObj(name)
		meta(obj)["annotations"] = map[string]interface{}{
			"note": "public",
			"private.orlop.gcp.managed.openshift.io/sync": "should-be-stripped",
		}

		resp := publicCreate(t, name, obj)
		defer cleanup(t, name)

		// Response
		respAnnotations := meta(resp)["annotations"].(map[string]interface{})
		if _, exists := respAnnotations["private.orlop.gcp.managed.openshift.io/sync"]; exists {
			t.Error("Private annotation visible in create response")
		}

		// Storage
		priv := privateGet(t, name)
		privAnnotations, _ := meta(priv)["annotations"].(map[string]interface{})
		if _, exists := privAnnotations["private.orlop.gcp.managed.openshift.io/sync"]; exists {
			t.Error("Private annotation persisted in storage via public create")
		}
	})

	t.Run("Create/Injection/private conditions stripped", func(t *testing.T) {
		name := "create-inject-cond"
		obj := baseObj(name)
		obj["status"] = map[string]interface{}{
			"conditions": []string{
				"Ready",
				"private.orlop.gcp.managed.openshift.io/InternalSync",
			},
		}

		publicCreate(t, name, obj)
		defer cleanup(t, name)

		// Check storage via private API
		priv := privateGet(t, name)
		for _, c := range conditions(priv) {
			if condStr, ok := c.(string); ok && hasPrivatePrefix(condStr) {
				t.Errorf("Private condition %q persisted via public create", condStr)
			}
		}
	})

	t.Run("Create/Injection/finalizers stripped", func(t *testing.T) {
		// Existing test covers this; included for completeness in matrix
		name := "create-inject-fin"
		obj := baseObj(name)
		meta(obj)["finalizers"] = []string{"test.io/my-finalizer"}

		resp := publicCreate(t, name, obj)
		defer cleanup(t, name)

		if _, exists := meta(resp)["finalizers"]; exists {
			t.Error("Finalizers visible in create response")
		}

		priv := privateGet(t, name)
		fins, _ := meta(priv)["finalizers"].([]interface{})
		if len(fins) > 0 {
			t.Errorf("Finalizers persisted via public create: %v", fins)
		}
	})

	t.Run("Create/Injection/internal spec fields ignored", func(t *testing.T) {
		name := "create-inject-spec"
		obj := baseObj(name)
		spec(obj)["internalField"] = "should-be-pruned"
		spec(obj)["nested"].(map[string]interface{})["internalField"] = "should-be-pruned"

		publicCreate(t, name, obj)
		defer cleanup(t, name)

		priv := privateGet(t, name)
		privSpec := spec(priv)
		if privSpec["internalField"] == "should-be-pruned" {
			t.Error("Internal spec field injected via public create")
		}
	})

	// =========================================================================
	// CREATE — LEAKAGE: Private fields not visible in Create response
	// =========================================================================

	t.Run("Create/Leakage/response strips private fields", func(t *testing.T) {
		name := "create-leak-resp"

		// Create via private API with all private fields set
		privObj := baseObj(name)
		spec(privObj)["internalField"] = "secret"
		spec(privObj)["nested"].(map[string]interface{})["internalField"] = "nested-secret"
		meta(privObj)["labels"] = map[string]interface{}{
			"app": "myapp",
			"private.orlop.gcp.managed.openshift.io/owner": "system",
		}
		meta(privObj)["annotations"] = map[string]interface{}{
			"private.orlop.gcp.managed.openshift.io/id": "12345",
		}
		meta(privObj)["finalizers"] = []string{"test.io/fin"}
		privObj["status"] = map[string]interface{}{
			"conditions": []string{"Ready", "private.orlop.gcp.managed.openshift.io/Sync"},
		}

		privateCreate(t, name, privObj)
		defer cleanup(t, name)

		// Read via public GET
		pub := publicGet(t, name)

		// Spec: no internal fields
		if _, exists := spec(pub)["internalField"]; exists {
			t.Error("internalField leaked in public GET")
		}

		// Labels: no private-prefixed
		labels, _ := meta(pub)["labels"].(map[string]interface{})
		for k := range labels {
			if hasPrivatePrefix(k) {
				t.Errorf("Private label %q leaked in public GET", k)
			}
		}

		// Annotations: no private-prefixed
		annotations, _ := meta(pub)["annotations"].(map[string]interface{})
		for k := range annotations {
			if hasPrivatePrefix(k) {
				t.Errorf("Private annotation %q leaked in public GET", k)
			}
		}

		// Finalizers: not present
		if _, exists := meta(pub)["finalizers"]; exists {
			t.Error("Finalizers leaked in public GET")
		}

		// Conditions: no private-prefixed
		for _, c := range conditions(pub) {
			if condStr, ok := c.(string); ok && hasPrivatePrefix(condStr) {
				t.Errorf("Private condition %q leaked in public GET", condStr)
			}
		}
	})

	// =========================================================================
	// UPDATE (PUT) — INJECTION: Client tries to inject private fields
	// =========================================================================

	t.Run("Update/Injection/private labels stripped", func(t *testing.T) {
		name := "update-inject-labels"
		publicCreate(t, name, baseObj(name))
		defer cleanup(t, name)

		pub := publicGet(t, name)
		updateObj := baseObj(name)
		meta(updateObj)["resourceVersion"] = rv(pub)
		meta(updateObj)["labels"] = map[string]interface{}{
			"app": "updated",
			"private.orlop.gcp.managed.openshift.io/injected": "attack",
		}

		resp := publicUpdate(t, name, updateObj)

		// Response check
		respLabels, _ := meta(resp)["labels"].(map[string]interface{})
		if _, exists := respLabels["private.orlop.gcp.managed.openshift.io/injected"]; exists {
			t.Error("Private label visible in update response")
		}

		// Storage check
		priv := privateGet(t, name)
		privLabels, _ := meta(priv)["labels"].(map[string]interface{})
		if _, exists := privLabels["private.orlop.gcp.managed.openshift.io/injected"]; exists {
			t.Error("Private label injected via public update")
		}
	})

	t.Run("Update/Injection/private annotations stripped", func(t *testing.T) {
		name := "update-inject-annot"
		publicCreate(t, name, baseObj(name))
		defer cleanup(t, name)

		pub := publicGet(t, name)
		updateObj := baseObj(name)
		meta(updateObj)["resourceVersion"] = rv(pub)
		meta(updateObj)["annotations"] = map[string]interface{}{
			"private.orlop.gcp.managed.openshift.io/attack": "injected",
		}

		publicUpdate(t, name, updateObj)

		priv := privateGet(t, name)
		privAnnotations, _ := meta(priv)["annotations"].(map[string]interface{})
		if _, exists := privAnnotations["private.orlop.gcp.managed.openshift.io/attack"]; exists {
			t.Error("Private annotation injected via public update")
		}
	})

	t.Run("Update/Injection/private conditions stripped", func(t *testing.T) {
		name := "update-inject-cond"
		publicCreate(t, name, baseObj(name))
		defer cleanup(t, name)

		pub := publicGet(t, name)
		updateObj := baseObj(name)
		meta(updateObj)["resourceVersion"] = rv(pub)
		updateObj["status"] = map[string]interface{}{
			"conditions": []string{
				"Ready",
				"private.orlop.gcp.managed.openshift.io/InjectedViaUpdate",
			},
		}

		publicUpdate(t, name, updateObj)

		priv := privateGet(t, name)
		for _, c := range conditions(priv) {
			if condStr, ok := c.(string); ok && condStr == "private.orlop.gcp.managed.openshift.io/InjectedViaUpdate" {
				t.Error("Private condition injected via public update")
			}
		}
	})

	t.Run("Update/Injection/finalizers stripped", func(t *testing.T) {
		name := "update-inject-fin"
		publicCreate(t, name, baseObj(name))
		defer cleanup(t, name)

		pub := publicGet(t, name)
		updateObj := baseObj(name)
		meta(updateObj)["resourceVersion"] = rv(pub)
		meta(updateObj)["finalizers"] = []string{"attack.io/finalizer"}

		publicUpdate(t, name, updateObj)

		priv := privateGet(t, name)
		fins, _ := meta(priv)["finalizers"].([]interface{})
		if len(fins) > 0 {
			t.Errorf("Finalizers injected via public update: %v", fins)
		}
	})

	// =========================================================================
	// UPDATE (PUT) — PRESERVATION: Existing private fields survive public PUT
	// =========================================================================

	t.Run("Update/Preservation/private labels survive", func(t *testing.T) {
		name := "update-pres-labels"
		publicCreate(t, name, baseObj(name))
		defer cleanup(t, name)

		// Add private label via private API
		priv := privateGet(t, name)
		privMeta := meta(priv)
		privMeta["labels"] = map[string]interface{}{
			"app": "myapp",
			"private.orlop.gcp.managed.openshift.io/owner": "controller",
		}
		privateUpdate(t, name, priv)

		// Public update (only sets public labels)
		pub := publicGet(t, name)
		updateObj := baseObj(name)
		meta(updateObj)["resourceVersion"] = rv(pub)
		meta(updateObj)["labels"] = map[string]interface{}{"app": "updated"}
		publicUpdate(t, name, updateObj)

		// Verify private label survived
		priv2 := privateGet(t, name)
		privLabels := meta(priv2)["labels"].(map[string]interface{})
		if privLabels["private.orlop.gcp.managed.openshift.io/owner"] != "controller" {
			t.Errorf("Private label lost after public update: %v", privLabels)
		}
		if privLabels["app"] != "updated" {
			t.Error("Public label not updated")
		}
	})

	t.Run("Update/Preservation/private annotations survive", func(t *testing.T) {
		name := "update-pres-annot"
		publicCreate(t, name, baseObj(name))
		defer cleanup(t, name)

		priv := privateGet(t, name)
		meta(priv)["annotations"] = map[string]interface{}{
			"note": "public-note",
			"private.orlop.gcp.managed.openshift.io/sync": "done",
		}
		privateUpdate(t, name, priv)

		pub := publicGet(t, name)
		updateObj := baseObj(name)
		meta(updateObj)["resourceVersion"] = rv(pub)
		meta(updateObj)["annotations"] = map[string]interface{}{"note": "updated-note"}
		publicUpdate(t, name, updateObj)

		priv2 := privateGet(t, name)
		privAnnotations := meta(priv2)["annotations"].(map[string]interface{})
		if privAnnotations["private.orlop.gcp.managed.openshift.io/sync"] != "done" {
			t.Errorf("Private annotation lost after public update: %v", privAnnotations)
		}
	})

	t.Run("Update/Preservation/private conditions survive", func(t *testing.T) {
		name := "update-pres-cond"
		// Create with private conditions via private API
		privObj := baseObj(name)
		privObj["status"] = map[string]interface{}{
			"conditions": []string{
				"Ready",
				"private.orlop.gcp.managed.openshift.io/Sync",
			},
		}
		privateCreate(t, name, privObj)
		defer cleanup(t, name)

		// Public update with conditions in body
		pub := publicGet(t, name)
		updateObj := baseObj(name)
		meta(updateObj)["resourceVersion"] = rv(pub)
		updateObj["status"] = map[string]interface{}{
			"conditions": []string{"Ready", "Available"},
		}
		publicUpdate(t, name, updateObj)

		// Private conditions must survive
		priv := privateGet(t, name)
		conds := conditions(priv)
		foundPrivate := false
		for _, c := range conds {
			if condStr, ok := c.(string); ok && condStr == "private.orlop.gcp.managed.openshift.io/Sync" {
				foundPrivate = true
			}
		}
		if !foundPrivate {
			t.Errorf("Private condition destroyed by public update. Conditions: %v", conds)
		}
	})

	t.Run("Update/Preservation/finalizers survive", func(t *testing.T) {
		name := "update-pres-fin"
		publicCreate(t, name, baseObj(name))
		defer cleanup(t, name)

		// Add finalizer via private API
		priv := privateGet(t, name)
		meta(priv)["finalizers"] = []string{"test.io/controller-fin"}
		privateUpdate(t, name, priv)

		// Public update
		pub := publicGet(t, name)
		updateObj := baseObj(name)
		meta(updateObj)["resourceVersion"] = rv(pub)
		spec(updateObj)["publicField"] = "updated"
		publicUpdate(t, name, updateObj)

		// Finalizer must survive
		priv2 := privateGet(t, name)
		fins := meta(priv2)["finalizers"].([]interface{})
		if len(fins) != 1 || fins[0] != "test.io/controller-fin" {
			t.Errorf("Finalizer lost after public update: %v", fins)
		}
	})

	t.Run("Update/Preservation/internal spec fields survive", func(t *testing.T) {
		// Existing test covers this; included for matrix completeness
		name := "update-pres-spec"

		privObj := baseObj(name)
		spec(privObj)["internalField"] = "secret-data"
		spec(privObj)["nested"].(map[string]interface{})["internalField"] = "nested-secret"
		privateCreate(t, name, privObj)
		defer cleanup(t, name)

		pub := publicGet(t, name)
		updateObj := baseObj(name)
		meta(updateObj)["resourceVersion"] = rv(pub)
		spec(updateObj)["publicField"] = "updated-public"
		publicUpdate(t, name, updateObj)

		priv := privateGet(t, name)
		if spec(priv)["internalField"] != "secret-data" {
			t.Errorf("Internal spec field lost after public update: %v", spec(priv)["internalField"])
		}
		nested := spec(priv)["nested"].(map[string]interface{})
		if nested["internalField"] != "nested-secret" {
			t.Errorf("Nested internal field lost after public update: %v", nested["internalField"])
		}
	})

	// =========================================================================
	// PATCH — INJECTION: Client tries to inject private fields via Patch
	// =========================================================================

	t.Run("Patch/Injection/private labels stripped", func(t *testing.T) {
		name := "patch-inject-labels"
		publicCreate(t, name, baseObj(name))
		defer cleanup(t, name)

		publicPatch(t, name, map[string]interface{}{
			"metadata": map[string]interface{}{
				"labels": map[string]interface{}{
					"private.orlop.gcp.managed.openshift.io/attack": "injected",
				},
			},
		})

		priv := privateGet(t, name)
		privLabels, _ := meta(priv)["labels"].(map[string]interface{})
		if _, exists := privLabels["private.orlop.gcp.managed.openshift.io/attack"]; exists {
			t.Error("Private label injected via public patch")
		}
	})

	t.Run("Patch/Injection/private annotations stripped", func(t *testing.T) {
		name := "patch-inject-annot"
		publicCreate(t, name, baseObj(name))
		defer cleanup(t, name)

		publicPatch(t, name, map[string]interface{}{
			"metadata": map[string]interface{}{
				"annotations": map[string]interface{}{
					"private.orlop.gcp.managed.openshift.io/attack": "injected",
				},
			},
		})

		priv := privateGet(t, name)
		privAnnotations, _ := meta(priv)["annotations"].(map[string]interface{})
		if _, exists := privAnnotations["private.orlop.gcp.managed.openshift.io/attack"]; exists {
			t.Error("Private annotation injected via public patch")
		}
	})

	t.Run("Patch/Injection/private conditions stripped", func(t *testing.T) {
		name := "patch-inject-cond"
		publicCreate(t, name, baseObj(name))
		defer cleanup(t, name)

		publicPatch(t, name, map[string]interface{}{
			"status": map[string]interface{}{
				"conditions": []string{
					"Ready",
					"private.orlop.gcp.managed.openshift.io/InjectedViaPatch",
				},
			},
		})

		priv := privateGet(t, name)
		for _, c := range conditions(priv) {
			if condStr, ok := c.(string); ok && condStr == "private.orlop.gcp.managed.openshift.io/InjectedViaPatch" {
				t.Error("Private condition injected via public patch")
			}
		}
	})

	t.Run("Patch/Injection/finalizers stripped", func(t *testing.T) {
		name := "patch-inject-fin"
		publicCreate(t, name, baseObj(name))
		defer cleanup(t, name)

		publicPatch(t, name, map[string]interface{}{
			"metadata": map[string]interface{}{
				"finalizers": []string{"attack.io/fin"},
			},
		})

		priv := privateGet(t, name)
		fins, _ := meta(priv)["finalizers"].([]interface{})
		if len(fins) > 0 {
			t.Errorf("Finalizers injected via public patch: %v", fins)
		}
	})

	// =========================================================================
	// PATCH — PRESERVATION: Existing private fields survive public Patch
	// =========================================================================

	t.Run("Patch/Preservation/private labels survive spec-only patch", func(t *testing.T) {
		name := "patch-pres-labels"
		publicCreate(t, name, baseObj(name))
		defer cleanup(t, name)

		priv := privateGet(t, name)
		meta(priv)["labels"] = map[string]interface{}{
			"private.orlop.gcp.managed.openshift.io/owner": "controller",
			"app": "myapp",
		}
		privateUpdate(t, name, priv)

		// Spec-only patch should not touch labels
		publicPatch(t, name, map[string]interface{}{
			"spec": map[string]interface{}{"publicField": "patched"},
		})

		priv2 := privateGet(t, name)
		privLabels := meta(priv2)["labels"].(map[string]interface{})
		if privLabels["private.orlop.gcp.managed.openshift.io/owner"] != "controller" {
			t.Errorf("Private label lost after spec-only patch: %v", privLabels)
		}
	})

	t.Run("Patch/Preservation/private annotations survive", func(t *testing.T) {
		name := "patch-pres-annot"
		publicCreate(t, name, baseObj(name))
		defer cleanup(t, name)

		priv := privateGet(t, name)
		meta(priv)["annotations"] = map[string]interface{}{
			"private.orlop.gcp.managed.openshift.io/state": "reconciled",
		}
		privateUpdate(t, name, priv)

		publicPatch(t, name, map[string]interface{}{
			"spec": map[string]interface{}{"publicField": "patched"},
		})

		priv2 := privateGet(t, name)
		privAnnotations, _ := meta(priv2)["annotations"].(map[string]interface{})
		if privAnnotations["private.orlop.gcp.managed.openshift.io/state"] != "reconciled" {
			t.Errorf("Private annotation lost after patch: %v", privAnnotations)
		}
	})

	t.Run("Patch/Preservation/private conditions survive spec-only patch", func(t *testing.T) {
		name := "patch-pres-cond"

		privObj := baseObj(name)
		privObj["status"] = map[string]interface{}{
			"conditions": []string{
				"Ready",
				"private.orlop.gcp.managed.openshift.io/Sync",
				"private.orlop.gcp.managed.openshift.io/Reconcile",
			},
		}
		privateCreate(t, name, privObj)
		defer cleanup(t, name)

		// Spec-only patch
		publicPatch(t, name, map[string]interface{}{
			"spec": map[string]interface{}{"publicField": "patched"},
		})

		priv := privateGet(t, name)
		conds := conditions(priv)
		privateConds := 0
		for _, c := range conds {
			if condStr, ok := c.(string); ok && hasPrivatePrefix(condStr) {
				privateConds++
			}
		}
		if privateConds != 2 {
			t.Errorf("Expected 2 private conditions after spec-only patch, got %d. Conditions: %v", privateConds, conds)
		}
	})

	t.Run("Patch/Preservation/finalizers survive", func(t *testing.T) {
		name := "patch-pres-fin"
		publicCreate(t, name, baseObj(name))
		defer cleanup(t, name)

		priv := privateGet(t, name)
		meta(priv)["finalizers"] = []string{"test.io/fin"}
		privateUpdate(t, name, priv)

		publicPatch(t, name, map[string]interface{}{
			"spec": map[string]interface{}{"publicField": "patched"},
		})

		priv2 := privateGet(t, name)
		fins := meta(priv2)["finalizers"].([]interface{})
		if len(fins) != 1 || fins[0] != "test.io/fin" {
			t.Errorf("Finalizer lost after patch: %v", fins)
		}
	})

	t.Run("Patch/Preservation/internal spec fields survive", func(t *testing.T) {
		name := "patch-pres-spec"

		privObj := baseObj(name)
		spec(privObj)["internalField"] = "secret-data"
		spec(privObj)["nested"].(map[string]interface{})["internalField"] = "nested-secret"
		privateCreate(t, name, privObj)
		defer cleanup(t, name)

		publicPatch(t, name, map[string]interface{}{
			"spec": map[string]interface{}{"publicField": "patched"},
		})

		priv := privateGet(t, name)
		if spec(priv)["internalField"] != "secret-data" {
			t.Errorf("Internal spec field lost after patch: %v", spec(priv)["internalField"])
		}
	})

	// =========================================================================
	// RESPONSE BODY — LEAKAGE: Private fields stripped from all response bodies
	// =========================================================================

	t.Run("ResponseLeakage/Update response strips private fields", func(t *testing.T) {
		name := "resp-leak-update"

		privObj := baseObj(name)
		spec(privObj)["internalField"] = "secret"
		meta(privObj)["labels"] = map[string]interface{}{
			"private.orlop.gcp.managed.openshift.io/owner": "controller",
		}
		meta(privObj)["finalizers"] = []string{"test.io/fin"}
		privObj["status"] = map[string]interface{}{
			"conditions": []string{"Ready", "private.orlop.gcp.managed.openshift.io/Sync"},
		}
		privateCreate(t, name, privObj)
		defer cleanup(t, name)

		pub := publicGet(t, name)
		updateObj := baseObj(name)
		meta(updateObj)["resourceVersion"] = rv(pub)
		resp := publicUpdate(t, name, updateObj)

		// Check response body for leaks
		if _, exists := spec(resp)["internalField"]; exists {
			t.Error("internalField leaked in update response")
		}
		if _, exists := meta(resp)["finalizers"]; exists {
			t.Error("Finalizers leaked in update response")
		}
		respLabels, _ := meta(resp)["labels"].(map[string]interface{})
		for k := range respLabels {
			if hasPrivatePrefix(k) {
				t.Errorf("Private label %q leaked in update response", k)
			}
		}
		for _, c := range conditions(resp) {
			if condStr, ok := c.(string); ok && hasPrivatePrefix(condStr) {
				t.Errorf("Private condition %q leaked in update response", condStr)
			}
		}
	})

	t.Run("ResponseLeakage/Patch response strips private fields", func(t *testing.T) {
		name := "resp-leak-patch"

		privObj := baseObj(name)
		spec(privObj)["internalField"] = "secret"
		meta(privObj)["labels"] = map[string]interface{}{
			"private.orlop.gcp.managed.openshift.io/owner": "controller",
		}
		meta(privObj)["finalizers"] = []string{"test.io/fin"}
		privObj["status"] = map[string]interface{}{
			"conditions": []string{"Ready", "private.orlop.gcp.managed.openshift.io/Sync"},
		}
		privateCreate(t, name, privObj)
		defer cleanup(t, name)

		resp := publicPatch(t, name, map[string]interface{}{
			"spec": map[string]interface{}{"publicField": "patched"},
		})

		if _, exists := spec(resp)["internalField"]; exists {
			t.Error("internalField leaked in patch response")
		}
		if _, exists := meta(resp)["finalizers"]; exists {
			t.Error("Finalizers leaked in patch response")
		}
		for _, c := range conditions(resp) {
			if condStr, ok := c.(string); ok && hasPrivatePrefix(condStr) {
				t.Errorf("Private condition %q leaked in patch response", condStr)
			}
		}
	})

	// ── MetadataRemoval: verify public labels/annotations can be removed via PUT ──

	t.Run("Update/MetadataRemoval/public_label_removed_via_PUT", func(t *testing.T) {
		name := "update-rm-label"
		createObj := baseObj(name)
		meta(createObj)["labels"] = map[string]interface{}{"app": "myapp", "env": "staging"}
		publicCreate(t, name, createObj)
		defer cleanup(t, name)

		// Add a private label via private API
		priv := privateGet(t, name)
		privMeta := meta(priv)
		privMeta["labels"] = map[string]interface{}{
			"app": "myapp",
			"env": "staging",
			"private.orlop.gcp.managed.openshift.io/owner": "controller",
		}
		privateUpdate(t, name, priv)

		// Public PUT that removes "env" label (only sends "app")
		pub := publicGet(t, name)
		updateObj := baseObj(name)
		meta(updateObj)["resourceVersion"] = rv(pub)
		meta(updateObj)["labels"] = map[string]interface{}{"app": "myapp"}
		publicUpdate(t, name, updateObj)

		// Verify via private API
		priv2 := privateGet(t, name)
		privLabels := meta(priv2)["labels"].(map[string]interface{})

		// "env" should be gone
		if _, exists := privLabels["env"]; exists {
			t.Errorf("Public label 'env' not removed by PUT: %v", privLabels)
		}
		// "app" should remain
		if privLabels["app"] != "myapp" {
			t.Errorf("Public label 'app' lost: %v", privLabels)
		}
		// private label should survive
		if privLabels["private.orlop.gcp.managed.openshift.io/owner"] != "controller" {
			t.Errorf("Private label lost after public PUT: %v", privLabels)
		}
	})

	t.Run("Update/MetadataRemoval/public_annotation_removed_via_PUT", func(t *testing.T) {
		name := "update-rm-annot"
		createObj := baseObj(name)
		meta(createObj)["annotations"] = map[string]interface{}{"note": "keep", "temp": "remove-me"}
		publicCreate(t, name, createObj)
		defer cleanup(t, name)

		// Add a private annotation via private API
		priv := privateGet(t, name)
		privMeta := meta(priv)
		privMeta["annotations"] = map[string]interface{}{
			"note": "keep",
			"temp": "remove-me",
			"private.orlop.gcp.managed.openshift.io/sync": "done",
		}
		privateUpdate(t, name, priv)

		// Public PUT that removes "temp" annotation
		pub := publicGet(t, name)
		updateObj := baseObj(name)
		meta(updateObj)["resourceVersion"] = rv(pub)
		meta(updateObj)["annotations"] = map[string]interface{}{"note": "keep"}
		publicUpdate(t, name, updateObj)

		// Verify via private API
		priv2 := privateGet(t, name)
		privAnnotations := meta(priv2)["annotations"].(map[string]interface{})

		if _, exists := privAnnotations["temp"]; exists {
			t.Errorf("Public annotation 'temp' not removed by PUT: %v", privAnnotations)
		}
		if privAnnotations["note"] != "keep" {
			t.Errorf("Public annotation 'note' lost: %v", privAnnotations)
		}
		if privAnnotations["private.orlop.gcp.managed.openshift.io/sync"] != "done" {
			t.Errorf("Private annotation lost after public PUT: %v", privAnnotations)
		}
	})

	t.Run("Update/MetadataRemoval/all_public_labels_removed_via_PUT", func(t *testing.T) {
		name := "update-rm-all-labels"
		createObj := baseObj(name)
		meta(createObj)["labels"] = map[string]interface{}{"app": "myapp"}
		publicCreate(t, name, createObj)
		defer cleanup(t, name)

		// Add private label via private API
		priv := privateGet(t, name)
		privMeta := meta(priv)
		privMeta["labels"] = map[string]interface{}{
			"app": "myapp",
			"private.orlop.gcp.managed.openshift.io/owner": "controller",
		}
		privateUpdate(t, name, priv)

		// Public PUT with NO labels (empty metadata)
		pub := publicGet(t, name)
		updateObj := baseObj(name)
		meta(updateObj)["resourceVersion"] = rv(pub)
		// No "labels" key at all — should remove all public labels
		publicUpdate(t, name, updateObj)

		// Verify via private API: only private label remains
		priv2 := privateGet(t, name)
		privLabels, _ := meta(priv2)["labels"].(map[string]interface{})

		if _, exists := privLabels["app"]; exists {
			t.Errorf("Public label 'app' not removed when PUT omits labels: %v", privLabels)
		}
		if privLabels["private.orlop.gcp.managed.openshift.io/owner"] != "controller" {
			t.Errorf("Private label lost when PUT omits labels: %v", privLabels)
		}
	})

	// =========================================================================
	// NEW HELPERS — publicList, publicDelete, privatePatch
	// =========================================================================

	publicList := func(t *testing.T) map[string]interface{} {
		t.Helper()
		code, result := do(t, pubClient, http.MethodGet, publicURL+apiPath, nil)
		if code != http.StatusOK {
			t.Fatalf("public list: expected 200, got %d: %v", code, result)
		}
		return result
	}

	publicDelete := func(t *testing.T, name string) (int, map[string]interface{}) {
		t.Helper()
		return do(t, pubClient, http.MethodDelete, publicURL+apiPath+"/"+name, nil)
	}

	privatePatch := func(t *testing.T, name string, patch map[string]interface{}) map[string]interface{} {
		t.Helper()
		code, result := do(t, privClient, http.MethodPatch, privateURL+apiPath+"/"+name, patch)
		if code != http.StatusOK {
			t.Fatalf("private patch %s: expected 200, got %d: %v", name, code, result)
		}
		return result
	}

	// === LIST ===

	t.Run("List/Leakage/all_private_fields_stripped", func(t *testing.T) {
		name := "list-leak-1"
		// Create via private API with ALL private field types
		privObj := baseObj(name)
		meta(privObj)["labels"] = map[string]interface{}{
			"app": "myapp",
			"private.orlop.gcp.managed.openshift.io/owner": "controller",
		}
		meta(privObj)["annotations"] = map[string]interface{}{
			"note": "public-note",
			"private.orlop.gcp.managed.openshift.io/sync": "done",
		}
		meta(privObj)["finalizers"] = []string{"test.io/fin"}
		spec(privObj)["internalField"] = "secret-data"
		spec(privObj)["nested"].(map[string]interface{})["internalField"] = "nested-secret"
		privObj["status"] = map[string]interface{}{
			"conditions": []string{"Ready", "private.orlop.gcp.managed.openshift.io/Sync"},
		}
		privateCreate(t, name, privObj)
		defer cleanup(t, name)

		listResp := publicList(t)
		items, ok := listResp["items"].([]interface{})
		if !ok {
			t.Fatalf("list response missing items array")
		}

		found := false
		for _, rawItem := range items {
			item, ok := rawItem.(map[string]interface{})
			if !ok {
				continue
			}
			m := meta(item)
			if m["name"] != name {
				continue
			}
			found = true

			// No private labels
			labels, _ := m["labels"].(map[string]interface{})
			for k := range labels {
				if hasPrivatePrefix(k) {
					t.Errorf("Private label %q leaked in list item", k)
				}
			}
			// Public label present
			if labels["app"] != "myapp" {
				t.Errorf("Public label missing in list item")
			}

			// No private annotations
			annotations, _ := m["annotations"].(map[string]interface{})
			for k := range annotations {
				if hasPrivatePrefix(k) {
					t.Errorf("Private annotation %q leaked in list item", k)
				}
			}

			// No finalizers
			if _, exists := m["finalizers"]; exists {
				t.Errorf("Finalizers leaked in list item")
			}

			// No internal spec fields
			s := spec(item)
			if _, exists := s["internalField"]; exists {
				t.Errorf("spec.internalField leaked in list item")
			}
			nested, _ := s["nested"].(map[string]interface{})
			if _, exists := nested["internalField"]; exists {
				t.Errorf("spec.nested.internalField leaked in list item")
			}

			// No private conditions
			for _, c := range conditions(item) {
				if condStr, ok := c.(string); ok && hasPrivatePrefix(condStr) {
					t.Errorf("Private condition %q leaked in list item", condStr)
				}
			}

			// Public fields present
			if s["publicField"] != "value" {
				t.Errorf("Public spec field missing in list item")
			}
		}
		if !found {
			t.Fatalf("object %q not found in list response", name)
		}
	})

	// === WATCH ===

	t.Run("Watch/Leakage/all_private_fields_stripped", func(t *testing.T) {
		name := "watch-leak-1"

		// Create via private API with all private fields
		privObj := baseObj(name)
		meta(privObj)["labels"] = map[string]interface{}{
			"app": "myapp",
			"private.orlop.gcp.managed.openshift.io/owner": "controller",
		}
		meta(privObj)["annotations"] = map[string]interface{}{
			"note": "visible",
			"private.orlop.gcp.managed.openshift.io/sync": "done",
		}
		meta(privObj)["finalizers"] = []string{"test.io/fin"}
		spec(privObj)["internalField"] = "secret-data"
		spec(privObj)["nested"].(map[string]interface{})["internalField"] = "nested-secret"
		privObj["status"] = map[string]interface{}{
			"conditions": []string{"Ready", "private.orlop.gcp.managed.openshift.io/Sync"},
		}
		privateCreate(t, name, privObj)
		defer cleanup(t, name)

		// Start watch on public API with timeout
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()

		watchReq, err := http.NewRequestWithContext(ctx, http.MethodGet, publicURL+apiPath+"?watch=true", nil)
		if err != nil {
			t.Fatalf("watch request error: %v", err)
		}
		watchResp, err := pubClient.Do(watchReq)
		if err != nil {
			t.Fatalf("watch HTTP error: %v", err)
		}
		defer watchResp.Body.Close()

		// Trigger MODIFIED event via private API
		priv := privateGet(t, name)
		spec(priv)["internalField"] = "updated-secret"
		privateUpdate(t, name, priv)

		// Read events
		decoder := json.NewDecoder(watchResp.Body)
		eventChecked := false
		for {
			var event map[string]interface{}
			if err := decoder.Decode(&event); err != nil {
				break // timeout or EOF
			}
			obj, ok := event["object"].(map[string]interface{})
			if !ok {
				continue
			}
			m, ok := obj["metadata"].(map[string]interface{})
			if !ok || m["name"] != name {
				continue
			}

			eventChecked = true

			// No private labels
			labels, _ := m["labels"].(map[string]interface{})
			for k := range labels {
				if hasPrivatePrefix(k) {
					t.Errorf("Private label %q leaked in watch event", k)
				}
			}

			// No private annotations
			annotations, _ := m["annotations"].(map[string]interface{})
			for k := range annotations {
				if hasPrivatePrefix(k) {
					t.Errorf("Private annotation %q leaked in watch event", k)
				}
			}

			// No finalizers
			if _, exists := m["finalizers"]; exists {
				t.Errorf("Finalizers leaked in watch event")
			}

			// No internal spec fields
			s, _ := obj["spec"].(map[string]interface{})
			if _, exists := s["internalField"]; exists {
				t.Errorf("spec.internalField leaked in watch event")
			}
			nested, _ := s["nested"].(map[string]interface{})
			if _, exists := nested["internalField"]; exists {
				t.Errorf("spec.nested.internalField leaked in watch event")
			}

			// No private conditions
			if status, ok := obj["status"].(map[string]interface{}); ok {
				if conds, ok := status["conditions"].([]interface{}); ok {
					for _, c := range conds {
						if condStr, ok := c.(string); ok && hasPrivatePrefix(condStr) {
							t.Errorf("Private condition %q leaked in watch event", condStr)
						}
					}
				}
			}
		}
		if !eventChecked {
			t.Errorf("No watch events received for object %q", name)
		}
	})

	// === DELETE ===

	t.Run("Delete/Leakage/soft_delete_response_strips_private_fields", func(t *testing.T) {
		name := "delete-leak-1"

		// Create via private API with all private fields + finalizer (for soft-delete)
		privObj := baseObj(name)
		meta(privObj)["labels"] = map[string]interface{}{
			"app": "myapp",
			"private.orlop.gcp.managed.openshift.io/owner": "controller",
		}
		meta(privObj)["annotations"] = map[string]interface{}{
			"note": "visible",
			"private.orlop.gcp.managed.openshift.io/sync": "done",
		}
		meta(privObj)["finalizers"] = []string{"test.io/prevent-delete"}
		spec(privObj)["internalField"] = "secret-data"
		privObj["status"] = map[string]interface{}{
			"conditions": []string{"Ready", "private.orlop.gcp.managed.openshift.io/Sync"},
		}
		privateCreate(t, name, privObj)
		defer cleanup(t, name)

		// Public DELETE — soft-delete because finalizer exists
		code, resp := publicDelete(t, name)
		if code != http.StatusOK {
			t.Fatalf("expected 200 for soft-delete, got %d: %v", code, resp)
		}

		// Response should be the object (not Status), with deletionTimestamp set
		m := meta(resp)
		if m["deletionTimestamp"] == nil {
			t.Errorf("deletionTimestamp missing in soft-delete response")
		}

		// No private labels
		labels, _ := m["labels"].(map[string]interface{})
		for k := range labels {
			if hasPrivatePrefix(k) {
				t.Errorf("Private label %q leaked in delete response", k)
			}
		}

		// No private annotations
		annotations, _ := m["annotations"].(map[string]interface{})
		for k := range annotations {
			if hasPrivatePrefix(k) {
				t.Errorf("Private annotation %q leaked in delete response", k)
			}
		}

		// No finalizers in response
		if _, exists := m["finalizers"]; exists {
			t.Errorf("Finalizers leaked in delete response")
		}

		// No internal spec
		if _, exists := spec(resp)["internalField"]; exists {
			t.Errorf("spec.internalField leaked in delete response")
		}

		// No private conditions
		for _, c := range conditions(resp) {
			if condStr, ok := c.(string); ok && hasPrivatePrefix(condStr) {
				t.Errorf("Private condition %q leaked in delete response", condStr)
			}
		}
	})

	// === DELETION TIMESTAMP ===

	t.Run("DeletionTimestamp/injection_via_create", func(t *testing.T) {
		name := "c4-dt-inject"
		obj := baseObj(name)
		meta(obj)["deletionTimestamp"] = "2024-01-01T00:00:00Z"

		resp := publicCreate(t, name, obj)
		defer cleanup(t, name)

		// Response should not have deletionTimestamp
		if meta(resp)["deletionTimestamp"] != nil {
			t.Errorf("deletionTimestamp present in create response")
		}

		// Storage should not have it
		priv := privateGet(t, name)
		if meta(priv)["deletionTimestamp"] != nil {
			t.Errorf("deletionTimestamp injected into storage via public create")
		}
	})

	t.Run("DeletionTimestamp/patch_clear_attempt", func(t *testing.T) {
		name := "c5-dt-clear"
		// 1. Create via public
		publicCreate(t, name, baseObj(name))
		defer cleanup(t, name)

		// 2. Add finalizer via private API
		priv := privateGet(t, name)
		meta(priv)["finalizers"] = []string{"test.io/prevent-delete"}
		privateUpdate(t, name, priv)

		// 3. Delete via public (soft-delete → sets deletionTimestamp)
		code, _ := publicDelete(t, name)
		if code != http.StatusOK {
			t.Fatalf("expected 200 soft-delete, got %d", code)
		}

		// Verify deletionTimestamp is set
		priv2 := privateGet(t, name)
		if meta(priv2)["deletionTimestamp"] == nil {
			t.Fatalf("deletionTimestamp not set after soft-delete")
		}

		// 4. Patch via public trying to clear deletionTimestamp
		publicPatch(t, name, map[string]interface{}{
			"metadata": map[string]interface{}{
				"deletionTimestamp": nil,
			},
		})

		// 5. Verify still set
		priv3 := privateGet(t, name)
		if meta(priv3)["deletionTimestamp"] == nil {
			t.Errorf("deletionTimestamp cleared via public patch — should be immutable")
		}
	})

	// === PATCH TAMPERING ===

	t.Run("Patch/Tampering/private_labels_unchanged", func(t *testing.T) {
		name := "h1-patch-label-tamper"
		publicCreate(t, name, baseObj(name))
		defer cleanup(t, name)

		// Set private label via private API
		priv := privateGet(t, name)
		meta(priv)["labels"] = map[string]interface{}{
			"private.orlop.gcp.managed.openshift.io/owner": "controller-original",
		}
		privateUpdate(t, name, priv)

		// Attempt tamper via public patch
		publicPatch(t, name, map[string]interface{}{
			"metadata": map[string]interface{}{
				"labels": map[string]interface{}{
					"private.orlop.gcp.managed.openshift.io/owner": "attacker-value",
				},
			},
		})

		priv2 := privateGet(t, name)
		privLabels := meta(priv2)["labels"].(map[string]interface{})
		if privLabels["private.orlop.gcp.managed.openshift.io/owner"] != "controller-original" {
			t.Errorf("Private label tampered via public patch: got %v", privLabels["private.orlop.gcp.managed.openshift.io/owner"])
		}
	})

	t.Run("Patch/Tampering/private_annotations_unchanged", func(t *testing.T) {
		name := "h2-patch-annot-tamper"
		publicCreate(t, name, baseObj(name))
		defer cleanup(t, name)

		priv := privateGet(t, name)
		meta(priv)["annotations"] = map[string]interface{}{
			"private.orlop.gcp.managed.openshift.io/sync": "original",
		}
		privateUpdate(t, name, priv)

		publicPatch(t, name, map[string]interface{}{
			"metadata": map[string]interface{}{
				"annotations": map[string]interface{}{
					"private.orlop.gcp.managed.openshift.io/sync": "tampered",
				},
			},
		})

		priv2 := privateGet(t, name)
		privAnnotations := meta(priv2)["annotations"].(map[string]interface{})
		if privAnnotations["private.orlop.gcp.managed.openshift.io/sync"] != "original" {
			t.Errorf("Private annotation tampered via public patch: got %v", privAnnotations["private.orlop.gcp.managed.openshift.io/sync"])
		}
	})

	t.Run("Patch/Tampering/private_conditions_unchanged", func(t *testing.T) {
		name := "h3-patch-cond-tamper"
		privObj := baseObj(name)
		privObj["status"] = map[string]interface{}{
			"conditions": []string{"Ready", "private.orlop.gcp.managed.openshift.io/Sync"},
		}
		privateCreate(t, name, privObj)
		defer cleanup(t, name)

		// Patch with modified private condition
		publicPatch(t, name, map[string]interface{}{
			"status": map[string]interface{}{
				"conditions": []string{"Ready", "private.orlop.gcp.managed.openshift.io/Tampered"},
			},
		})

		priv := privateGet(t, name)
		conds := conditions(priv)
		foundOriginal := false
		for _, c := range conds {
			if condStr, ok := c.(string); ok {
				if condStr == "private.orlop.gcp.managed.openshift.io/Sync" {
					foundOriginal = true
				}
				if condStr == "private.orlop.gcp.managed.openshift.io/Tampered" {
					t.Errorf("Tampered private condition injected via patch")
				}
			}
		}
		if !foundOriginal {
			t.Errorf("Original private condition lost after patch tamper attempt. Conditions: %v", conds)
		}
	})

	// === UPDATE (PUT) TAMPERING ===

	t.Run("Update/Tampering/private_conditions_unchanged", func(t *testing.T) {
		name := "h4-update-cond-tamper"
		privObj := baseObj(name)
		privObj["status"] = map[string]interface{}{
			"conditions": []string{"Ready", "private.orlop.gcp.managed.openshift.io/Sync"},
		}
		privateCreate(t, name, privObj)
		defer cleanup(t, name)

		pub := publicGet(t, name)
		updateObj := baseObj(name)
		meta(updateObj)["resourceVersion"] = rv(pub)
		updateObj["status"] = map[string]interface{}{
			"conditions": []string{"Ready", "private.orlop.gcp.managed.openshift.io/DifferentPrivate"},
		}
		publicUpdate(t, name, updateObj)

		priv := privateGet(t, name)
		conds := conditions(priv)
		foundOriginal := false
		for _, c := range conds {
			if condStr, ok := c.(string); ok {
				if condStr == "private.orlop.gcp.managed.openshift.io/Sync" {
					foundOriginal = true
				}
				if condStr == "private.orlop.gcp.managed.openshift.io/DifferentPrivate" {
					t.Errorf("Injected private condition via PUT tamper")
				}
			}
		}
		if !foundOriginal {
			t.Errorf("Original private condition lost after PUT tamper. Conditions: %v", conds)
		}
	})

	// === FINALIZER TAMPERING (explicit empty array) ===

	t.Run("Update/Tampering/finalizers_explicit_empty_array_via_PUT", func(t *testing.T) {
		name := "h6-fin-empty-put"
		publicCreate(t, name, baseObj(name))
		defer cleanup(t, name)

		// Add finalizer via private API
		priv := privateGet(t, name)
		meta(priv)["finalizers"] = []string{"test.io/controller-fin"}
		privateUpdate(t, name, priv)

		// Public PUT with explicit empty finalizers
		pub := publicGet(t, name)
		updateObj := baseObj(name)
		meta(updateObj)["resourceVersion"] = rv(pub)
		meta(updateObj)["finalizers"] = []interface{}{}
		publicUpdate(t, name, updateObj)

		priv2 := privateGet(t, name)
		fins, _ := meta(priv2)["finalizers"].([]interface{})
		if len(fins) != 1 || fins[0] != "test.io/controller-fin" {
			t.Errorf("Finalizer cleared by explicit empty array in PUT: %v", fins)
		}
	})

	t.Run("Patch/Tampering/finalizers_explicit_empty_array_via_Patch", func(t *testing.T) {
		name := "h6-fin-empty-patch"
		publicCreate(t, name, baseObj(name))
		defer cleanup(t, name)

		priv := privateGet(t, name)
		meta(priv)["finalizers"] = []string{"test.io/controller-fin"}
		privateUpdate(t, name, priv)

		publicPatch(t, name, map[string]interface{}{
			"metadata": map[string]interface{}{
				"finalizers": []interface{}{},
			},
		})

		priv2 := privateGet(t, name)
		fins, _ := meta(priv2)["finalizers"].([]interface{})
		if len(fins) != 1 || fins[0] != "test.io/controller-fin" {
			t.Errorf("Finalizer cleared by explicit empty array in Patch: %v", fins)
		}
	})

	// === GET LEAKAGE ===

	t.Run("Get/Leakage/all_private_fields_stripped", func(t *testing.T) {
		name := "h9-get-leak"
		privObj := baseObj(name)
		meta(privObj)["labels"] = map[string]interface{}{
			"app": "myapp",
			"private.orlop.gcp.managed.openshift.io/owner": "controller",
		}
		meta(privObj)["annotations"] = map[string]interface{}{
			"note": "visible",
			"private.orlop.gcp.managed.openshift.io/sync": "done",
		}
		meta(privObj)["finalizers"] = []string{"test.io/fin"}
		spec(privObj)["internalField"] = "secret-data"
		spec(privObj)["nested"].(map[string]interface{})["internalField"] = "nested-secret"
		privObj["status"] = map[string]interface{}{
			"conditions": []string{"Ready", "private.orlop.gcp.managed.openshift.io/Sync"},
		}
		privateCreate(t, name, privObj)
		defer cleanup(t, name)

		pub := publicGet(t, name)

		// No private labels
		labels, _ := meta(pub)["labels"].(map[string]interface{})
		for k := range labels {
			if hasPrivatePrefix(k) {
				t.Errorf("Private label %q leaked in GET", k)
			}
		}

		// No private annotations
		annotations, _ := meta(pub)["annotations"].(map[string]interface{})
		for k := range annotations {
			if hasPrivatePrefix(k) {
				t.Errorf("Private annotation %q leaked in GET", k)
			}
		}

		// No finalizers
		if _, exists := meta(pub)["finalizers"]; exists {
			t.Errorf("Finalizers leaked in GET")
		}

		// No internal spec
		if _, exists := spec(pub)["internalField"]; exists {
			t.Errorf("spec.internalField leaked in GET")
		}
		nested, _ := spec(pub)["nested"].(map[string]interface{})
		if _, exists := nested["internalField"]; exists {
			t.Errorf("spec.nested.internalField leaked in GET")
		}

		// No private conditions
		for _, c := range conditions(pub) {
			if condStr, ok := c.(string); ok && hasPrivatePrefix(condStr) {
				t.Errorf("Private condition %q leaked in GET", condStr)
			}
		}

		// Public fields present
		if labels["app"] != "myapp" {
			t.Errorf("Public label missing in GET")
		}
		if spec(pub)["publicField"] != "value" {
			t.Errorf("Public spec field missing in GET")
		}
	})

	// === CREATE RESPONSE — holistic check (M1) ===

	t.Run("Create/ResponseCheck/holistic_private_field_check", func(t *testing.T) {
		name := "m1-create-resp"
		obj := baseObj(name)
		meta(obj)["labels"] = map[string]interface{}{
			"app": "myapp",
			"private.orlop.gcp.managed.openshift.io/owner": "attack",
		}
		meta(obj)["annotations"] = map[string]interface{}{
			"private.orlop.gcp.managed.openshift.io/sync": "attack",
		}
		meta(obj)["finalizers"] = []string{"test.io/fin"}
		obj["status"] = map[string]interface{}{
			"conditions": []string{"Ready", "private.orlop.gcp.managed.openshift.io/Sync"},
		}

		resp := publicCreate(t, name, obj)
		defer cleanup(t, name)

		// Check response body directly
		respLabels, _ := meta(resp)["labels"].(map[string]interface{})
		for k := range respLabels {
			if hasPrivatePrefix(k) {
				t.Errorf("Private label %q in create response", k)
			}
		}

		respAnnotations, _ := meta(resp)["annotations"].(map[string]interface{})
		for k := range respAnnotations {
			if hasPrivatePrefix(k) {
				t.Errorf("Private annotation %q in create response", k)
			}
		}

		if _, exists := meta(resp)["finalizers"]; exists {
			t.Errorf("Finalizers in create response")
		}

		for _, c := range conditions(resp) {
			if condStr, ok := c.(string); ok && hasPrivatePrefix(condStr) {
				t.Errorf("Private condition %q in create response", condStr)
			}
		}

		// Public fields present
		if respLabels["app"] != "myapp" {
			t.Errorf("Public label missing from create response")
		}
	})

	// === PATCH with status conditions — status writes blocked (M2 + GCP-1062) ===

	t.Run("Patch/Conditions/status_patch_ignored_preserves_existing", func(t *testing.T) {
		name := "m2-patch-cond-blocked"
		privObj := baseObj(name)
		privObj["status"] = map[string]interface{}{
			"conditions": []string{"Ready", "private.orlop.gcp.managed.openshift.io/Sync"},
		}
		privateCreate(t, name, privObj)
		defer cleanup(t, name)

		// Attempt to patch status via public API (should be ignored/stripped)
		publicPatch(t, name, map[string]interface{}{
			"status": map[string]interface{}{
				"conditions": []string{"Ready", "Available"},
			},
		})

		priv := privateGet(t, name)
		conds := conditions(priv)

		// Original controller-set conditions preserved (patch ignored)
		foundPrivate := false
		foundReady := false
		foundAvailable := false
		for _, c := range conds {
			if condStr, ok := c.(string); ok {
				if condStr == "private.orlop.gcp.managed.openshift.io/Sync" {
					foundPrivate = true
				}
				if condStr == "Ready" {
					foundReady = true
				}
				if condStr == "Available" {
					foundAvailable = true
				}
			}
		}
		if !foundPrivate {
			t.Errorf("Private condition lost after public status patch attempt. Conditions: %v", conds)
		}
		if !foundReady {
			t.Errorf("Ready condition lost after public status patch attempt. Conditions: %v", conds)
		}
		if foundAvailable {
			t.Errorf("Public API status patch was applied (security issue). Conditions: %v", conds)
		}
	})

	// === CREATE — status write blocked (GCP-1062) ===

	t.Run("Create/StatusBlocked/status_not_persisted", func(t *testing.T) {
		name := "create-status-blocked"
		createObj := baseObj(name)
		createObj["status"] = map[string]interface{}{
			"conditions": []string{"Ready", "Available"},
		}
		publicCreate(t, name, createObj)
		defer cleanup(t, name)

		// Verify status was not persisted
		priv := privateGet(t, name)
		conds := conditions(priv)
		if len(conds) != 0 {
			t.Errorf("Public API Create persisted status (security issue). Conditions: %v", conds)
		}
	})

	// === UPDATE (PUT) — status write blocked (GCP-1062) ===

	t.Run("Update/StatusBlocked/existing_status_preserved", func(t *testing.T) {
		name := "update-status-blocked"

		// Create via private API with status
		privObj := baseObj(name)
		privObj["status"] = map[string]interface{}{
			"conditions": []string{"Ready", "private.orlop.gcp.managed.openshift.io/Sync"},
		}
		privateCreate(t, name, privObj)
		defer cleanup(t, name)

		// Attempt to overwrite via public API
		pub := publicGet(t, name)
		updateObj := baseObj(name)
		meta(updateObj)["resourceVersion"] = rv(pub)
		updateObj["status"] = map[string]interface{}{
			"conditions": []string{"Failed", "Available"},
		}
		publicUpdate(t, name, updateObj)

		// Verify controller-set status preserved
		priv := privateGet(t, name)
		conds := conditions(priv)

		foundReady := false
		foundPrivateSync := false
		foundFailed := false
		foundAvailable := false
		for _, c := range conds {
			if condStr, ok := c.(string); ok {
				if condStr == "Ready" {
					foundReady = true
				}
				if condStr == "private.orlop.gcp.managed.openshift.io/Sync" {
					foundPrivateSync = true
				}
				if condStr == "Failed" {
					foundFailed = true
				}
				if condStr == "Available" {
					foundAvailable = true
				}
			}
		}

		if !foundReady {
			t.Errorf("Ready condition lost after public Update (should be preserved). Conditions: %v", conds)
		}
		if !foundPrivateSync {
			t.Errorf("Private condition lost after public Update (should be preserved). Conditions: %v", conds)
		}
		if foundFailed || foundAvailable {
			t.Errorf("Public API Update applied status changes (security issue). Conditions: %v", conds)
		}
	})

	// === UPDATE (PUT) — internal spec field injection (M3) ===

	t.Run("Update/Injection/internal_spec_field_pruned", func(t *testing.T) {
		name := "m3-spec-inject"
		publicCreate(t, name, baseObj(name))
		defer cleanup(t, name)

		pub := publicGet(t, name)
		updateObj := baseObj(name)
		meta(updateObj)["resourceVersion"] = rv(pub)
		spec(updateObj)["internalField"] = "injected"

		publicUpdate(t, name, updateObj)

		priv := privateGet(t, name)
		if spec(priv)["internalField"] == "injected" {
			t.Errorf("internalField injected via public PUT")
		}
	})

	// === EMPTY FINALIZER EDGE CASES (M4) ===

	t.Run("Finalizers/EdgeCases/empty_array_on_create", func(t *testing.T) {
		name := "m4a-fin-empty-create"
		obj := baseObj(name)
		meta(obj)["finalizers"] = []interface{}{}

		publicCreate(t, name, obj)
		defer cleanup(t, name)

		priv := privateGet(t, name)
		fins, _ := meta(priv)["finalizers"].([]interface{})
		if len(fins) > 0 {
			t.Errorf("Empty finalizer array on create resulted in stored finalizers: %v", fins)
		}
	})

	t.Run("Finalizers/EdgeCases/null_on_PUT_preserves_existing", func(t *testing.T) {
		name := "m4b-fin-null-put"
		publicCreate(t, name, baseObj(name))
		defer cleanup(t, name)

		// Add finalizer via private API
		priv := privateGet(t, name)
		meta(priv)["finalizers"] = []string{"test.io/controller-fin"}
		privateUpdate(t, name, priv)

		// Public PUT with null finalizers
		pub := publicGet(t, name)
		updateObj := baseObj(name)
		meta(updateObj)["resourceVersion"] = rv(pub)
		meta(updateObj)["finalizers"] = nil
		publicUpdate(t, name, updateObj)

		priv2 := privateGet(t, name)
		fins, _ := meta(priv2)["finalizers"].([]interface{})
		if len(fins) != 1 || fins[0] != "test.io/controller-fin" {
			t.Errorf("Finalizer lost when PUT sends null finalizers: %v", fins)
		}
	})

	t.Run("Finalizers/EdgeCases/missing_key_on_PUT_preserves_existing", func(t *testing.T) {
		name := "m4c-fin-missing-put"
		publicCreate(t, name, baseObj(name))
		defer cleanup(t, name)

		// Add finalizer via private API
		priv := privateGet(t, name)
		meta(priv)["finalizers"] = []string{"test.io/controller-fin"}
		privateUpdate(t, name, priv)

		// Public PUT with no finalizers key at all
		pub := publicGet(t, name)
		updateObj := baseObj(name)
		meta(updateObj)["resourceVersion"] = rv(pub)
		// No "finalizers" key in metadata
		publicUpdate(t, name, updateObj)

		priv2 := privateGet(t, name)
		fins, _ := meta(priv2)["finalizers"].([]interface{})
		if len(fins) != 1 || fins[0] != "test.io/controller-fin" {
			t.Errorf("Finalizer lost when PUT omits finalizers key: %v", fins)
		}
	})

	// privatePatch is defined but not yet used in tests — remove suppression
	// when tests are added that exercise the private PATCH path.
	_ = privatePatch
}
