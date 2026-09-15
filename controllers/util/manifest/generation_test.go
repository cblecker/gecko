package manifest_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/openshift-online/gecko/controllers/util/constants"
	"github.com/openshift-online/gecko/controllers/util/manifest"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// ─── test helpers ─────────────────────────────────────────────────────────────

// metaWithGen builds ObjectMeta carrying only the generation annotation.
func metaWithGen(genStr string) metav1.ObjectMeta {
	return metav1.ObjectMeta{
		Annotations: map[string]string{constants.AnnotationGeneration: genStr},
	}
}

// objWithGen returns an Unstructured with the generation annotation set.
// apiVersion and kind are included so the object survives UnmarshalJSON validation.
func objWithGen(name, genStr string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
	}}
	obj.SetName(name)
	obj.SetNamespace("default")
	if genStr != "" {
		obj.SetAnnotations(map[string]string{constants.AnnotationGeneration: genStr})
	}
	return obj
}

// ─── CompareGenerations ────────────────────────────────────────────────────────

func TestCompareGenerations_Operations(t *testing.T) {
	tests := []struct {
		name            string
		newGen          int64
		existingGen     int64
		exists          bool
		wantOp          manifest.Operation
		wantNewGen      int64
		wantExistingGen int64
	}{
		{
			name:   "resource does not exist → create",
			newGen: 5, existingGen: 0, exists: false,
			wantOp: manifest.OperationCreate, wantNewGen: 5, wantExistingGen: 0,
		},
		{
			name:   "exists=false with non-zero existing arg → existing zeroed in decision",
			newGen: 3, existingGen: 7, exists: false,
			wantOp: manifest.OperationCreate, wantNewGen: 3, wantExistingGen: 0,
		},
		{
			name:   "generations match → skip",
			newGen: 4, existingGen: 4, exists: true,
			wantOp: manifest.OperationSkip, wantNewGen: 4, wantExistingGen: 4,
		},
		{
			name:   "both zero → skip (equal)",
			newGen: 0, existingGen: 0, exists: true,
			wantOp: manifest.OperationSkip, wantNewGen: 0, wantExistingGen: 0,
		},
		{
			name:   "new generation higher → update",
			newGen: 5, existingGen: 3, exists: true,
			wantOp: manifest.OperationUpdate, wantNewGen: 5, wantExistingGen: 3,
		},
		{
			name:   "new generation lower (rollback) → update",
			newGen: 2, existingGen: 5, exists: true,
			wantOp: manifest.OperationUpdate, wantNewGen: 2, wantExistingGen: 5,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := manifest.CompareGenerations(tc.newGen, tc.existingGen, tc.exists)
			assert.Equal(t, tc.wantOp, d.Operation)
			assert.Equal(t, tc.wantNewGen, d.NewGeneration)
			assert.Equal(t, tc.wantExistingGen, d.ExistingGeneration)
			assert.NotEmpty(t, d.Reason, "Reason must always be set")
		})
	}
}

func TestCompareGenerations_ReasonContent(t *testing.T) {
	t.Run("create reason mentions resource not found", func(t *testing.T) {
		d := manifest.CompareGenerations(1, 0, false)
		assert.Contains(t, d.Reason, "not found")
	})

	t.Run("skip reason includes the matching generation number", func(t *testing.T) {
		d := manifest.CompareGenerations(7, 7, true)
		assert.Contains(t, d.Reason, "7")
	})

	t.Run("update reason shows both old and new generations", func(t *testing.T) {
		d := manifest.CompareGenerations(10, 3, true)
		assert.Contains(t, d.Reason, "3")
		assert.Contains(t, d.Reason, "10")
	})
}

// ─── GetGeneration ─────────────────────────────────────────────────────────────

func TestGetGeneration(t *testing.T) {
	tests := []struct {
		name    string
		meta    metav1.ObjectMeta
		wantGen int64
	}{
		{"nil annotations → 0", metav1.ObjectMeta{Annotations: nil}, 0},
		{"annotation absent → 0", metav1.ObjectMeta{Annotations: map[string]string{"other": "val"}}, 0},
		{"empty annotation value → 0", metaWithGen(""), 0},
		{"non-numeric annotation → 0", metaWithGen("not-a-number"), 0},
		{"annotation overflow (out of int64 range) → 0", metaWithGen("99999999999999999999"), 0},
		{"zero value → 0", metaWithGen("0"), 0},
		{"valid positive → parsed", metaWithGen("42"), 42},
		{"generation 1 → 1", metaWithGen("1"), 1},
		{"negative value → parsed as-is (no validation here)", metaWithGen("-5"), -5},
		{"max int64 → parsed correctly", metaWithGen("9223372036854775807"), 9223372036854775807},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.wantGen, manifest.GetGeneration(tc.meta))
		})
	}
}

// ─── GetGenerationFromUnstructured ────────────────────────────────────────────

func TestGetGenerationFromUnstructured(t *testing.T) {
	t.Run("nil object → 0", func(t *testing.T) {
		assert.Equal(t, int64(0), manifest.GetGenerationFromUnstructured(nil))
	})

	t.Run("no annotations field → 0", func(t *testing.T) {
		obj := &unstructured.Unstructured{Object: map[string]interface{}{}}
		assert.Equal(t, int64(0), manifest.GetGenerationFromUnstructured(obj))
	})

	t.Run("annotation key absent → 0", func(t *testing.T) {
		obj := &unstructured.Unstructured{Object: map[string]interface{}{}}
		obj.SetAnnotations(map[string]string{"other": "val"})
		assert.Equal(t, int64(0), manifest.GetGenerationFromUnstructured(obj))
	})

	t.Run("empty annotation value → 0", func(t *testing.T) {
		assert.Equal(t, int64(0), manifest.GetGenerationFromUnstructured(objWithGen("x", "")))
	})

	t.Run("non-numeric annotation → 0", func(t *testing.T) {
		obj := objWithGen("x", "")
		obj.SetAnnotations(map[string]string{constants.AnnotationGeneration: "abc"})
		assert.Equal(t, int64(0), manifest.GetGenerationFromUnstructured(obj))
	})

	t.Run("valid generation → parsed", func(t *testing.T) {
		obj := &unstructured.Unstructured{Object: map[string]interface{}{}}
		obj.SetAnnotations(map[string]string{constants.AnnotationGeneration: "7"})
		assert.Equal(t, int64(7), manifest.GetGenerationFromUnstructured(obj))
	})
}

// ─── ValidateGeneration ────────────────────────────────────────────────────────

func TestValidateGeneration(t *testing.T) {
	t.Run("nil annotations → error mentioning annotation key", func(t *testing.T) {
		err := manifest.ValidateGeneration(metav1.ObjectMeta{Annotations: nil})
		require.Error(t, err)
		assert.Contains(t, err.Error(), constants.AnnotationGeneration)
	})

	t.Run("annotation key absent → error", func(t *testing.T) {
		err := manifest.ValidateGeneration(metav1.ObjectMeta{
			Annotations: map[string]string{"other": "val"},
		})
		require.Error(t, err)
	})

	t.Run("empty annotation value → error mentioning 'empty'", func(t *testing.T) {
		err := manifest.ValidateGeneration(metaWithGen(""))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "empty")
	})

	t.Run("non-numeric value → error mentioning 'invalid'", func(t *testing.T) {
		err := manifest.ValidateGeneration(metaWithGen("not-a-number"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid")
	})

	t.Run("zero → error (generation must be > 0)", func(t *testing.T) {
		err := manifest.ValidateGeneration(metaWithGen("0"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "> 0")
	})

	t.Run("negative → error", func(t *testing.T) {
		require.Error(t, manifest.ValidateGeneration(metaWithGen("-1")))
	})

	t.Run("valid positive generation → no error", func(t *testing.T) {
		require.NoError(t, manifest.ValidateGeneration(metaWithGen("1")))
	})

	t.Run("large valid generation → no error", func(t *testing.T) {
		require.NoError(t, manifest.ValidateGeneration(metaWithGen("9999")))
	})
}

// ─── ValidateGenerationFromUnstructured ───────────────────────────────────────

func TestValidateGenerationFromUnstructured(t *testing.T) {
	t.Run("nil object → error mentioning 'nil'", func(t *testing.T) {
		err := manifest.ValidateGenerationFromUnstructured(nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "nil")
	})

	t.Run("nil annotations → error", func(t *testing.T) {
		obj := &unstructured.Unstructured{Object: map[string]interface{}{}}
		err := manifest.ValidateGenerationFromUnstructured(obj)
		require.Error(t, err)
		assert.Contains(t, err.Error(), constants.AnnotationGeneration)
	})

	t.Run("annotation key absent → error", func(t *testing.T) {
		obj := &unstructured.Unstructured{Object: map[string]interface{}{}}
		obj.SetAnnotations(map[string]string{"other": "val"})
		require.Error(t, manifest.ValidateGenerationFromUnstructured(obj))
	})

	t.Run("empty annotation value → error", func(t *testing.T) {
		obj := &unstructured.Unstructured{Object: map[string]interface{}{}}
		obj.SetAnnotations(map[string]string{constants.AnnotationGeneration: ""})
		require.Error(t, manifest.ValidateGenerationFromUnstructured(obj))
	})

	t.Run("non-numeric value → error", func(t *testing.T) {
		obj := &unstructured.Unstructured{Object: map[string]interface{}{}}
		obj.SetAnnotations(map[string]string{constants.AnnotationGeneration: "bad"})
		require.Error(t, manifest.ValidateGenerationFromUnstructured(obj))
	})

	t.Run("zero → error", func(t *testing.T) {
		obj := &unstructured.Unstructured{Object: map[string]interface{}{}}
		obj.SetAnnotations(map[string]string{constants.AnnotationGeneration: "0"})
		require.Error(t, manifest.ValidateGenerationFromUnstructured(obj))
	})

	t.Run("negative → error", func(t *testing.T) {
		obj := &unstructured.Unstructured{Object: map[string]interface{}{}}
		obj.SetAnnotations(map[string]string{constants.AnnotationGeneration: "-3"})
		require.Error(t, manifest.ValidateGenerationFromUnstructured(obj))
	})

	t.Run("valid positive generation → no error", func(t *testing.T) {
		obj := &unstructured.Unstructured{Object: map[string]interface{}{}}
		obj.SetAnnotations(map[string]string{constants.AnnotationGeneration: "5"})
		require.NoError(t, manifest.ValidateGenerationFromUnstructured(obj))
	})
}

// ─── GetLatestGenerationFromList ──────────────────────────────────────────────

func TestGetLatestGenerationFromList(t *testing.T) {
	t.Run("nil list → nil", func(t *testing.T) {
		assert.Nil(t, manifest.GetLatestGenerationFromList(nil))
	})

	t.Run("empty list → nil", func(t *testing.T) {
		assert.Nil(t, manifest.GetLatestGenerationFromList(&unstructured.UnstructuredList{}))
	})

	t.Run("single item → returned regardless of generation", func(t *testing.T) {
		list := &unstructured.UnstructuredList{Items: []unstructured.Unstructured{*objWithGen("only", "3")}}
		got := manifest.GetLatestGenerationFromList(list)
		require.NotNil(t, got)
		assert.Equal(t, "only", got.GetName())
	})

	t.Run("picks item with highest generation annotation", func(t *testing.T) {
		list := &unstructured.UnstructuredList{
			Items: []unstructured.Unstructured{
				*objWithGen("a", "1"),
				*objWithGen("b", "5"),
				*objWithGen("c", "3"),
			},
		}
		got := manifest.GetLatestGenerationFromList(list)
		require.NotNil(t, got)
		assert.Equal(t, "b", got.GetName())
	})

	t.Run("tie in generation → lexicographically first name wins (deterministic)", func(t *testing.T) {
		list := &unstructured.UnstructuredList{
			Items: []unstructured.Unstructured{
				*objWithGen("zebra", "5"),
				*objWithGen("alpha", "5"),
				*objWithGen("mango", "5"),
			},
		}
		got := manifest.GetLatestGenerationFromList(list)
		require.NotNil(t, got)
		assert.Equal(t, "alpha", got.GetName())
	})

	t.Run("items without generation annotation are treated as 0", func(t *testing.T) {
		noGen := &unstructured.Unstructured{Object: map[string]interface{}{}}
		noGen.SetName("no-gen")
		list := &unstructured.UnstructuredList{
			Items: []unstructured.Unstructured{
				*noGen,
				*objWithGen("with-gen", "2"),
			},
		}
		got := manifest.GetLatestGenerationFromList(list)
		require.NotNil(t, got)
		assert.Equal(t, "with-gen", got.GetName())
	})

	t.Run("does not reorder the original list", func(t *testing.T) {
		list := &unstructured.UnstructuredList{
			Items: []unstructured.Unstructured{
				*objWithGen("first", "1"),
				*objWithGen("second", "10"),
			},
		}
		_ = manifest.GetLatestGenerationFromList(list)
		assert.Equal(t, "first", list.Items[0].GetName(), "original list must not be mutated")
		assert.Equal(t, "second", list.Items[1].GetName())
	})
}

// ─── BuildLabelSelector ───────────────────────────────────────────────────────

func TestBuildLabelSelector(t *testing.T) {
	t.Run("nil map → empty string", func(t *testing.T) {
		assert.Equal(t, "", manifest.BuildLabelSelector(nil))
	})

	t.Run("empty map → empty string", func(t *testing.T) {
		assert.Equal(t, "", manifest.BuildLabelSelector(map[string]string{}))
	})

	t.Run("single label → key=value", func(t *testing.T) {
		assert.Equal(t, "env=prod", manifest.BuildLabelSelector(map[string]string{"env": "prod"}))
	})

	t.Run("multiple labels → sorted alphabetically by key", func(t *testing.T) {
		labels := map[string]string{"env": "prod", "app": "myapp", "team": "platform"}
		assert.Equal(t, "app=myapp,env=prod,team=platform", manifest.BuildLabelSelector(labels))
	})

	t.Run("output is deterministic across repeated calls", func(t *testing.T) {
		labels := map[string]string{"z": "last", "a": "first", "m": "middle"}
		assert.Equal(t, manifest.BuildLabelSelector(labels), manifest.BuildLabelSelector(labels))
	})
}

// ─── DiscoveryConfig ──────────────────────────────────────────────────────────

func TestDiscoveryConfig(t *testing.T) {
	t.Run("GetNamespace returns Namespace field", func(t *testing.T) {
		d := &manifest.DiscoveryConfig{Namespace: "kube-system"}
		assert.Equal(t, "kube-system", d.GetNamespace())
	})

	t.Run("GetName returns ByName field", func(t *testing.T) {
		d := &manifest.DiscoveryConfig{ByName: "my-resource"}
		assert.Equal(t, "my-resource", d.GetName())
	})

	t.Run("GetLabelSelector returns LabelSelector field", func(t *testing.T) {
		d := &manifest.DiscoveryConfig{LabelSelector: "app=foo"}
		assert.Equal(t, "app=foo", d.GetLabelSelector())
	})

	t.Run("IsSingleResource is true when ByName is set", func(t *testing.T) {
		assert.True(t, (&manifest.DiscoveryConfig{ByName: "something"}).IsSingleResource())
	})

	t.Run("IsSingleResource is false when ByName is empty", func(t *testing.T) {
		assert.False(t, (&manifest.DiscoveryConfig{}).IsSingleResource())
	})
}
