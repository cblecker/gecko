package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/evanphx/json-patch/v5"
	"github.com/go-chi/chi/v5"

	"github.com/openshift-online/gecko/orlop/pkg/apiserver/constants"
	"github.com/openshift-online/gecko/orlop/pkg/apiserver/types"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/util/strategicpatch"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Patch handles PATCH requests with support for multiple patch types:
// - JSON Patch (RFC 6902)
// - JSON Merge Patch (RFC 7386)
// - Strategic Merge Patch (Kubernetes)
// - Server-Side Apply
func (h *ResourceHandler) Patch(w http.ResponseWriter, r *http.Request) {
	namespace := chi.URLParam(r, constants.URLParamNamespace)
	name := chi.URLParam(r, constants.URLParamName)
	contentType := r.Header.Get(constants.HeaderContentType)
	h.logger.V(1).Info("Patch request", "kind", h.gvk.Kind, "namespace", namespace, "name", name, "contentType", contentType)

	// Check if this is a server-side apply request
	if strings.HasPrefix(contentType, constants.ContentTypeApplyPatchPrefix) {
		h.ApplyPatch(w, r)
		return
	}

	// Get existing object
	existing, err := h.store.Get(r.Context(), namespace, name)
	if err != nil {
		if errors.IsNotFound(err) {
			writeError(w, http.StatusNotFound, err.Error())
		} else {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to get object: %v", err))
		}
		return
	}

	if !validateParentOwnership(r.Context(), existing) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}

	// Convert to serving version so patches operate in the client's schema
	existing, err = h.convertToServingVersion(existing)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to convert object version: %v", err))
		return
	}

	// Read patch body
	patchBytes, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("failed to read patch: %v", err))
		return
	}

	// Convert existing object to JSON
	existingJSON, err := json.Marshal(existing)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to marshal existing object: %v", err))
		return
	}

	// Apply patch based on Content-Type
	patchedJSON, err := h.applyPatch(contentType, existing, existingJSON, patchBytes)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Process and update the object
	h.processPatchedObject(w, r, namespace, name, patchedJSON, existing)
}

// applyPatch routes to the appropriate patch implementation based on Content-Type
func (h *ResourceHandler) applyPatch(contentType string, existing client.Object, existingJSON, patchBytes []byte) ([]byte, error) {
	var patchedJSON []byte
	var err error

	switch contentType {
	case constants.ContentTypeJSONPatch:
		// JSON Patch (RFC 6902)
		patchedJSON, err = h.jsonPatch(existingJSON, patchBytes)
		if err != nil {
			return nil, fmt.Errorf("json patch failed: %w", err)
		}
	case constants.ContentTypeMergePatch:
		// JSON Merge Patch (RFC 7386)
		patchedJSON, err = jsonMergePatch(existingJSON, patchBytes)
		if err != nil {
			return nil, fmt.Errorf("merge patch failed: %w", err)
		}
	case constants.ContentTypeStrategicMergePatch:
		// Strategic Merge Patch (Kubernetes default)
		patchedJSON, err = h.strategicMergePatch(existing, patchBytes)
		if err != nil {
			return nil, fmt.Errorf("strategic merge patch failed: %w", err)
		}
	default:
		// Default to merge patch
		patchedJSON, err = jsonMergePatch(existingJSON, patchBytes)
		if err != nil {
			return nil, fmt.Errorf("patch failed: %w", err)
		}
	}

	return patchedJSON, nil
}

// processPatchedObject handles the common logic after a patch is applied
func (h *ResourceHandler) processPatchedObject(w http.ResponseWriter, r *http.Request, namespace, name string, patchedJSON []byte, existing client.Object) {
	// Convert to map for schema processing
	var objMap map[string]interface{}
	if err := json.Unmarshal(patchedJSON, &objMap); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to unmarshal patched object: %v", err))
		return
	}

	// Process object (prune, default, validate)
	if errs := h.processor.Process(r.Context(), objMap); len(errs) > 0 {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("validation failed: %v", errs.ToAggregate()))
		return
	}

	// Convert back to typed object
	objJSON, _ := json.Marshal(objMap)
	obj, err := h.scheme.New(h.gvk)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to create object: %v", err))
		return
	}
	if err := json.Unmarshal(objJSON, obj); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to unmarshal object: %v", err))
		return
	}
	clientObj := obj.(client.Object)

	if d, ok := obj.(types.CustomDefaulter); ok {
		if err := d.Default(r.Context()); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("defaulting failed: %v", err))
			return
		}
	}

	if v, ok := obj.(types.CustomValidator); ok {
		if err := v.ValidateUpdate(r.Context(), existing); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("validation failed: %v", err))
			return
		}
	}

	// Ensure namespace and name are preserved
	clientObj.SetNamespace(namespace)
	clientObj.SetName(name)

	if err := validateMetadata(clientObj); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid metadata: %v", err))
		return
	}

	// Preserve deletionTimestamp — it cannot be changed via Patch.
	// Only Delete sets it, only finalizer removal clears it (via hard-delete).
	// Without this, a merge patch omitting metadata.deletionTimestamp would
	// leave the field as zero-value (nil), clearing the soft-delete marker.
	existingAccessor, err := meta.Accessor(existing)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to access existing metadata: %v", err))
		return
	}
	patchedAccessor, err := meta.Accessor(clientObj)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to access patched metadata: %v", err))
		return
	}
	if dt := existingAccessor.GetDeletionTimestamp(); dt != nil {
		patchedAccessor.SetDeletionTimestamp(dt)

		// If object is being deleted and all finalizers are removed, perform hard delete
		if len(patchedAccessor.GetFinalizers()) == 0 {
			// Run delete validation before removing
			if v, ok := clientObj.(types.CustomValidator); ok {
				if err := v.ValidateDelete(r.Context()); err != nil {
					writeError(w, http.StatusBadRequest, fmt.Sprintf("delete validation failed: %v", err))
					return
				}
			}

			// Re-read the object to check for concurrent modifications.
			// Another request may have added a finalizer between our initial
			// read and now; deleting without this check would lose that finalizer.
			current, err := h.store.Get(r.Context(), namespace, name)
			if err != nil {
				if errors.IsNotFound(err) {
					writeError(w, http.StatusNotFound, err.Error())
				} else {
					writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to re-read object: %v", err))
				}
				return
			}
			currentAccessor, err := meta.Accessor(current)
			if err != nil {
				writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to access current metadata: %v", err))
				return
			}
			if currentAccessor.GetResourceVersion() != existingAccessor.GetResourceVersion() {
				writeError(w, http.StatusConflict, "object was modified concurrently, retry the patch")
				return
			}

			if err := h.store.Delete(r.Context(), namespace, name); err != nil {
				h.logger.Error(err, "Failed to hard delete after finalizers removed",
					"kind", h.gvk.Kind, "namespace", namespace, "name", name)
				if errors.IsNotFound(err) {
					writeError(w, http.StatusNotFound, err.Error())
				} else {
					writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to delete object: %v", err))
				}
				return
			}

			h.logger.Info("Hard deleted after finalizers removed",
				"kind", h.gvk.Kind, "namespace", namespace, "name", name)

			// Return the deleted object (consistent with Update hard-delete path
			// and Kubernetes API conventions for PATCH).
			w.Header().Set(constants.HeaderContentType, constants.ContentTypeJSON)
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(clientObj)
			return
		}
	}

	// Set GVK
	clientObj.GetObjectKind().SetGroupVersionKind(h.gvk)

	// Convert to storage version before storing
	storeObj, err := h.convertToStorageVersion(clientObj)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to convert object version: %v", err))
		return
	}

	// Update object
	if err := h.store.Update(r.Context(), storeObj); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to update object: %v", err))
		return
	}

	// Convert back to serving version for response
	responseObj, err := h.convertToServingVersion(storeObj)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to convert response version: %v", err))
		return
	}

	h.logger.Info("Patched", "kind", h.gvk.Kind, "namespace", namespace, "name", name)

	// Return updated object
	w.Header().Set(constants.HeaderContentType, constants.ContentTypeJSON)
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(responseObj)
}

// jsonPatch applies a JSON Patch (RFC 6902) to the original JSON.
// JSON Patch is a sequence of operations: add, remove, replace, move, copy, test.
func (h *ResourceHandler) jsonPatch(originalJSON, patchBytes []byte) ([]byte, error) {
	// Parse the JSON Patch
	patch, err := jsonpatch.DecodePatch(patchBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to decode JSON patch: %w", err)
	}

	// Apply the patch
	patchedJSON, err := patch.Apply(originalJSON)
	if err != nil {
		return nil, fmt.Errorf("failed to apply JSON patch: %w", err)
	}

	return patchedJSON, nil
}

// strategicMergePatch applies a strategic merge patch using Kubernetes semantics.
// Strategic merge patch understands list merge strategies and struct tags.
func (h *ResourceHandler) strategicMergePatch(original client.Object, patchBytes []byte) ([]byte, error) {
	// Convert original object to JSON
	originalJSON, err := json.Marshal(original)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal original: %w", err)
	}

	// Create a new object of the same type for the patch result
	// This is needed because strategicpatch requires a typed object
	patchedObj, err := h.scheme.New(h.gvk)
	if err != nil {
		return nil, fmt.Errorf("failed to create object for patch: %w", err)
	}

	// Apply strategic merge patch
	// The strategicpatch package uses struct tags to determine merge strategies
	patchedJSON, err := strategicpatch.StrategicMergePatch(originalJSON, patchBytes, patchedObj)
	if err != nil {
		return nil, fmt.Errorf("failed to apply strategic merge patch: %w", err)
	}

	return patchedJSON, nil
}

// jsonMergePatch applies a JSON merge patch (RFC 7386) to original JSON.
func jsonMergePatch(original, patch []byte) ([]byte, error) {
	var originalMap, patchMap map[string]interface{}

	if err := json.Unmarshal(original, &originalMap); err != nil {
		return nil, fmt.Errorf("failed to unmarshal original: %w", err)
	}

	if err := json.Unmarshal(patch, &patchMap); err != nil {
		return nil, fmt.Errorf("failed to unmarshal patch: %w", err)
	}

	// Apply merge patch
	merged := mergeMaps(originalMap, patchMap)

	return json.Marshal(merged)
}

// mergeMaps recursively merges patch into original following RFC 7386 rules.
func mergeMaps(original, patch map[string]interface{}) map[string]interface{} {
	result := make(map[string]interface{})

	// Copy all from original
	for k, v := range original {
		result[k] = v
	}

	// Apply patch
	for k, patchValue := range patch {
		if patchValue == nil {
			// nil in patch means delete the key
			delete(result, k)
			continue
		}

		originalValue, exists := original[k]
		if !exists {
			// Key doesn't exist in original, add it
			result[k] = patchValue
			continue
		}

		// Both exist - check if both are maps
		originalMap, originalIsMap := originalValue.(map[string]interface{})
		patchMap, patchIsMap := patchValue.(map[string]interface{})

		if originalIsMap && patchIsMap {
			// Both are maps, recurse
			result[k] = mergeMaps(originalMap, patchMap)
		} else {
			// Replace with patch value
			result[k] = patchValue
		}
	}

	return result
}
