package config

import (
	"strings"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	env := map[string]string{
		"OAUTH_CLIENT_ID":     "123.apps.googleusercontent.com",
		"WORKFLOW_PROJECT_ID": "platform-project",
		"WORKFLOW_REGION":     "northamerica-northeast2",
	}
	cfg, err := Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Port != "8080" || cfg.WorkflowName != "activate-project" {
		t.Fatalf("defaults not applied: %+v", cfg)
	}
	if got, want := strings.Join(cfg.AllowedEmailDomains, ","), "redhat.com"; got != want {
		t.Fatalf("AllowedEmailDomains = %q, want %q", got, want)
	}
}

func TestLoadMissingRequired(t *testing.T) {
	_, err := Load(func(string) string { return "" })
	if err == nil {
		t.Fatal("Load with empty environment succeeded, want error")
	}
	for _, want := range []string{"OAUTH_CLIENT_ID", "WORKFLOW_PROJECT_ID", "WORKFLOW_REGION"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not name %s", err.Error(), want)
		}
	}
}

func TestLoadNormalizesDomainList(t *testing.T) {
	env := map[string]string{
		"OAUTH_CLIENT_ID":       "c",
		"WORKFLOW_PROJECT_ID":   "p",
		"WORKFLOW_REGION":       "r",
		"ALLOWED_EMAIL_DOMAINS": " RedHat.com , redhat.com ,partners.example ",
	}
	cfg, err := Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, want := strings.Join(cfg.AllowedEmailDomains, ","), "redhat.com,partners.example"; got != want {
		t.Fatalf("AllowedEmailDomains = %q, want %q", got, want)
	}
}

// baseEnv is the minimum valid environment; tests add to it.
func baseEnv() map[string]string {
	return map[string]string{
		"OAUTH_CLIENT_ID":     "123.apps.googleusercontent.com",
		"WORKFLOW_PROJECT_ID": "leader-project-1",
		"WORKFLOW_REGION":     "us-east1",
	}
}

func load(env map[string]string) (Config, error) {
	return Load(func(k string) string { return env[k] })
}

func TestLoadNotificationsAreOffByDefault(t *testing.T) {
	cfg, err := load(baseEnv())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.NotifyIntegration != "" || cfg.NotifyProjectID != "" || cfg.NotifyRegion != "" {
		t.Fatalf("notifications are not off by default: %+v", cfg)
	}
}

func TestLoadNotificationCoordinatesAreIndependentOfTheWorkflow(t *testing.T) {
	env := baseEnv()
	env["NOTIFY_INTEGRATION"] = "signup-notify"
	env["NOTIFY_PROJECT_ID"] = "host-project-1"
	env["NOTIFY_REGION"] = "northamerica-northeast2"

	cfg, err := load(env)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.WorkflowProjectID != "leader-project-1" || cfg.WorkflowRegion != "us-east1" {
		t.Errorf("workflow coordinates = %q/%q", cfg.WorkflowProjectID, cfg.WorkflowRegion)
	}
	if cfg.NotifyProjectID != "host-project-1" || cfg.NotifyRegion != "northamerica-northeast2" {
		t.Errorf("notification coordinates = %q/%q", cfg.NotifyProjectID, cfg.NotifyRegion)
	}
}

// There is no fallback to the workflow's project and region: a notifier that
// silently followed the authorization leader would look for an integration that
// does not exist there.
func TestLoadNotificationRequiresItsOwnCoordinates(t *testing.T) {
	env := baseEnv()
	env["NOTIFY_INTEGRATION"] = "signup-notify"

	_, err := load(env)
	if err == nil {
		t.Fatal("Load with an integration but no coordinates succeeded, want error")
	}
	for _, want := range []string{"NOTIFY_PROJECT_ID", "NOTIFY_REGION"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %s", err.Error(), want)
		}
	}
}

func TestLoadRejectsMalformedNotificationCoordinates(t *testing.T) {
	for _, tc := range []struct {
		name, key, value string
	}{
		{"project too short", "NOTIFY_PROJECT_ID", "tiny"},
		{"project uppercase", "NOTIFY_PROJECT_ID", "Host-Project-1"},
		{"project trailing hyphen", "NOTIFY_PROJECT_ID", "host-project-"},
		{"project with a path", "NOTIFY_PROJECT_ID", "host-project/../x"},
		{"region uppercase", "NOTIFY_REGION", "US-CENTRAL1"},
		{"region without a number", "NOTIFY_REGION", "us-central"},
		{"region with a space", "NOTIFY_REGION", "us central1"},
		{"region with a path", "NOTIFY_REGION", "us-central1/../x"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := baseEnv()
			env["NOTIFY_INTEGRATION"] = "signup-notify"
			env["NOTIFY_PROJECT_ID"] = "host-project-1"
			env["NOTIFY_REGION"] = "us-central1"
			env[tc.key] = tc.value

			_, err := load(env)
			if err == nil {
				t.Fatalf("Load accepted %s=%q", tc.key, tc.value)
			}
			if !strings.Contains(err.Error(), tc.key) {
				t.Errorf("error %q does not name %s", err.Error(), tc.key)
			}
		})
	}
}

// A typo is reported even while notifications are disabled, so it cannot wait
// to surface the day they are enabled.
func TestLoadValidatesUnusedNotificationCoordinates(t *testing.T) {
	env := baseEnv()
	env["NOTIFY_REGION"] = "Not A Region"

	if _, err := load(env); err == nil {
		t.Fatal("Load accepted a malformed NOTIFY_REGION while notifications were disabled")
	}
}
