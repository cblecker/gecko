package v1_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-logr/logr"

	"github.com/openshift-online/gecko/orlop/pkg/apiserver/conversion"
	"github.com/openshift-online/gecko/orlop/pkg/apiserver/handlers"
	pkgschema "github.com/openshift-online/gecko/orlop/pkg/apiserver/schema"
	"github.com/openshift-online/gecko/orlop/pkg/apiserver/storage/memory"
	privatev1 "github.com/openshift-online/gecko/platform-api/api/private/v1"
	publicv1 "github.com/openshift-online/gecko/platform-api/api/public/v1"

	apiext "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	structuralschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"sigs.k8s.io/yaml"
)

func TestControlPlaneUpgradePolicyPublicAPIValidation(t *testing.T) {
	privateScheme, publicScheme := runtime.NewScheme(), runtime.NewScheme()
	if err := privatev1.AddToScheme(privateScheme); err != nil {
		t.Fatal(err)
	}
	if err := publicv1.AddToScheme(publicScheme); err != nil {
		t.Fatal(err)
	}
	gvk := privatev1.GroupVersion.WithKind("ControlPlaneUpgradePolicy")
	store := memory.NewMemoryStore("controlplaneupgradepolicies", privateScheme, gvk)
	handler := handlers.NewConvertingResourceHandler(store, maintenancePolicySchemaProcessor(t, publicv1.ControlPlaneUpgradePolicySchemaYAML),
		conversion.NewConverter(publicScheme, privateScheme, ""), gvk, "controlplaneupgradepolicies", publicScheme, privateScheme, nil, logr.Discard())
	base := "/apis/gcp.managed.openshift.io/v1/namespaces/{namespace}/controlplaneupgradepolicies"
	router := chi.NewRouter()
	router.Post(base, handler.Create)
	router.Put(base+"/{name}", handler.Update)
	router.Patch(base+"/{name}", handler.Patch)
	path := "/apis/gcp.managed.openshift.io/v1/namespaces/customer/controlplaneupgradepolicies"
	request := func(method, url string, body any) *httptest.ResponseRecorder {
		t.Helper()
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequestWithContext(t.Context(), method, url, bytes.NewReader(data))
		req.Header.Set("Content-Type", "application/json")
		if method == http.MethodPatch {
			req.Header.Set("Content-Type", "application/merge-patch+json")
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		return response
	}
	start := time.Date(2026, time.January, 5, 2, 0, 0, 0, time.UTC)
	policy := publicv1.ControlPlaneUpgradePolicy{
		TypeMeta:   metav1.TypeMeta{APIVersion: publicv1.GroupVersion.String(), Kind: "ControlPlaneUpgradePolicy"},
		ObjectMeta: metav1.ObjectMeta{Name: "test-policy", Namespace: "customer"},
		Spec: publicv1.ControlPlaneUpgradePolicySpec{
			ClusterID: "test-cluster",
			MaintenanceWindow: &publicv1.ControlPlaneMaintenanceWindow{
				Start: metav1.NewTime(start), DurationMinutes: 240,
				Recurrence: publicv1.ControlPlaneMaintenanceRecurrence{
					Frequency: "weekly", DaysOfWeek: []string{"monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday"},
				},
			},
			MaintenanceExclusions: []publicv1.ControlPlaneMaintenanceExclusion{{
				Name: "freeze", Start: metav1.NewTime(start.Add(100 * 24 * time.Hour)), End: metav1.NewTime(start.Add(120 * 24 * time.Hour)),
			}},
		},
	}
	invalid := policy.DeepCopy()
	invalid.Spec.MaintenanceWindow.DurationMinutes = 60
	assertRejected := func(response *httptest.ResponseRecorder) {
		t.Helper()
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "at least 48 hours") {
			t.Fatalf("expected descriptive HTTP 400, got %d: %s", response.Code, response.Body.String())
		}
	}
	assertRejected(request(http.MethodPost, path, invalid))
	invalidWithoutWindow := policy.DeepCopy()
	invalidWithoutWindow.Spec.MaintenanceWindow = nil
	freezeEnd := start.Add(130 * 24 * time.Hour)
	invalidWithoutWindow.Spec.MaintenanceExclusions = []publicv1.ControlPlaneMaintenanceExclusion{
		{Name: "freeze", Start: metav1.NewTime(start.Add(100 * 24 * time.Hour)), End: metav1.NewTime(freezeEnd)},
		{Name: "four-hours", Start: metav1.NewTime(freezeEnd), End: metav1.NewTime(freezeEnd.Add(4 * time.Hour))},
		{Name: "five-hours", Start: metav1.NewTime(freezeEnd.Add(4 * time.Hour)), End: metav1.NewTime(freezeEnd.Add(9 * time.Hour))},
	}
	assertRejected(request(http.MethodPost, path, invalidWithoutWindow))
	if _, err := store.Get(t.Context(), "customer", "test-policy"); !apierrors.IsNotFound(err) {
		t.Fatalf("invalid create persisted a policy: %v", err)
	}
	response := request(http.MethodPost, path, &policy)
	if response.Code != http.StatusCreated {
		t.Fatalf("valid create returned %d: %s", response.Code, response.Body.String())
	}
	if err := json.Unmarshal(response.Body.Bytes(), &policy); err != nil {
		t.Fatal(err)
	}
	invalid = policy.DeepCopy()
	invalid.Spec.MaintenanceWindow.DurationMinutes = 60
	assertRejected(request(http.MethodPut, path+"/test-policy", invalid))
	invalid = policy.DeepCopy()
	invalid.Spec.MaintenanceExclusions[0].End.Time = invalid.Spec.MaintenanceExclusions[0].End.Add(time.Second)
	assertRejected(request(http.MethodPut, path+"/test-policy", invalid))
	assertRejected(request(http.MethodPatch, path+"/test-policy", map[string]any{
		"spec": map[string]any{"maintenanceWindow": map[string]any{"durationMinutes": 60}},
	}))
	assertRejected(request(http.MethodPatch, path+"/test-policy", map[string]any{
		"spec": map[string]any{"maintenanceExclusions": invalid.Spec.MaintenanceExclusions},
	}))
	persisted, err := store.Get(t.Context(), "customer", "test-policy")
	if err != nil {
		t.Fatal(err)
	}
	if persisted.GetResourceVersion() != policy.ResourceVersion {
		t.Fatal("invalid update or patch changed the stored policy")
	}
	response = request(http.MethodPatch, path+"/test-policy", map[string]any{
		"spec": map[string]any{"maintenanceWindow": nil},
	})
	if response.Code != http.StatusOK {
		t.Fatalf("removing optional window returned %d: %s", response.Code, response.Body.String())
	}
	if err := json.Unmarshal(response.Body.Bytes(), &policy); err != nil {
		t.Fatal(err)
	}
	invalid = policy.DeepCopy()
	invalid.Spec.MaintenanceWindow = nil
	invalid.Spec.MaintenanceExclusions = invalidWithoutWindow.Spec.MaintenanceExclusions
	assertRejected(request(http.MethodPut, path+"/test-policy", invalid))
	assertRejected(request(http.MethodPatch, path+"/test-policy", map[string]any{
		"spec": map[string]any{"maintenanceExclusions": invalid.Spec.MaintenanceExclusions},
	}))
	persisted, err = store.Get(t.Context(), "customer", "test-policy")
	if err != nil {
		t.Fatal(err)
	}
	if persisted.GetResourceVersion() != policy.ResourceVersion {
		t.Fatal("invalid exclusions without a window changed the stored policy")
	}
}

func TestControlPlaneUpgradePolicyExclusionCountSchemas(t *testing.T) {
	for _, tc := range []struct{ name, schema string }{
		{"private", privatev1.ControlPlaneUpgradePolicySchemaYAML},
		{"public", publicv1.ControlPlaneUpgradePolicySchemaYAML},
	} {
		t.Run(tc.name, func(t *testing.T) {
			processor := maintenancePolicySchemaProcessor(t, tc.schema)
			for _, count := range []int{3, 4} {
				exclusions := make([]any, count)
				for i := range count {
					exclusions[i] = map[string]any{
						"name": string(rune('a' + i)), "start": "2026-01-01T00:00:00Z", "end": "2026-01-02T00:00:00Z",
					}
				}
				object := map[string]any{"spec": map[string]any{"clusterID": "test-cluster", "maintenanceExclusions": exclusions}}
				errs := processor.Process(t.Context(), object, nil)
				if (len(errs) > 0) != (count > 3) {
					t.Fatalf("count %d: schema errors = %v", count, errs)
				}
			}
		})
	}
}

func maintenancePolicySchemaProcessor(t *testing.T, schemaYAML string) *pkgschema.Processor {
	t.Helper()
	var propsV1 apiextv1.JSONSchemaProps
	if err := yaml.Unmarshal([]byte(schemaYAML), &propsV1); err != nil {
		t.Fatal(err)
	}
	var props apiext.JSONSchemaProps
	if err := apiextv1.Convert_v1_JSONSchemaProps_To_apiextensions_JSONSchemaProps(&propsV1, &props, nil); err != nil {
		t.Fatal(err)
	}
	structural, err := structuralschema.NewStructural(&props)
	if err != nil {
		t.Fatal(err)
	}
	processor, err := pkgschema.NewProcessor(structural, &props)
	if err != nil {
		t.Fatal(err)
	}
	return processor
}
