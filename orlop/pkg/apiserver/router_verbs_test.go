package apiserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-logr/logr"

	"github.com/openshift-online/gecko/orlop/pkg/apiserver/types"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestNotImplementedHandler_Returns501(t *testing.T) {
	for _, verb := range []string{"create", "get", "list", "update", "patch", "delete", "watch", "quoted\"\\\n\x01"} {
		t.Run(verb, func(t *testing.T) {
			h := notImplementedHandler(verb, logr.Discard())
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			w := httptest.NewRecorder()
			h(w, req)
			if w.Code != http.StatusNotImplemented {
				t.Errorf("expected 501, got %d", w.Code)
			}
			if got := w.Header().Get("Content-Type"); got != "application/json" {
				t.Errorf("content type = %q", got)
			}
			var status metav1.Status
			if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil {
				t.Fatalf("invalid JSON Status response: %v: %s", err, w.Body.String())
			}
			if status.APIVersion != "v1" || status.Kind != "Status" || status.Status != metav1.StatusFailure || status.Reason != metav1.StatusReasonMethodNotAllowed || status.Code != http.StatusNotImplemented {
				t.Errorf("unexpected Status response: %#v", status)
			}
			if want := fmt.Sprintf("verb %q is not supported for this resource", verb); status.Message != want {
				t.Errorf("message = %q, want %q", status.Message, want)
			}
		})
	}
}

func TestRegisterVerbsForResource_VerbAllowedCheck(t *testing.T) {
	// Verify VerbAllowed logic is consistent for the routing decision.
	res := types.ResourceInfo{Verbs: []string{"list", "get"}}
	for _, tc := range []struct {
		verb    string
		allowed bool
	}{
		{"list", true},
		{"get", true},
		{"create", false},
		{"update", false},
		{"patch", false},
		{"delete", false},
		{"watch", false},
	} {
		got := res.VerbAllowed(tc.verb)
		if got != tc.allowed {
			t.Errorf("VerbAllowed(%q) = %v, want %v", tc.verb, got, tc.allowed)
		}
	}
}

func TestRegisterVerbsForResource_NoRestriction_AllVerbsAllowed(t *testing.T) {
	res := types.ResourceInfo{} // nil Verbs
	for _, verb := range []string{"create", "get", "list", "update", "patch", "delete", "watch"} {
		if !res.VerbAllowed(verb) {
			t.Errorf("VerbAllowed(%q) = false; want true when Verbs is nil", verb)
		}
	}
}

func TestRestrictWatch(t *testing.T) {
	for _, tc := range []struct {
		name  string
		verbs []string
		query string
		want  int
	}{
		{name: "read-only watch", verbs: []string{"get", "list"}, query: "?watch=true", want: http.StatusNotImplemented},
		{name: "read-only list", verbs: []string{"get", "list"}, want: http.StatusOK},
		{name: "explicit list", verbs: []string{"get", "list"}, query: "?watch=false", want: http.StatusOK},
		{name: "watch enabled", verbs: []string{"get", "list", "watch"}, query: "?watch=true", want: http.StatusOK},
		{name: "unrestricted watch", query: "?watch=true", want: http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			handler := restrictWatch(func(w http.ResponseWriter, _ *http.Request) { called = true; w.WriteHeader(http.StatusOK) }, types.ResourceInfo{Verbs: tc.verbs}, logr.Discard())
			response := httptest.NewRecorder()
			handler(response, httptest.NewRequest(http.MethodGet, "/catalog"+tc.query, nil))
			if response.Code != tc.want || called != (tc.want == http.StatusOK) {
				t.Fatalf("status=%d handler called=%t", response.Code, called)
			}
			if tc.want == http.StatusNotImplemented {
				var status metav1.Status
				if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil {
					t.Fatalf("watch guard returned invalid JSON: %v", err)
				}
				if status.Code != http.StatusNotImplemented || status.Reason != metav1.StatusReasonMethodNotAllowed {
					t.Fatalf("unexpected watch Status: %#v", status)
				}
			}
		})
	}
}
