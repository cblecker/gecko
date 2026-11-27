// Package workflow triggers the platform's existing activate-project Cloud
// Workflow and maps its outcomes onto the sign-up result categories.
package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/openshift-online/gecko/marketplace-signup/internal/apierror"
)

const (
	// Executions live on the workflowexecutions API host; the workflows host
	// only serves workflow definitions (a create there returns a 404 page).
	defaultBaseURL  = "https://workflowexecutions.googleapis.com"
	maxResponseBody = 1 << 20
)

var (
	// ErrAlreadyClaimed means the namespace's service-admin binding exists for
	// a different subject (workflow raise code 409).
	ErrAlreadyClaimed = errors.New("namespace already administered by another user")
	// ErrReservedName means the namespace is reserved by the platform
	// (workflow raise code 403).
	ErrReservedName = errors.New("namespace is reserved")
	// ErrInvalidInput means the workflow rejected the arguments. The backend
	// validates first, so this indicates a bug or a contract change.
	ErrInvalidInput = errors.New("workflow rejected its arguments")
	// ErrTransient means the activation failed for an infrastructure reason and
	// may succeed on retry.
	ErrTransient = errors.New("transient workflow failure")
	// ErrMisconfigured means the backend could not call the workflow at all
	// (usually a missing invoker grant). Retrying will never help.
	ErrMisconfigured = errors.New("workflow invocation not authorized")
)

// Result is the successful activation outcome.
type Result struct {
	// Status is "activated" for a new binding or "already_active" when the
	// same subject already held service-admin.
	Status    string
	Namespace string
}

// Client invokes activate-project executions and waits for the terminal state.
type Client struct {
	ProjectID    string
	Region       string
	WorkflowName string

	// HTTP is the authenticated client. BaseURL exists for tests.
	HTTP    *http.Client
	BaseURL string

	// Timeout bounds the whole create-and-wait cycle. The workflow itself
	// retries role readiness for up to a couple of minutes.
	Timeout      time.Duration
	PollInterval time.Duration
}

// NewClient builds a client that calls the Workflows Executions API through
// httpClient, which must authenticate its requests. The backend passes one
// backed by Application Default Credentials.
func NewClient(httpClient *http.Client, projectID, region, workflowName string) *Client {
	return &Client{
		ProjectID:    projectID,
		Region:       region,
		WorkflowName: workflowName,
		HTTP:         httpClient,
		BaseURL:      defaultBaseURL,
		Timeout:      3 * time.Minute,
		PollInterval: 2 * time.Second,
	}
}

// Activate runs the workflow with {project_namespace, subject} and waits for it
// to finish.
func (c *Client) Activate(ctx context.Context, namespace, subject string) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()

	argument, err := json.Marshal(map[string]string{
		"project_namespace": namespace,
		"subject":           subject,
	})
	if err != nil {
		return Result{}, fmt.Errorf("encode workflow arguments: %w", err)
	}

	parent := fmt.Sprintf("%s/v1/projects/%s/locations/%s/workflows/%s/executions",
		c.BaseURL, c.ProjectID, c.Region, c.WorkflowName)
	createBody, err := json.Marshal(map[string]string{"argument": string(argument)})
	if err != nil {
		return Result{}, fmt.Errorf("encode execution request: %w", err)
	}

	var created struct {
		Name string `json:"name"`
	}
	if err := c.do(ctx, http.MethodPost, parent, createBody, &created); err != nil {
		if isAuthorizationFailure(err) {
			return Result{}, fmt.Errorf("%w: create execution: %w", ErrMisconfigured, err)
		}
		return Result{}, fmt.Errorf("%w: create execution: %w", ErrTransient, err)
	}
	if created.Name == "" {
		return Result{}, fmt.Errorf("%w: execution create returned no name", ErrTransient)
	}

	for {
		var execution execution
		if err := c.do(ctx, http.MethodGet, c.BaseURL+"/v1/"+created.Name, nil, &execution); err != nil {
			if isAuthorizationFailure(err) {
				// POST was accepted (execution exists), so this is not a
				// misconfiguration of the create call — the outcome is simply
				// unreadable. Report as transient/unknown, not unavailable.
				return Result{}, fmt.Errorf("%w: poll denied, outcome unknown (execution %s): %w", ErrTransient, created.Name, err)
			}
			// Transient poll failure (e.g., 502/503 from Workflows). Retry
			// against the same execution until the overall timeout expires;
			// do not resubmit the POST. The accepted execution may still
			// create the binding.
			select {
			case <-ctx.Done():
				return Result{}, fmt.Errorf("%w: poll failed and deadline exceeded for %s: %w", ErrTransient, created.Name, err)
			case <-time.After(c.PollInterval):
				continue
			}
		}
		switch execution.State {
		case "SUCCEEDED":
			return resultFromExecution(namespace, execution.Result)
		case "FAILED", "CANCELLED":
			return Result{}, classifyFailure(created.Name, execution.Error)
		}

		select {
		case <-ctx.Done():
			return Result{}, fmt.Errorf("%w: execution %s did not finish in time", ErrTransient, created.Name)
		case <-time.After(c.PollInterval):
		}
	}
}

// execution is the subset of the executions resource the backend reads.
type execution struct {
	Name   string          `json:"name"`
	State  string          `json:"state"`
	Result string          `json:"result"`
	Error  *executionError `json:"error"`
}

// executionError keeps only the payload. Its context (a stack trace) and the
// payload's message are free text from the workflow; the full error stays in
// the execution's record.
type executionError struct {
	Payload json.RawMessage `json:"payload"`
}

func resultFromExecution(namespace, rawResult string) (Result, error) {
	var payload struct {
		Status    string `json:"status"`
		Namespace string `json:"namespace"`
	}
	if err := json.Unmarshal([]byte(rawResult), &payload); err != nil {
		return Result{}, fmt.Errorf("%w: undecodable workflow result: %w", ErrTransient, err)
	}
	switch payload.Status {
	case "activated", "already_active":
	default:
		return Result{}, fmt.Errorf("%w: unexpected workflow status %q", ErrTransient, payload.Status)
	}
	if payload.Namespace == "" {
		payload.Namespace = namespace
	}
	return Result{Status: payload.Status, Namespace: payload.Namespace}, nil
}

// classifyFailure maps a failed or cancelled execution to an error category.
// The workflow raises either an object with a numeric code (invalid input,
// reserved namespace, subject conflict) or a plain string (input validation,
// infrastructure failures). The Executions REST API returns payload as a JSON
// string containing JSON, so both the raw payload and an outer-string-decoded
// form are tried.
//
// The handler logs the returned error, so it names the execution, whose record
// holds the workflow's full error, and never carries the workflow's message.
func classifyFailure(execution string, execErr *executionError) error {
	kind := ErrTransient
	if execErr != nil && len(execErr.Payload) > 0 {
		if k := classifyPayload(execErr.Payload); k != nil {
			kind = k
		} else {
			// Executions API wire format: outer JSON string wrapping the workflow's JSON.
			var outer string
			if err := json.Unmarshal(execErr.Payload, &outer); err == nil {
				if k := classifyPayload([]byte(outer)); k != nil {
					kind = k
				}
			}
		}
	}
	return fmt.Errorf("%w: execution %s", kind, execution)
}

// classifyPayload returns the category of a workflow error payload, or nil if
// the backend does not recognize it. It reads the message only to recognize
// today's input errors by their wording, and never returns it.
func classifyPayload(raw []byte) error {
	var obj struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil {
		switch obj.Code {
		case 400:
			return ErrInvalidInput
		case 403:
			return ErrReservedName
		case 409:
			return ErrAlreadyClaimed
		}
		if isInputValidation(obj.Message) {
			return ErrInvalidInput
		}
	}
	var msg string
	if err := json.Unmarshal(raw, &msg); err == nil && isInputValidation(msg) {
		return ErrInvalidInput
	}
	return nil
}

// statusError is an HTTP failure from the Workflows API.
type statusError struct {
	code int
	// summary describes the response without its body (see apierror.Describe):
	// the error is logged.
	summary string
}

func (e *statusError) Error() string {
	return e.summary
}

// isAuthorizationFailure reports whether err means the backend itself is not
// allowed to call the workflow (missing/incorrect IAM), as opposed to a
// retryable failure.
func isAuthorizationFailure(err error) bool {
	var se *statusError
	if errors.As(err, &se) {
		return se.code == http.StatusUnauthorized || se.code == http.StatusForbidden
	}
	return false
}

// isInputValidation recognizes activate-project's input errors by their
// wording, because the workflow raises them as plain strings. A raise with code
// 400 is recognized by its code instead (see classifyPayload), which survives
// rewording: prefer that when the workflow next changes.
func isInputValidation(message string) bool {
	for _, prefix := range []string{
		"Missing or empty required parameter",
		"Invalid project_namespace",
		"Invalid subject",
	} {
		if strings.HasPrefix(message, prefix) {
			return true
		}
	}
	return false
}

// do performs one API call, mapping transport and status failures to transient
// errors (activation failures are classified separately, from the execution).
func (c *Client) do(ctx context.Context, method, url string, body []byte, out any) error {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &statusError{code: resp.StatusCode, summary: apierror.Describe(resp)}
	}
	return json.NewDecoder(io.LimitReader(resp.Body, maxResponseBody)).Decode(out)
}
