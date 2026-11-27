// Package notify sends one email per activation by executing the
// "signup-notify" Application Integration: one execution is one email, with a
// custom subject and no credentials in the chain (Google's mailer delivers it).
// The integration lives in the environment's fixed hosting project, which is
// configured separately from the activation workflow's project.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/openshift-online/gecko/marketplace-signup/internal/apierror"
)

const (
	// defaultBaseURL is the Application Integration API host.
	defaultBaseURL = "https://integrations.googleapis.com"

	// triggerID is the API trigger defined in the signup-notify integration;
	// it is fixed by the integration definition, not configurable here.
	triggerID = "api_trigger/SignupNotify_API_1"
)

// Activation is what one notification reports. Its fields are the
// integration's input parameters, so their names are the contract with the
// signup-notify definition in gcp-hcp-infra. The environment, recipients and
// hosting project are rendered into that definition; the backend does not send
// them.
type Activation struct {
	// Signer is the verified Red Hat email that activated the project.
	Signer string
	// Project is the customer project ID the user submitted.
	Project string
	// Status is the workflow outcome: "activated" or "already_active".
	Status string
	// LeaderProject is the regional project whose activate-project workflow
	// ran. It comes from configuration, never from the request.
	LeaderProject string
}

// Client executes the signup-notify integration. It is a separate client
// from the workflow one because the failure semantics differ: a failed
// notification must never affect an activation that already succeeded.
type Client struct {
	// ProjectID and Region locate the integration. They are independent of the
	// activation workflow's project and region.
	ProjectID   string
	Region      string
	Integration string

	// HTTP is the authenticated client. BaseURL exists for tests.
	HTTP    *http.Client
	BaseURL string

	// Timeout bounds a single execute call. Notification is fire-and-forget;
	// the caller discards the error after logging it.
	Timeout time.Duration
}

// NewClient builds a client that executes the integration through httpClient,
// which must authenticate its requests. The backend passes the same client it
// uses for the activate-project workflow, so both act as one identity.
func NewClient(httpClient *http.Client, projectID, region, integration string) *Client {
	return &Client{
		ProjectID:   projectID,
		Region:      region,
		Integration: integration,
		HTTP:        httpClient,
		BaseURL:     defaultBaseURL,
		Timeout:     15 * time.Second,
	}
}

// Notify executes the integration with the activation facts. The recipient is
// fixed in the integration definition: Signer, Project, Status and
// LeaderProject are the only inputs the backend controls, so it cannot
// redirect mail.
func (c *Client) Notify(ctx context.Context, activation Activation) error {
	ctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()

	body, err := json.Marshal(executeRequest{
		TriggerID: triggerID,
		InputParameters: map[string]parameterValue{
			"Signer":        {StringValue: activation.Signer},
			"Project":       {StringValue: activation.Project},
			"Status":        {StringValue: activation.Status},
			"LeaderProject": {StringValue: activation.LeaderProject},
		},
	})
	if err != nil {
		return fmt.Errorf("encode notification request: %w", err)
	}

	endpoint := fmt.Sprintf("%s/v1/projects/%s/locations/%s/integrations/%s:execute",
		c.BaseURL, url.PathEscape(c.ProjectID), url.PathEscape(c.Region), url.PathEscape(c.Integration))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("execute integration: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("execute integration: %s", apierror.Describe(resp))
	}

	// The response body is large execution metadata; drain a bounded amount
	// so the connection can be reused, then discard.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	return nil
}

// executeRequest is the subset of the integrations.execute request.
type executeRequest struct {
	TriggerID       string                    `json:"triggerId"`
	InputParameters map[string]parameterValue `json:"inputParameters"`
}

type parameterValue struct {
	StringValue string `json:"stringValue"`
}
