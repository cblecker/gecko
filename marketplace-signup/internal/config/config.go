// Package config loads and validates the backend's configuration from the
// environment. Every value is required unless a documented default applies.
// The only identity-bearing default is the restrictive one:
// ALLOWED_EMAIL_DOMAINS falls back to redhat.com, and the chart always sets it.
package config

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	// projectIDPattern is the GCP project ID syntax: 6-30 characters, leading
	// letter, lowercase letters, digits and hyphens, no trailing hyphen.
	projectIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]$`)

	// regionPattern matches GCP region names such as us-central1 and
	// northamerica-northeast2.
	regionPattern = regexp.MustCompile(`^[a-z]+-[a-z]+[0-9]+$`)
)

// Config is the backend's runtime configuration.
type Config struct {
	// Port is the HTTP listen port.
	Port string

	// OAuthClientID is the dedicated OAuth client the SPA signs in with.
	// ID tokens must carry it in their aud claim.
	OAuthClientID string

	// AllowedEmailDomains is the hosted-domain allowlist. Google's signed
	// hd claim must equal one of these, and the email domain must agree.
	AllowedEmailDomains []string

	// Workflow coordinates for the existing activate-project workflow. They
	// name the current authorization leader's regional project and location.
	// The leader project is also reported in every notification.
	WorkflowProjectID string
	WorkflowRegion    string
	WorkflowName      string

	// NotifyIntegration is the name of the Application Integration that sends
	// the per-activation email. Empty disables notifications entirely.
	NotifyIntegration string

	// NotifyProjectID and NotifyRegion locate the notification integration.
	// They are independent of the workflow coordinates: the integration lives
	// in the environment's fixed hosting project, while the workflow runs in
	// whichever region is the authorization leader. There is deliberately no
	// fallback to the workflow coordinates; they are required whenever
	// NotifyIntegration is set.
	NotifyProjectID string
	NotifyRegion    string

	// FrontendDir, when set, serves the SPA from disk at the same origin.
	// Local development only: in deployment the SPA is served by its own
	// container on the same hostname.
	FrontendDir string

	// AllowedOrigin is the SPA origin permitted by CORS. The SPA and the API
	// share one hostname in deployment, so this is normally empty, which means
	// no cross-origin access: only same-origin requests work.
	AllowedOrigin string
}

// Getenv mirrors os.Getenv so Load is testable.
type Getenv func(string) string

// Load reads configuration, returning an error naming every missing or
// malformed required value.
func Load(getenv Getenv) (Config, error) {
	cfg := Config{
		Port:              orDefault(getenv("PORT"), "8080"),
		OAuthClientID:     strings.TrimSpace(getenv("OAUTH_CLIENT_ID")),
		WorkflowProjectID: strings.TrimSpace(getenv("WORKFLOW_PROJECT_ID")),
		WorkflowRegion:    strings.TrimSpace(getenv("WORKFLOW_REGION")),
		WorkflowName:      orDefault(strings.TrimSpace(getenv("WORKFLOW_NAME")), "activate-project"),
		NotifyIntegration: strings.TrimSpace(getenv("NOTIFY_INTEGRATION")),
		NotifyProjectID:   strings.TrimSpace(getenv("NOTIFY_PROJECT_ID")),
		NotifyRegion:      strings.TrimSpace(getenv("NOTIFY_REGION")),
		FrontendDir:       strings.TrimSpace(getenv("FRONTEND_DIR")),
		AllowedOrigin:     strings.TrimRight(strings.TrimSpace(getenv("ALLOWED_ORIGIN")), "/"),
	}

	cfg.AllowedEmailDomains = splitList(orDefault(getenv("ALLOWED_EMAIL_DOMAINS"), "redhat.com"))

	var missing []string
	if cfg.OAuthClientID == "" {
		missing = append(missing, "OAUTH_CLIENT_ID")
	}
	if cfg.WorkflowProjectID == "" {
		missing = append(missing, "WORKFLOW_PROJECT_ID")
	}
	if cfg.WorkflowRegion == "" {
		missing = append(missing, "WORKFLOW_REGION")
	}
	if len(cfg.AllowedEmailDomains) == 0 {
		missing = append(missing, "ALLOWED_EMAIL_DOMAINS")
	}
	if cfg.NotifyIntegration != "" {
		if cfg.NotifyProjectID == "" {
			missing = append(missing, "NOTIFY_PROJECT_ID")
		}
		if cfg.NotifyRegion == "" {
			missing = append(missing, "NOTIFY_REGION")
		}
	}
	if len(missing) > 0 {
		return Config{}, fmt.Errorf("missing required configuration: %s", strings.Join(missing, ", "))
	}

	// Validate the coordinates' format, so a typo fails at startup instead of
	// failing every sign-up as a transient error. The notification destination
	// is checked even when it is unused, so a typo surfaces before notifications
	// are first enabled.
	var malformed []string
	if !projectIDPattern.MatchString(cfg.WorkflowProjectID) {
		malformed = append(malformed, "WORKFLOW_PROJECT_ID (expected a GCP project ID)")
	}
	if !regionPattern.MatchString(cfg.WorkflowRegion) {
		malformed = append(malformed, "WORKFLOW_REGION (expected a GCP region such as us-central1)")
	}
	if cfg.NotifyProjectID != "" && !projectIDPattern.MatchString(cfg.NotifyProjectID) {
		malformed = append(malformed, "NOTIFY_PROJECT_ID (expected a GCP project ID)")
	}
	if cfg.NotifyRegion != "" && !regionPattern.MatchString(cfg.NotifyRegion) {
		malformed = append(malformed, "NOTIFY_REGION (expected a GCP region such as us-central1)")
	}
	if len(malformed) > 0 {
		return Config{}, fmt.Errorf("malformed configuration: %s", strings.Join(malformed, ", "))
	}

	return cfg, nil
}

func orDefault(value, fallback string) string {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return fallback
}

func splitList(value string) []string {
	var out []string
	seen := make(map[string]struct{})
	for _, part := range strings.Split(value, ",") {
		part = strings.ToLower(strings.TrimSpace(part))
		if part == "" {
			continue
		}
		if _, dup := seen[part]; dup {
			continue
		}
		seen[part] = struct{}{}
		out = append(out, part)
	}
	return out
}
