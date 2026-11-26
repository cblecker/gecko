// Command server runs the sign-up backend.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"github.com/openshift-online/gecko/marketplace-signup/internal/auth"
	"github.com/openshift-online/gecko/marketplace-signup/internal/config"
	"github.com/openshift-online/gecko/marketplace-signup/internal/httpapi"
	"github.com/openshift-online/gecko/marketplace-signup/internal/identity"
	"github.com/openshift-online/gecko/marketplace-signup/internal/notify"
	"github.com/openshift-online/gecko/marketplace-signup/internal/workflow"
)

// cloudPlatformScope is the scope the Workflows and Application Integration
// APIs accept.
const cloudPlatformScope = "https://www.googleapis.com/auth/cloud-platform"

// tokenRefreshTimeout bounds each OAuth token refresh. Credentials from a user
// account (local development) refresh through an HTTP client with no timeout,
// so a stalled token endpoint would block every request that shares the token
// source. The deployed Workload Identity path goes through the metadata server,
// which has its own timeouts.
const tokenRefreshTimeout = 10 * time.Second

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("server exited with error", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}

	policy, err := identity.NewPolicy(cfg.AllowedEmailDomains)
	if err != nil {
		return err
	}

	// One identity for every Google API call: the pod's Workload Identity
	// service account, through Application Default Credentials.
	ctx := context.Background()
	httpClient, err := newAuthenticatedClient(ctx, tokenRefreshTimeout)
	if err != nil {
		return err
	}
	deps := newDeps(cfg, httpClient)

	if deps.Notifier != nil {
		logger.Info("activation notifications enabled",
			"integration", cfg.NotifyIntegration,
			"notify_project", cfg.NotifyProjectID,
			"notify_region", cfg.NotifyRegion,
			"workflow_project", cfg.WorkflowProjectID,
			"workflow_region", cfg.WorkflowRegion,
		)
	}

	handler := httpapi.NewHandler(cfg, logger, deps, policy)

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           handler.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		// Activations wait on the workflow (bounded by its own 3-minute
		// timeout), so the write deadline must exceed that.
		WriteTimeout: 4 * time.Minute,
		IdleTimeout:  120 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		logger.Info("listening", "port", cfg.Port)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		logger.Info("shutting down")
		return srv.Shutdown(shutdownCtx)
	}
}

// newAuthenticatedClient returns an HTTP client that signs its requests with
// Application Default Credentials. Each token refresh is bounded by
// refreshTimeout; the API calls themselves are not, because they carry their own
// deadlines (an activation legitimately waits for minutes).
func newAuthenticatedClient(ctx context.Context, refreshTimeout time.Duration) (*http.Client, error) {
	// oauth2.NewClient copies the timeout of the HTTP client in its context onto
	// the client it returns, so the bounded client goes only to the token source.
	refreshCtx := context.WithValue(ctx, oauth2.HTTPClient, &http.Client{Timeout: refreshTimeout})
	tokens, err := google.DefaultTokenSource(refreshCtx, cloudPlatformScope)
	if err != nil {
		return nil, fmt.Errorf("resolve default credentials: %w", err)
	}
	return oauth2.NewClient(ctx, tokens), nil
}

// newDeps wires the handler's collaborators from the configuration. The
// activation workflow and the notifier are addressed independently: the
// workflow follows the authorization leader, while the notification integration
// stays in the environment's fixed hosting project.
func newDeps(cfg config.Config, httpClient *http.Client) httpapi.Deps {
	deps := httpapi.Deps{
		Verifier:  auth.NewVerifier(cfg.OAuthClientID),
		Activator: workflow.NewClient(httpClient, cfg.WorkflowProjectID, cfg.WorkflowRegion, cfg.WorkflowName),
	}
	// Notifications are optional: NOTIFY_INTEGRATION empty means none (local
	// development). Assign the notifier only when enabled, because a nil
	// *notify.Client stored in the interface would be non-nil and panic when used.
	if cfg.NotifyIntegration != "" {
		deps.Notifier = notify.NewClient(httpClient, cfg.NotifyProjectID, cfg.NotifyRegion, cfg.NotifyIntegration)
	}
	return deps
}
