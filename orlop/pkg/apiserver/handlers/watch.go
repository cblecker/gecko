package handlers

import (
	"context"
	"fmt"
	"net/http"

	"github.com/openshift-online/gecko/orlop/pkg/apiserver/storage"

	"k8s.io/apimachinery/pkg/api/meta"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// handleWatch implements the Kubernetes watch protocol
func (h *ResourceHandler) handleWatch(w http.ResponseWriter, r *http.Request, opts storage.ListOptions) {
	config := parseWatchConfig(r)

	h.logger.V(1).Info("Watch parameters",
		config.allowWatchBookmarks, config.sendInitialEvents, config.resourceVersionMatch, config.timeoutSeconds)

	// Apply timeout if specified
	ctx, timeoutCancel := applyWatchTimeout(r.Context(), config.timeoutSeconds)
	defer timeoutCancel()

	// Start watch
	eventCh, stop, err := h.store.Watch(ctx, opts, config.resourceVersion)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("failed to start watch: %v", err))
		return
	}
	defer stop()

	// Get current resource version
	currentRV, isEmpty := h.getCurrentResourceVersion(ctx, opts)

	// Set up streaming response
	streamer, err := newWatchStreamer(w, h.gvk, currentRV, isEmpty)
	if err != nil {
		return
	}

	// Convert objects to serving version when storage version differs
	var transformer objectTransformer
	if h.needsConversion() {
		transformer = func(obj client.Object) (interface{}, error) {
			return h.convertToServingVersion(obj)
		}
	}

	streamWatch(ctx, streamer, eventCh, config, opts, h.store, transformer)
}

// getCurrentResourceVersion retrieves the current resource version from the store
func (h *ResourceHandler) getCurrentResourceVersion(ctx context.Context, opts storage.ListOptions) (string, bool) {
	list, err := h.store.List(ctx, opts)
	if err != nil {
		return "0", true
	}

	items, _ := meta.ExtractList(list)
	return list.GetResourceVersion(), len(items) == 0
}
