// Package apierror summarizes error responses from Google APIs for logs.
package apierror

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
)

// maxBody bounds how much of an error response is read. Google's error
// bodies, details included, are well under it.
const maxBody = 16 << 10

// errorInfoType identifies the google.rpc.ErrorInfo detail, whose reason is a
// fixed code such as IAM_PERMISSION_DENIED.
const errorInfoType = "type.googleapis.com/google.rpc.ErrorInfo"

// code matches the values that are safe to log: fixed, upper-case codes.
var code = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)

// Describe summarizes a failed response as its HTTP status and, when the body
// is a Google API error, its canonical status and ErrorInfo reason, for example
// "HTTP 403 PERMISSION_DENIED (IAM_PERMISSION_DENIED)". It never returns the
// body: the error message is free text that can echo request data, so only
// fields that are fixed codes are kept.
func Describe(resp *http.Response) string {
	summary := fmt.Sprintf("HTTP %d", resp.StatusCode)
	var envelope struct {
		Error struct {
			Status  string `json:"status"`
			Details []struct {
				Type   string `json:"@type"`
				Reason string `json:"reason"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(&envelope); err != nil {
		return summary
	}
	if code.MatchString(envelope.Error.Status) {
		summary += " " + envelope.Error.Status
	}
	for _, detail := range envelope.Error.Details {
		if detail.Type == errorInfoType && code.MatchString(detail.Reason) {
			summary += " (" + detail.Reason + ")"
			break
		}
	}
	return summary
}
