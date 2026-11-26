package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openshift-online/gecko/marketplace-signup/internal/auth"
	"github.com/openshift-online/gecko/marketplace-signup/internal/config"
	"github.com/openshift-online/gecko/marketplace-signup/internal/httpapi"
	"github.com/openshift-online/gecko/marketplace-signup/internal/identity"
	"github.com/openshift-online/gecko/marketplace-signup/internal/notify"
	"github.com/openshift-online/gecko/marketplace-signup/internal/workflow"
)

const (
	customerProject = "my-project-123"
	leaderProject   = "leader-project-1"
	leaderRegion    = "us-east1"
	hostProject     = "host-project-1"
	hostRegion      = "us-central1"
	execution       = "projects/" + leaderProject + "/locations/" + leaderRegion + "/workflows/activate-project/executions/e1"
)

func testConfig() config.Config {
	return config.Config{
		OAuthClientID:     "test-client",
		WorkflowProjectID: leaderProject,
		WorkflowRegion:    leaderRegion,
		WorkflowName:      "activate-project",
		NotifyIntegration: "signup-notify",
		NotifyProjectID:   hostProject,
		NotifyRegion:      hostRegion,
	}
}

// writeBody writes a fake API response, reporting a failed write at its source
// instead of leaving it to surface as a later, unrelated failure.
func writeBody(t *testing.T, w http.ResponseWriter, body string) {
	t.Helper()
	if _, err := io.WriteString(w, body); err != nil {
		t.Errorf("write response: %v", err)
	}
}

type fakeVerifier struct{}

func (fakeVerifier) Verify(context.Context, string) (auth.Identity, error) {
	return auth.Identity{Subject: "sub-1", Email: "alice@redhat.com", EmailVerified: true, HostedDomain: "redhat.com"}, nil
}

// requestLog records the requests one fake Google API receives.
type requestLog struct {
	mu       sync.Mutex
	requests []string
}

func (l *requestLog) add(r *http.Request) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.requests = append(l.requests, r.Method+" "+r.URL.Path)
}

func (l *requestLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.requests...)
}

func equal(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// TestActivationAndNotificationTargetDifferentProjects drives one sign-up
// through the handler built by the production wiring (newDeps), against fake
// Google APIs. The workflow runs in the authorization leader's project while the
// email is sent from the separate hosting project, and the email reports the
// leader project it was activated through.
func TestActivationAndNotificationTargetDifferentProjects(t *testing.T) {
	workflowRequests := &requestLog{}
	workflows := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		workflowRequests.add(r)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/projects/"+leaderProject+"/locations/"+leaderRegion+"/workflows/activate-project/executions":
			writeBody(t, w, `{"name":"`+execution+`","state":"ACTIVE"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/"+execution:
			writeBody(t, w, `{"state":"SUCCEEDED","result":"{\"status\":\"activated\",\"namespace\":\"`+customerProject+`\"}"}`)
		default:
			t.Errorf("workflow API got an unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(workflows.Close)

	notified := make(chan struct{})
	notifyRequests := &requestLog{}
	var notifyBody struct {
		InputParameters map[string]struct {
			StringValue string `json:"stringValue"`
		} `json:"inputParameters"`
	}
	notifier := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		notifyRequests.add(r)
		if err := json.NewDecoder(r.Body).Decode(&notifyBody); err != nil {
			t.Errorf("decode notification request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		writeBody(t, w, `{}`)
		close(notified)
	}))
	t.Cleanup(notifier.Close)

	cfg := testConfig()
	deps := newDeps(cfg, http.DefaultClient)
	deps.Verifier = fakeVerifier{} // the real verifier needs Google's signing keys
	activator, ok := deps.Activator.(*workflow.Client)
	if !ok {
		t.Fatalf("activator is %T, want *workflow.Client", deps.Activator)
	}
	activator.BaseURL, activator.PollInterval = workflows.URL, time.Millisecond
	notifyClient, ok := deps.Notifier.(*notify.Client)
	if !ok {
		t.Fatalf("notifier is %T, want *notify.Client", deps.Notifier)
	}
	notifyClient.BaseURL = notifier.URL

	policy, err := identity.NewPolicy([]string{"redhat.com"})
	if err != nil {
		t.Fatalf("NewPolicy: %v", err)
	}
	handler := httpapi.NewHandler(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), deps, policy).Routes()

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/signup",
		strings.NewReader(`{"project_id":"`+customerProject+`","id_token":"id.token.sig"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	select {
	case <-notified:
	case <-time.After(5 * time.Second):
		t.Fatal("notification not sent")
	}

	// The activation went to the leader's project and nowhere else.
	wantWorkflow := []string{
		"POST /v1/projects/" + leaderProject + "/locations/" + leaderRegion + "/workflows/activate-project/executions",
		"GET /v1/" + execution,
	}
	if got := workflowRequests.snapshot(); !equal(got, wantWorkflow) {
		t.Errorf("workflow requests = %v, want %v", got, wantWorkflow)
	}

	// The email went to the hosting project and reports the leader.
	wantNotify := []string{"POST /v1/projects/" + hostProject + "/locations/" + hostRegion + "/integrations/signup-notify:execute"}
	if got := notifyRequests.snapshot(); !equal(got, wantNotify) {
		t.Errorf("notification requests = %v, want %v", got, wantNotify)
	}
	wantInputs := map[string]string{
		"Signer":        "alice@redhat.com",
		"Project":       customerProject,
		"Status":        "activated",
		"LeaderProject": leaderProject,
	}
	if len(notifyBody.InputParameters) != len(wantInputs) {
		t.Errorf("notification inputs = %+v, want exactly %v", notifyBody.InputParameters, wantInputs)
	}
	for key, want := range wantInputs {
		if got := notifyBody.InputParameters[key].StringValue; got != want {
			t.Errorf("notification input %q = %q, want %q", key, got, want)
		}
	}
}

// With notifications disabled the handler must get no notifier at all. A nil
// *notify.Client stored in the interface would be non-nil, and the handler
// would call it.
func TestNotificationsDisabledLeaveNoNotifier(t *testing.T) {
	cfg := testConfig()
	cfg.NotifyIntegration, cfg.NotifyProjectID, cfg.NotifyRegion = "", "", ""

	if deps := newDeps(cfg, http.DefaultClient); deps.Notifier != nil {
		t.Fatalf("notifier = %#v, want nil when NOTIFY_INTEGRATION is unset", deps.Notifier)
	}
}
