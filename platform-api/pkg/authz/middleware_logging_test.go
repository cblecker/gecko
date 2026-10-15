package authz

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	"github.com/google/uuid"

	"github.com/openshift-online/gecko/orlop/pkg/apiserver/constants"
	"github.com/openshift-online/gecko/orlop/pkg/apiserver/handlers"
	"github.com/openshift-online/gecko/orlop/pkg/apiserver/types"
	"github.com/openshift-online/gecko/platform-api/pkg/authn"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	runtimeschema "k8s.io/apimachinery/pkg/runtime/schema"
)

func TestMiddlewareAddsRequestIDAndSanitizesInvalidPathLog(t *testing.T) {
	_, authorizer := newTestAuthorizer(t)
	entries := []capturedAuthorizationLog{}
	logger := logr.FromSlogHandler(&capturedAuthorizationLogHandler{entries: &entries})
	resources := []types.ResourceInfo{{
		GVK:        runtimeschema.GroupVersionKind{Group: "gcp.managed.openshift.io", Version: "v1", Kind: "Cluster"},
		Plural:     "clusters",
		Namespaced: true,
	}}
	ctx := authn.WithUser(context.Background(), "alice@example.com")
	request := httptest.NewRequestWithContext(ctx, http.MethodGet, "/invalid/customer-namespace/customer-cluster", nil)
	response := httptest.NewRecorder()

	Middleware(authorizer, logger, resources)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("invalid path reached the next handler")
	})).ServeHTTP(response, request)

	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusForbidden)
	}
	requestID := response.Header().Get(constants.HeaderRequestID)
	if _, err := uuid.Parse(requestID); err != nil {
		t.Fatalf("%s = %q, want UUID: %v", constants.HeaderRequestID, requestID, err)
	}
	if len(entries) != 1 {
		t.Fatalf("log entries = %d, want 1", len(entries))
	}
	entry := entries[0]
	if entry.message != "authorization denied" {
		t.Fatalf("log message = %q, want authorization denied", entry.message)
	}
	if entry.attributes["requestID"] != requestID {
		t.Fatalf("requestID log attribute = %q, want %q", entry.attributes["requestID"], requestID)
	}
	if entry.attributes["reason"] != authorizationReasonInvalidPath {
		t.Fatalf("reason log attribute = %q, want %q", entry.attributes["reason"], authorizationReasonInvalidPath)
	}
	if output := entry.String(); strings.Contains(output, "customer-namespace") || strings.Contains(output, "customer-cluster") {
		t.Fatalf("log leaked customer identifier: %s", output)
	}
}

func TestMiddlewareAggregatesListAuthorizationFailures(t *testing.T) {
	stores, authorizer := newTestAuthorizer(t)
	seedClusterListBinding(t, stores, "project-a", nil)
	if err := authorizer.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}

	entries := []capturedAuthorizationLog{}
	logger := logr.FromSlogHandler(&capturedAuthorizationLogHandler{entries: &entries})
	resources := []types.ResourceInfo{{
		GVK:        runtimeschema.GroupVersionKind{Group: "gcp.managed.openshift.io", Version: "v1", Kind: "Cluster"},
		Plural:     "clusters",
		Namespaced: true,
	}}
	ctx := authn.WithUser(context.Background(), "alice@example.com")
	request := httptest.NewRequestWithContext(ctx, http.MethodGet, "/apis/gcp.managed.openshift.io/v1/namespaces/project-a/clusters", nil)
	response := httptest.NewRecorder()

	Middleware(authorizer, logger, resources)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		filter, ok := handlers.ItemFilterFromContext(r.Context())
		if !ok {
			t.Fatal("item filter was not added to the request context")
		}
		item := &unstructured.Unstructured{Object: map[string]interface{}{
			"metadata": map[string]interface{}{"name": "customer-object"},
		}}
		for range 3 {
			allowed, err := filter(r.Context(), item)
			if err != nil || allowed {
				t.Fatalf("item filter result = %t, %v; want denied without error", allowed, err)
			}
		}
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(response, request)

	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNoContent)
	}
	requestID := response.Header().Get(constants.HeaderRequestID)
	if _, err := uuid.Parse(requestID); err != nil {
		t.Fatalf("%s = %q, want UUID: %v", constants.HeaderRequestID, requestID, err)
	}
	if len(entries) != 1 {
		t.Fatalf("log entries = %d, want 1 aggregated failure", len(entries))
	}
	entry := entries[0]
	if entry.attributes["requestID"] != requestID {
		t.Fatalf("requestID log attribute = %q, want %q", entry.attributes["requestID"], requestID)
	}
	if entry.attributes["reason"] != authorizationReasonObjectContextBuild {
		t.Fatalf("reason log attribute = %q, want %q", entry.attributes["reason"], authorizationReasonObjectContextBuild)
	}
	if entry.attributes["failureCount"] != "3" {
		t.Fatalf("failureCount log attribute = %q, want 3", entry.attributes["failureCount"])
	}
	if output := entry.String(); strings.Contains(output, "customer-object") {
		t.Fatalf("log leaked customer identifier: %s", output)
	}
}

type capturedAuthorizationLog struct {
	message    string
	attributes map[string]string
}

func (l capturedAuthorizationLog) String() string {
	return fmt.Sprintf("%s %v", l.message, l.attributes)
}

type capturedAuthorizationLogHandler struct {
	entries    *[]capturedAuthorizationLog
	attributes []slog.Attr
}

func (h *capturedAuthorizationLogHandler) Enabled(context.Context, slog.Level) bool {
	return true
}

func (h *capturedAuthorizationLogHandler) Handle(_ context.Context, record slog.Record) error {
	entry := capturedAuthorizationLog{
		message:    record.Message,
		attributes: make(map[string]string),
	}
	for _, attribute := range h.attributes {
		entry.attributes[attribute.Key] = fmt.Sprint(attribute.Value.Any())
	}
	record.Attrs(func(attribute slog.Attr) bool {
		entry.attributes[attribute.Key] = fmt.Sprint(attribute.Value.Any())
		return true
	})
	*h.entries = append(*h.entries, entry)
	return nil
}

func (h *capturedAuthorizationLogHandler) WithAttrs(attributes []slog.Attr) slog.Handler {
	clone := *h
	clone.attributes = append(append([]slog.Attr{}, h.attributes...), attributes...)
	return &clone
}

func (h *capturedAuthorizationLogHandler) WithGroup(string) slog.Handler {
	return h
}
