package handlers

import (
	"errors"
	"testing"

	privatev1 "github.com/openshift-online/gecko/orlop/apis/private/test/v1"

	"k8s.io/apimachinery/pkg/runtime"
)

// TestApplyPatch_ErrorsCanBeUnwrapped verifies that errors returned by
// applyPatch wrap the underlying cause with %w, so callers can inspect
// the original error via errors.Is / errors.As.
func TestApplyPatch_ErrorsCanBeUnwrapped(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := privatev1.SchemeBuilder.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}

	h := &ResourceHandler{
		gvk:    gvkV1,
		scheme: scheme,
	}

	// Use invalid JSON as patch bytes so every branch hits a parse/apply error.
	invalidPatch := []byte(`not json`)
	validJSON := []byte(`{"apiVersion":"v1","kind":"Object","metadata":{"name":"x","namespace":"default"}}`)

	tests := []struct {
		name        string
		contentType string
	}{
		{"json-patch", "application/json-patch+json"},
		{"merge-patch", "application/merge-patch+json"},
		{"strategic-merge-patch", "application/strategic-merge-patch+json"},
		{"default-patch", "application/unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obj := &privatev1.Object{}
			obj.SetName("test")
			obj.SetNamespace("default")

			_, err := h.applyPatch(tt.contentType, obj, validJSON, invalidPatch)
			if err == nil {
				t.Fatal("expected error, got nil")
			}

			// The key assertion: Unwrap must return a non-nil error,
			// proving %w wrapping is in effect.
			var unwrapped interface{ Unwrap() error }
			if !errors.As(err, &unwrapped) {
				t.Fatalf("error does not implement Unwrap(): %v", err)
			}
			if unwrapped.Unwrap() == nil {
				t.Errorf("Unwrap() returned nil for error: %v", err)
			}
		})
	}
}
