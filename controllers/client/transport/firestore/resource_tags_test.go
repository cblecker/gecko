package firestore

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestPreserveResourceTags(t *testing.T) {
	for _, tc := range []struct {
		name, previous, desired string
		mismatch                bool
	}{
		{"unchanged", `[{"key":"env","value":"prod"}]`, `[{"key":"env","value":"prod"}]`, false},
		{"changed", `[{"key":"env","value":"prod"}]`, `[{"key":"env","value":"dev"}]`, true},
		{"removed", `[{"key":"env","value":"prod"}]`, ``, true},
		{"added", ``, `[{"key":"env","value":"prod"}]`, true},
		{"absent", ``, ``, false},
		{"parent changed", `[{"key":"env","value":"prod","parentID":"123"}]`, `[{"key":"env","value":"prod","parentID":"456"}]`, true},
		{"order changed", `[{"key":"a","value":"v"},{"key":"b","value":"v"}]`, `[{"key":"b","value":"v"},{"key":"a","value":"v"}]`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manifest := func(tags, release string) map[string]any {
				obj := map[string]any{"spec": map[string]any{"release": map[string]any{"image": release}, "platform": map[string]any{"gcp": map[string]any{}}}}
				if tags != "" {
					var value any
					require.NoError(t, json.Unmarshal([]byte(tags), &value))
					require.NoError(t, unstructured.SetNestedField(obj, value, "spec", "platform", "gcp", "resourceTags"))
				}
				return obj
			}
			previous := manifest(tc.previous, "old")
			desired := manifest(tc.desired, "new")
			mismatch, err := preserveResourceTags(desired, previous)
			require.NoError(t, err)
			require.Equal(t, tc.mismatch, mismatch)
			require.Equal(t, manifest(tc.previous, "new"), desired, "only tags are preserved")
			require.Equal(t, manifest(tc.previous, "old"), previous, "stored input is not mutated")
			mismatch, err = preserveResourceTags(desired, previous)
			require.NoError(t, err)
			require.False(t, mismatch, "repeated preservation is idempotent")
		})
	}
}

func TestPreserveResourceTagsMalformedManifest(t *testing.T) {
	_, err := preserveResourceTags(map[string]any{}, map[string]any{"spec": "invalid"})
	require.Error(t, err)
}
