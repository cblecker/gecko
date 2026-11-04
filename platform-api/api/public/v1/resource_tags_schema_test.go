package v1

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	privatev1 "github.com/openshift-online/gecko/platform-api/api/private/v1"

	"k8s.io/utils/ptr"
)

// Exercise both generated schemas with the same request body: private and
// public admission must agree about the new public field.
func TestClusterSchemaResourceTags(t *testing.T) {
	fifty := make([]map[string]any, 50)
	for i := range fifty {
		fifty[i] = map[string]any{"key": fmt.Sprintf("tag%d", i), "value": "true"}
	}
	fiftyJSON, err := json.Marshal(fifty)
	if err != nil {
		t.Fatal(err)
	}
	fiftyOneJSON, err := json.Marshal(append(fifty, map[string]any{"key": "extra", "value": "true"}))
	if err != nil {
		t.Fatal(err)
	}
	for _, schema := range []struct{ name, yaml string }{{"public", ClusterSchemaYAML}, {"private", privatev1.ClusterSchemaYAML}} {
		t.Run(schema.name, func(t *testing.T) {
			processor := newSchemaProcessor(t, schema.yaml)
			for _, tc := range []struct {
				name, tags string
				invalid    bool
			}{
				{name: "omitted"},
				{name: "project default", tags: `[{"key":"redhat-managed","value":"true"}]`},
				{name: "organization", tags: `[{"key":"env","value":"production","parentID":"123456789"}]`},
				{name: "explicit project", tags: `[{"key":"env","value":"production","parentID":"test-project"}]`},
				{name: "value characters", tags: `[{"key":"Environment.Type","value":"A value_-.@%=+:,*#&()[]{} 1"}]`},
				{name: "maximum count", tags: string(fiftyJSON)},
				{name: "empty list", tags: `[]`, invalid: true},
				{name: "too many", tags: string(fiftyOneJSON), invalid: true},
				{name: "missing key", tags: `[{"value":"true"}]`, invalid: true},
				{name: "missing value", tags: `[{"key":"env"}]`, invalid: true},
				{name: "empty key", tags: `[{"key":"","value":"true"}]`, invalid: true},
				{name: "empty value", tags: `[{"key":"env","value":""}]`, invalid: true},
				{name: "long key", tags: `[{"key":"` + strings.Repeat("a", 64) + `","value":"true"}]`, invalid: true},
				{name: "invalid key", tags: `[{"key":"bad/key","value":"true"}]`, invalid: true},
				{name: "invalid value", tags: `[{"key":"env","value":"trailing-"}]`, invalid: true},
				{name: "duplicate key", tags: `[{"key":"env","value":"a"},{"key":"env","value":"b","parentID":"123456789"}]`, invalid: true},
				{name: "wrong project", tags: `[{"key":"env","value":"a","parentID":"other-project"}]`, invalid: true},
				{name: "empty parent", tags: `[{"key":"env","value":"a","parentID":""}]`, invalid: true},
				{name: "invalid parent", tags: `[{"key":"env","value":"a","parentID":"organizations/123"}]`, invalid: true},
				{name: "zero prefix parent", tags: `[{"key":"env","value":"a","parentID":"0123"}]`, invalid: true},
			} {
				t.Run(tc.name, func(t *testing.T) {
					object := resourceTagClusterObject(t, tc.tags)
					errs := processor.Process(t.Context(), object, nil)
					if tc.invalid {
						if len(errs) == 0 {
							t.Fatal("expected resource tag validation failure")
						}
					} else {
						if len(errs) != 0 {
							t.Fatalf("unexpected validation errors: %v", errs)
						}
						if tc.tags != "" {
							raw, err := json.Marshal(object["spec"].(map[string]any)["platform"].(map[string]any)["gcp"].(map[string]any)["resourceTags"])
							if err != nil {
								t.Fatal(err)
							}
							var want, got any
							if err := json.Unmarshal([]byte(tc.tags), &want); err != nil {
								t.Fatal(err)
							}
							if err := json.Unmarshal(raw, &got); err != nil {
								t.Fatal(err)
							}
							if !reflect.DeepEqual(want, got) {
								t.Fatalf("tags must not be pruned: got %s, want %s", raw, tc.tags)
							}
						}
					}
				})
			}
		})
	}
}

func TestClusterSchemaResourceTagsImmutable(t *testing.T) {
	const tags = `[{"key":"env","value":"prod","parentID":"123456789"}]`
	for _, schema := range []struct{ name, yaml string }{{"public", ClusterSchemaYAML}, {"private", privatev1.ClusterSchemaYAML}} {
		t.Run(schema.name, func(t *testing.T) {
			processor := newSchemaProcessor(t, schema.yaml)
			for _, tc := range []struct {
				name, oldTags, newTags string
				invalid                bool
			}{
				{name: "unchanged", oldTags: tags, newTags: tags},
				{name: "both omitted"},
				{name: "changed value", oldTags: tags, newTags: `[{"key":"env","value":"dev","parentID":"123456789"}]`, invalid: true},
				{name: "changed parent", oldTags: tags, newTags: `[{"key":"env","value":"prod","parentID":"987654321"}]`, invalid: true},
				{name: "changed key", oldTags: tags, newTags: `[{"key":"other","value":"prod","parentID":"123456789"}]`, invalid: true},
				{name: "added field", newTags: tags, invalid: true},
				{name: "removed field", oldTags: tags, invalid: true},
				{name: "added entry", oldTags: tags, newTags: `[{"key":"env","value":"prod","parentID":"123456789"},{"key":"other","value":"v"}]`, invalid: true},
			} {
				t.Run(tc.name, func(t *testing.T) {
					oldObject := resourceTagClusterObject(t, tc.oldTags)
					if errs := processor.Process(t.Context(), oldObject, nil); len(errs) != 0 {
						t.Fatalf("invalid old object: %v", errs)
					}
					object := resourceTagClusterObject(t, tc.newTags)
					// Unrelated mutable labels may still change when tags are unchanged.
					object["spec"].(map[string]any)["platform"].(map[string]any)["gcp"].(map[string]any)["resourceLabels"] = []any{map[string]any{"key": "env", "value": "updated"}}
					errs := processor.Process(t.Context(), object, oldObject)
					if tc.invalid {
						if len(errs) == 0 {
							t.Fatal("expected resource tag validation failure")
						}
						if !strings.Contains(errs.ToAggregate().Error(), "resourceTags") {
							t.Fatalf("expected resourceTags error, got: %v", errs)
						}
					} else {
						if len(errs) != 0 {
							t.Fatalf("unexpected validation errors: %v", errs)
						}
					}
				})
			}
		})
	}
}

func resourceTagClusterObject(t *testing.T, tags string) map[string]any {
	t.Helper()
	object := clusterAsMap(t, validCluster("4.22.1"))
	if tags != "" {
		var value any
		if err := json.Unmarshal([]byte(tags), &value); err != nil {
			t.Fatal(err)
		}
		object["spec"].(map[string]any)["platform"].(map[string]any)["gcp"].(map[string]any)["resourceTags"] = value
	}
	return object
}

func TestClusterResourceTagsConversionAndDeepCopy(t *testing.T) {
	cluster := validCluster("4.22.1")
	cluster.Spec.Platform.GCP.ResourceTags = []GCPResourceTag{{Key: "env", Value: "prod", ParentID: ptr.To("123456789")}}
	var private privatev1.Cluster
	if err := Convert_Cluster_PublicToPrivate(&cluster, &private, nil); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual([]privatev1.GCPResourceTag{{Key: "env", Value: "prod", ParentID: ptr.To("123456789")}}, private.Spec.Platform.GCP.ResourceTags) {
		t.Fatalf("unexpected private tags: %v", private.Spec.Platform.GCP.ResourceTags)
	}
	var public Cluster
	if err := Convert_Cluster_PrivateToPublic(&private, &public, nil); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cluster.Spec.Platform.GCP.ResourceTags, public.Spec.Platform.GCP.ResourceTags) {
		t.Fatalf("unexpected public tags: %v", public.Spec.Platform.GCP.ResourceTags)
	}
	privateCopy := private.DeepCopy()
	*privateCopy.Spec.Platform.GCP.ResourceTags[0].ParentID = "987654321"
	if *private.Spec.Platform.GCP.ResourceTags[0].ParentID != "123456789" {
		t.Fatal("private deep copy aliases parentID")
	}
	publicCopy := public.DeepCopy()
	*publicCopy.Spec.Platform.GCP.ResourceTags[0].ParentID = "987654321"
	if *public.Spec.Platform.GCP.ResourceTags[0].ParentID != "123456789" {
		t.Fatal("public deep copy aliases parentID")
	}
}
