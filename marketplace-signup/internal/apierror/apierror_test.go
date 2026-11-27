package apierror

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func response(status int, body io.Reader) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(body)}
}

func TestDescribe(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{
			name:   "a Google error keeps only its codes",
			status: http.StatusForbidden,
			body: `{"error":{"code":403,"status":"PERMISSION_DENIED","message":"Permission denied for alice@redhat.com",` +
				`"details":[{"@type":"type.googleapis.com/google.rpc.Help","reason":"NOT_THIS_ONE"},` +
				`{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"IAM_PERMISSION_DENIED","metadata":{"permission":"p"}}]}}`,
			want: "HTTP 403 PERMISSION_DENIED (IAM_PERMISSION_DENIED)",
		},
		{
			name:   "a status without details",
			status: http.StatusNotFound,
			body:   `{"error":{"code":404,"status":"NOT_FOUND","message":"no such integration"}}`,
			want:   "HTTP 404 NOT_FOUND",
		},
		{
			name:   "a body that is not a Google error",
			status: http.StatusBadGateway,
			body:   "<html><body>502 Bad Gateway</body></html>",
			want:   "HTTP 502",
		},
		{
			name:   "an empty body",
			status: http.StatusInternalServerError,
			want:   "HTTP 500",
		},
		{
			name:   "fields that are not codes are dropped",
			status: http.StatusBadRequest,
			body: `{"error":{"status":"invalid for alice@redhat.com",` +
				`"details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"not a code"}]}}`,
			want: "HTTP 400",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Describe(response(tt.status, strings.NewReader(tt.body))); got != tt.want {
				t.Errorf("Describe = %q, want %q", got, tt.want)
			}
		})
	}
}

// countingReader counts the bytes read through it.
type countingReader struct {
	r io.Reader
	n int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += n
	return n, err
}

func TestDescribeBoundsTheRead(t *testing.T) {
	body := &countingReader{r: strings.NewReader(
		`{"error":{"message":"` + strings.Repeat("x", 4*maxBody) + `","status":"INTERNAL"}}`)}
	if got, want := Describe(response(http.StatusInternalServerError, body)), "HTTP 500"; got != want {
		t.Errorf("Describe = %q, want %q: an oversized body is not parsed", got, want)
	}
	if body.n > maxBody {
		t.Errorf("read %d bytes, want at most %d", body.n, maxBody)
	}
}
