package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

const testTimeout = 5 * time.Second

func testActivation() Activation {
	return Activation{
		Signer:        "user@example.com",
		Project:       "customer-proj-1",
		Status:        "activated",
		LeaderProject: "leader-proj-1",
	}
}

func newTestClient(srv *httptest.Server) *Client {
	return &Client{
		ProjectID:   "host-proj-1",
		Region:      "host-region1",
		Integration: "signup-notify",
		HTTP:        srv.Client(),
		BaseURL:     srv.URL,
		Timeout:     testTimeout,
	}
}

func TestNotifyPostsExecuteRequest(t *testing.T) {
	var gotPath string
	var gotBody executeRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte(`{"executionId":"test"}`)); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	defer srv.Close()

	if err := newTestClient(srv).Notify(context.Background(), testActivation()); err != nil {
		t.Fatalf("Notify: %v", err)
	}

	// The integration is addressed by the client's own project and region, not
	// by anything in the activation.
	wantPath := "/v1/projects/host-proj-1/locations/host-region1/integrations/signup-notify:execute"
	if gotPath != wantPath {
		t.Errorf("path = %q, want %q", gotPath, wantPath)
	}
	if gotBody.TriggerID != triggerID {
		t.Errorf("triggerId = %q, want %q", gotBody.TriggerID, triggerID)
	}
}

// The integration definition declares exactly these inputs. Sending more would
// be ignored or rejected; sending fewer would blank part of the email.
func TestNotifySendsExactlyTheContractInputs(t *testing.T) {
	var gotBody executeRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode request: %v", err)
		}
	}))
	defer srv.Close()

	if err := newTestClient(srv).Notify(context.Background(), testActivation()); err != nil {
		t.Fatalf("Notify: %v", err)
	}

	want := map[string]string{
		"Signer":        "user@example.com",
		"Project":       "customer-proj-1",
		"Status":        "activated",
		"LeaderProject": "leader-proj-1",
	}
	if len(gotBody.InputParameters) != len(want) {
		t.Errorf("inputParameters has %d entries, want exactly %d: %+v", len(gotBody.InputParameters), len(want), gotBody.InputParameters)
	}
	for key, value := range want {
		if got := gotBody.InputParameters[key].StringValue; got != value {
			t.Errorf("inputParameters[%q] = %q, want %q", key, got, value)
		}
	}
}

func TestNotifyEscapesPathElements(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
	}))
	defer srv.Close()

	c := newTestClient(srv)
	c.Integration = "a/b?c"
	if err := c.Notify(context.Background(), testActivation()); err != nil {
		t.Fatalf("Notify: %v", err)
	}

	wantPath := "/v1/projects/host-proj-1/locations/host-region1/integrations/a%2Fb%3Fc:execute"
	if gotPath != wantPath {
		t.Errorf("path = %q, want %q", gotPath, wantPath)
	}
}

func TestNotifySurfacesHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"code":403,"status":"PERMISSION_DENIED","message":"denied for alice@redhat.com"}}`, http.StatusForbidden)
	}))
	defer srv.Close()

	err := newTestClient(srv).Notify(context.Background(), testActivation())
	if err == nil {
		t.Fatal("Notify: expected error for HTTP 403, got nil")
	}
	// The handler logs this error: it carries the status codes, never the body.
	if got, want := err.Error(), "execute integration: HTTP 403 PERMISSION_DENIED"; got != want {
		t.Errorf("error = %q, want %q", got, want)
	}
}
