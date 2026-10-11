package handlers

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/go-logr/logr"

	"github.com/openshift-online/gecko/orlop/pkg/apiserver/constants"
	pkgschema "github.com/openshift-online/gecko/orlop/pkg/apiserver/schema"
	"github.com/openshift-online/gecko/orlop/pkg/apiserver/types"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// writeError writes an error response with a Status object.
func writeError(w http.ResponseWriter, code int, message string) {
	status := metav1.Status{
		TypeMeta: metav1.TypeMeta{
			APIVersion: constants.APIVersionV1,
			Kind:       constants.KindStatus,
		},
		Status:  metav1.StatusFailure,
		Message: message,
		Code:    int32(code),
	}

	w.Header().Set(constants.HeaderContentType, constants.ContentTypeJSON)
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(status)
}

// specChanged checks if the spec field has changed between two objects.
// All gecko resource types must serialize their spec under the JSON key "spec".
//
// Comparison is done on the unstructured form of each object rather than raw
// marshaled bytes: old and new may be different Go representations of the
// same content (e.g. a typed struct vs. an *unstructured.Unstructured loaded
// from storage), which marshal to JSON with different key ordering even when
// semantically identical. runtime.DefaultUnstructuredConverter normalizes
// both sides to map[string]interface{} before the comparison, and
// apiequality.Semantic.DeepEqual is the standard Kubernetes idiom for
// semantic (not byte-level) equality.
//
// Numeric invariant: extractSpec reduces both operands to
// map[string]interface{} of JSON primitives, so apiequality.Semantic's
// type-specific equality funcs (Quantity, Time, ...) never fire here —
// Semantic.DeepEqual effectively behaves as reflect.DeepEqual on the
// unstructured tree. Correctness therefore depends on both sides decoding
// numbers as the same Go type. This holds today: typed objects convert to
// int64 via ToUnstructured, and the postgres/spanner stores' unstructured
// Get also yields int64. If a future Get path decodes numbers as float64,
// numeric spec fields would churn — normalize numbers here if that changes.
func specChanged(old, new runtime.Object) bool {
	oldSpec, err := extractSpec(old)
	if err != nil {
		return true // fail safe: treat unreadable state as changed
	}
	newSpec, err := extractSpec(new)
	if err != nil {
		return true
	}
	return !apiequality.Semantic.DeepEqual(oldSpec, newSpec)
}

// extractSpec converts obj to its unstructured map representation and
// returns the value of its "spec" field.
func extractSpec(obj runtime.Object) (interface{}, error) {
	u, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		return nil, err
	}
	return u["spec"], nil
}

// existingForCompare returns a copy of the stored object normalized the same way
// as an incoming update — schema prune/default processing (when a processor is
// given) plus custom defaulting — so that identical content compares equal in
// specChanged and does not churn metadata.generation. The returned value is
// comparison-only; the input object is never modified.
//
// Fail-safe: on a schema-processing error it falls back to the raw object and
// skips custom defaulting (which must not mutate the shared input). That can
// cause a one-time spurious generation bump on the error path but never persists
// bad data.
//
// Callers on the converting (public<->private) path pass a nil processor: there
// the public-schema prune/default happens at the public layer, so mirroring it
// onto the private stored object would require a public<->private round-trip.
// That asymmetry converges after one write and is deliberately left as a
// follow-up.
func existingForCompare(ctx context.Context, processor *pkgschema.Processor, existing runtime.Object, logger logr.Logger) runtime.Object {
	copyObj := existing.DeepCopyObject()
	if copyObj == nil {
		return existing
	}
	if processor != nil {
		if err := applyProcessingForComparison(ctx, processor, copyObj); err != nil {
			logger.Error(err, "failed to apply schema processing to existing object for comparison")
			return existing
		}
	}
	if d, ok := copyObj.(types.CustomDefaulter); ok {
		if err := d.Default(ctx); err != nil {
			logger.Error(err, "custom defaulter failed on existing object for comparison")
		}
	}
	return copyObj
}

// applyProcessingForComparison runs the same prune/default schema processing
// used on the incoming update object against a copy of obj, in place. Callers
// use this to build a like-for-like "old" value before calling specChanged:
// without it, schema defaulting/pruning applied only to the incoming object
// can make an otherwise-unchanged apply look like a permanent spec change.
func applyProcessingForComparison(ctx context.Context, processor *pkgschema.Processor, obj runtime.Object) error {
	data, err := json.Marshal(obj)
	if err != nil {
		return err
	}
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	// Process mutates m in place (pruning unknown fields, applying defaults).
	// Validation errors are irrelevant here: this copy exists only to build a
	// comparable "old" value, and validation of the real request already
	// happened on the incoming object.
	processor.Process(ctx, m, nil)
	processedData, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return json.Unmarshal(processedData, obj)
}
