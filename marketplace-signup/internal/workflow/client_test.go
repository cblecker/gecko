package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// writeBody writes a fake API response, reporting a failed write at its source
// instead of leaving it to surface as a later, unrelated failure.
func writeBody(t *testing.T, w http.ResponseWriter, body []byte) {
	t.Helper()
	if _, err := w.Write(body); err != nil {
		t.Errorf("write response: %v", err)
	}
}

// marshal encodes a fixture. It must be called from the test's goroutine, not
// from an HTTP handler.
func marshal(t *testing.T, v any) []byte {
	t.Helper()
	encoded, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %T: %v", v, err)
	}
	return encoded
}

// fakeWorkflows returns a client backed by a local server that answers the
// create call and then hands out the given execution states in order.
func fakeWorkflows(t *testing.T, states []map[string]any) *Client {
	t.Helper()

	var gets int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/projects/p/locations/r/workflows/w/executions":
			var req struct {
				Argument string `json:"argument"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Errorf("decode create body: %v", err)
			}
			var args map[string]string
			if err := json.Unmarshal([]byte(req.Argument), &args); err != nil {
				t.Errorf("argument is not JSON: %q", req.Argument)
			}
			if args["project_namespace"] != "my-project-123" || args["subject"] != "alice@redhat.com" {
				t.Errorf("unexpected argument: %v", args)
			}
			writeBody(t, w, []byte(`{"name":"projects/p/locations/r/executions/e1","state":"ACTIVE"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/projects/p/locations/r/executions/e1":
			idx := gets
			if idx >= len(states) {
				idx = len(states) - 1
			}
			gets++
			body, err := json.Marshal(states[idx])
			if err != nil {
				t.Errorf("marshal execution state: %v", err)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			writeBody(t, w, body)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	return &Client{
		ProjectID:    "p",
		Region:       "r",
		WorkflowName: "w",
		HTTP:         srv.Client(),
		BaseURL:      srv.URL,
		Timeout:      5 * time.Second,
		PollInterval: time.Millisecond,
	}
}

func TestActivateSuccess(t *testing.T) {
	c := fakeWorkflows(t, []map[string]any{
		{"name": "e1", "state": "ACTIVE"},
		{"name": "e1", "state": "SUCCEEDED", "result": `{"status":"activated","namespace":"my-project-123"}`},
	})

	res, err := c.Activate(context.Background(), "my-project-123", "alice@redhat.com")
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if res.Status != "activated" || res.Namespace != "my-project-123" {
		t.Fatalf("unexpected result: %+v", res)
	}
}

func TestActivateAlreadyActive(t *testing.T) {
	c := fakeWorkflows(t, []map[string]any{
		{"name": "e1", "state": "SUCCEEDED", "result": `{"status":"already_active","namespace":"my-project-123"}`},
	})
	res, err := c.Activate(context.Background(), "my-project-123", "alice@redhat.com")
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if res.Status != "already_active" {
		t.Fatalf("status = %q", res.Status)
	}
}

func TestActivateFailureMapping(t *testing.T) {
	// The workflow's messages and the stack trace in an execution's context are
	// free text from upstream. They classify the failure but never reach the
	// error, which the handler logs; the error names the execution instead.
	const trace = `RuntimeError in step "create_binding", routine "main", line: 115, subject alice@redhat.com`
	failed := func(payload any) map[string]any {
		return map[string]any{
			"name": "e1", "state": "FAILED",
			"error": map[string]any{"payload": payload, "context": trace},
		}
	}
	tests := []struct {
		name     string
		exec     map[string]any
		want     error
		upstream string // the workflow's own text, which must not reach the error
	}{
		{
			name:     "reserved namespace",
			exec:     failed(map[string]any{"code": 403.0, "message": "Project namespace is reserved and cannot be activated: kube-system"}),
			want:     ErrReservedName,
			upstream: "cannot be activated",
		},
		{
			name:     "subject conflict",
			exec:     failed(map[string]any{"code": 409.0, "message": "service-admin-rb already exists for a different subject"}),
			want:     ErrAlreadyClaimed,
			upstream: "service-admin-rb",
		},
		{
			name:     "input validation raise",
			exec:     failed("Invalid project_namespace: expected a valid GCP project ID"),
			want:     ErrInvalidInput,
			upstream: "Invalid project_namespace",
		},
		{
			// Recognized by its code, whatever the wording.
			name:     "input validation raise with code 400",
			exec:     failed(map[string]any{"code": 400.0, "message": "project namespace is not a valid ID"}),
			want:     ErrInvalidInput,
			upstream: "not a valid ID",
		},
		{
			name:     "role unavailable is transient",
			exec:     failed("PlatformRole service-admin is unavailable: timeout"),
			want:     ErrTransient,
			upstream: "PlatformRole",
		},
		{
			name:     "gke error is transient",
			exec:     failed("Failed to create service-admin binding: HTTP 500"),
			want:     ErrTransient,
			upstream: "Failed to create",
		},
		{
			name: "no payload is transient",
			exec: map[string]any{"name": "e1", "state": "FAILED"},
			want: ErrTransient,
		},
		{
			name: "cancelled is transient",
			exec: map[string]any{"name": "e1", "state": "CANCELLED"},
			want: ErrTransient,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := fakeWorkflows(t, []map[string]any{tc.exec})
			_, err := c.Activate(context.Background(), "my-project-123", "alice@redhat.com")
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			msg := err.Error()
			if !strings.Contains(msg, "projects/p/locations/r/executions/e1") {
				t.Errorf("error %q does not name the execution", msg)
			}
			for _, text := range []string{tc.upstream, "RuntimeError", "alice@redhat.com"} {
				if text != "" && strings.Contains(msg, text) {
					t.Errorf("error %q carries the workflow's text %q", msg, text)
				}
			}
		})
	}
}

func TestActivateCreateFailureIsTransient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		writeBody(t, w, []byte(`{"error":{"message":"internal"}}`))
	}))
	t.Cleanup(srv.Close)

	c := &Client{
		ProjectID: "p", Region: "r", WorkflowName: "w",
		HTTP: srv.Client(), BaseURL: srv.URL,
		Timeout: time.Second, PollInterval: time.Millisecond,
	}
	if _, err := c.Activate(context.Background(), "my-project-123", "alice@redhat.com"); !errors.Is(err, ErrTransient) {
		t.Fatalf("error = %v, want ErrTransient", err)
	}
}

func TestActivateCreateForbiddenIsMisconfigured(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		writeBody(t, w, []byte(`{"error":{"code":403,"status":"PERMISSION_DENIED","message":"caller lacks workflows.invoker"}}`))
	}))
	t.Cleanup(srv.Close)

	c := &Client{
		ProjectID: "p", Region: "r", WorkflowName: "w",
		HTTP: srv.Client(), BaseURL: srv.URL,
		Timeout: time.Second, PollInterval: time.Millisecond,
	}
	_, err := c.Activate(context.Background(), "my-project-123", "alice@redhat.com")
	if !errors.Is(err, ErrMisconfigured) {
		t.Fatalf("error = %v, want ErrMisconfigured", err)
	}
	// The handler logs this error: it carries the status codes, never the body.
	if msg := err.Error(); !strings.HasSuffix(msg, "create execution: HTTP 403 PERMISSION_DENIED") || strings.Contains(msg, "lacks") {
		t.Errorf("error = %q, want the status codes without the response body", msg)
	}
}

func TestActivateTimesOut(t *testing.T) {
	c := fakeWorkflows(t, []map[string]any{{"name": "e1", "state": "ACTIVE"}})
	c.Timeout = 50 * time.Millisecond
	c.PollInterval = time.Millisecond
	if _, err := c.Activate(context.Background(), "my-project-123", "alice@redhat.com"); !errors.Is(err, ErrTransient) {
		t.Fatalf("error = %v, want ErrTransient", err)
	}
}

func TestClassifyFailureOuterStringPayload(t *testing.T) {
	// Real Executions API wraps the workflow's JSON in an outer JSON string.
	outer409 := marshal(t, map[string]any{"code": 409.0, "message": "exists"})
	wrapped409 := marshal(t, string(outer409))
	if err := classifyFailure("e1", &executionError{Payload: wrapped409}); !errors.Is(err, ErrAlreadyClaimed) {
		t.Fatalf("wrapped 409: got %v, want ErrAlreadyClaimed", err)
	}
	outer403 := marshal(t, map[string]any{"code": 403.0, "message": "reserved"})
	wrapped403 := marshal(t, string(outer403))
	if err := classifyFailure("e1", &executionError{Payload: wrapped403}); !errors.Is(err, ErrReservedName) {
		t.Fatalf("wrapped 403: got %v, want ErrReservedName", err)
	}
	// The wire format for a plain-string payload is a single outer string
	// containing the message; it classifies as input validation.
	directWrapped := marshal(t, "Invalid project_namespace: bad")
	if err := classifyFailure("e1", &executionError{Payload: directWrapped}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("wrapped input validation: got %v, want ErrInvalidInput", err)
	}
}

func TestActivatePollRetriesTransientThenSucceeds(t *testing.T) {
	// First GET after create returns 502, second returns SUCCEEDED
	var gets int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			if _, err := w.Write([]byte(`{"name":"projects/p/locations/r/workflows/w/executions/e1","state":"ACTIVE"}`)); err != nil {
				t.Errorf("write create: %v", err)
			}
			return
		}
		if r.Method == http.MethodGet {
			gets++
			if gets == 1 {
				w.WriteHeader(http.StatusBadGateway)
				if _, err := w.Write([]byte(`{"error":"bad gateway"}`)); err != nil {
					t.Errorf("write 502: %v", err)
				}
				return
			}
			body, err := json.Marshal(map[string]any{
				"name": "e1", "state": "SUCCEEDED",
				"result": `{"status":"activated","namespace":"my-project-123"}`,
			})
			if err != nil {
				t.Errorf("marshal success: %v", err)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			writeBody(t, w, body)
			return
		}
		t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
	}))
	t.Cleanup(srv.Close)
	c := &Client{
		ProjectID: "p", Region: "r", WorkflowName: "w",
		HTTP: srv.Client(), BaseURL: srv.URL,
		Timeout: 5 * time.Second, PollInterval: time.Millisecond,
	}
	res, err := c.Activate(context.Background(), "my-project-123", "alice@redhat.com")
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if res.Status != "activated" {
		t.Fatalf("status=%q", res.Status)
	}
	if gets != 2 {
		t.Fatalf("gets=%d, want 2 (retry exercised)", gets)
	}
}

func TestActivatePollDeniedIsTransientNotMisconfigured(t *testing.T) {
	// POST accepted, GET denied — should be transient (outcome unknown), not misconfigured
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			if _, err := w.Write([]byte(`{"name":"projects/p/locations/r/workflows/w/executions/e1","state":"ACTIVE"}`)); err != nil {
				t.Errorf("write create: %v", err)
			}
			return
		}
		w.WriteHeader(http.StatusForbidden)
		if _, err := w.Write([]byte(`{"error":"forbidden"}`)); err != nil {
			t.Errorf("write 403: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	c := &Client{
		ProjectID: "p", Region: "r", WorkflowName: "w",
		HTTP: srv.Client(), BaseURL: srv.URL,
		Timeout: time.Second, PollInterval: time.Millisecond,
	}
	_, err := c.Activate(context.Background(), "my-project-123", "alice@redhat.com")
	if !errors.Is(err, ErrTransient) {
		t.Fatalf("got %v, want ErrTransient", err)
	}
	if errors.Is(err, ErrMisconfigured) {
		t.Fatalf("must not be ErrMisconfigured (create was accepted, poll was denied)")
	}
}
