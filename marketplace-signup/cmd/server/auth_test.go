package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const refreshTimeout = 200 * time.Millisecond

// useServiceAccountCredentials points Application Default Credentials at a
// throwaway service-account key whose token endpoint is tokenURL. A service
// account refreshes through the same context HTTP client as user-account
// credentials, but unlike those it can be redirected to a test server.
func useServiceAccountCredentials(t *testing.T, tokenURL string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("encode key: %v", err)
	}
	creds, err := json.Marshal(map[string]string{
		"type":           "service_account",
		"project_id":     "throwaway-test",
		"private_key_id": "0000000000000000000000000000000000000000",
		"private_key":    string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		"client_email":   "throwaway@throwaway-test.iam.gserviceaccount.com",
		"client_id":      "1",
		"token_uri":      tokenURL,
	})
	if err != nil {
		t.Fatalf("encode credentials: %v", err)
	}
	path := filepath.Join(t.TempDir(), "service-account.json")
	if err := os.WriteFile(path, creds, 0o600); err != nil {
		t.Fatalf("write credentials: %v", err)
	}
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", path)
}

// getStatus performs a GET and returns the response status, closing the body.
// It is safe to call from a goroutine other than the test's.
func getStatus(t *testing.T, client *http.Client, url string) (int, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	if err := resp.Body.Close(); err != nil {
		t.Errorf("close response body: %v", err)
	}
	return resp.StatusCode, nil
}

// A token endpoint that never answers must not block requests forever: the
// transport refreshes the token before it sends the API request, so the API
// call's own deadline does not bound the refresh.
func TestStalledTokenEndpointFailsTheRequestInsteadOfBlockingIt(t *testing.T) {
	stall := make(chan struct{})
	tokens := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-stall }))
	t.Cleanup(tokens.Close)
	t.Cleanup(func() { close(stall) }) // runs first: lets a stuck handler return
	api := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(api.Close)

	useServiceAccountCredentials(t, tokens.URL)
	client, err := newAuthenticatedClient(t.Context(), refreshTimeout)
	if err != nil {
		t.Fatalf("newAuthenticatedClient: %v", err)
	}

	failed := make(chan error, 1)
	go func() {
		_, err := getStatus(t, client, api.URL)
		failed <- err
	}()
	select {
	case err := <-failed:
		if err == nil {
			t.Fatal("the request succeeded without a token")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the token refresh was not bounded")
	}
}

// Only the refresh is bounded. An API call that takes longer than the refresh
// timeout, such as an activation waiting on its workflow, must still succeed.
func TestSlowAPICallsAreNotCappedByTheRefreshTimeout(t *testing.T) {
	tokens := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if _, err := io.WriteString(w, `{"access_token":"test-access-token","token_type":"Bearer","expires_in":3600}`); err != nil {
			t.Errorf("write token: %v", err)
		}
	}))
	t.Cleanup(tokens.Close)
	var authorization string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		time.Sleep(3 * refreshTimeout)
	}))
	t.Cleanup(api.Close)

	useServiceAccountCredentials(t, tokens.URL)
	client, err := newAuthenticatedClient(t.Context(), refreshTimeout)
	if err != nil {
		t.Fatalf("newAuthenticatedClient: %v", err)
	}

	status, err := getStatus(t, client, api.URL)
	if err != nil {
		t.Fatalf("a call slower than the refresh timeout failed: %v", err)
	}
	if status != http.StatusOK {
		t.Errorf("status = %d, want 200", status)
	}
	if authorization != "Bearer test-access-token" {
		t.Errorf("Authorization = %q, want the refreshed token", authorization)
	}
}
