package versionsync

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestReleaseControllerTransportErrorOmitsEndpoint(t *testing.T) {
	source, err := NewReleaseControllerClient("https://private.internal.example")
	require.NoError(t, err)
	source.httpClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("dial private.internal.example failed")
	})}
	_, err = source.ListStreams(context.Background())
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "private.internal.example")
	assert.Contains(t, err.Error(), "transport error")
}

func TestReleaseControllerAcceptedTags(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/prefix/api/v1/releasestream/4.22.0-0.nightly/tags", r.URL.Path)
		assert.Equal(t, "Accepted", r.URL.Query().Get("phase"))
		assert.Equal(t, "application/json", r.Header.Get("Accept"))
		fmt.Fprint(w, `{"name":"4.22.0-0.nightly","tags":[
   {"name":"4.22.0-0.nightly-2026-10-04-051631","phase":"Accepted","pullSpec":"registry.ci.openshift.org/ocp/release:4.22.0-0.nightly-2026-10-04-051631"},
   {"name":"4.22.0-0.nightly-2026-10-01-110840","phase":"Accepted","pullSpec":"registry.ci.openshift.org/ocp/release:4.22.0-0.nightly-2026-10-01-110840"},
   {"name":"4.22.0-0.nightly-rejected","phase":"Rejected"},
   {"name":"4.22.0-0.nightly-ready","phase":"Ready"},
   {"name":"4.22.0-0.nightly-pending","phase":"Pending"},
   {"name":"4.22.0-0.nightly-failed","phase":"Failed"}]}`)
	}))
	defer server.Close()
	source, err := NewReleaseControllerClient(server.URL + "/prefix/")
	require.NoError(t, err)
	releases, err := source.ListReleases(context.Background(), "4.22.0-0.nightly")
	require.NoError(t, err)
	require.Len(t, releases, 2)
	assert.Equal(t, "4.22.0-0.nightly-2026-10-04-051631", releases[0].Version)
	assert.Equal(t, "registry.ci.openshift.org/ocp/release:4.22.0-0.nightly-2026-10-01-110840", releases[1].Payload)
}

func TestReleaseControllerErrors(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"unknown stream", "", 404},
		{"outage", "", 503},
		{"invalid JSON", `{`, 200},
		{"missing tags", `{"name":"stream"}`, 200},
		{"invalid tags", `{"name":"stream","tags":{}}`, 200},
		{"graph response", `{"nodes":[]}`, 200},
		{"wrong stream", `{"name":"other","tags":[]}`, 200},
		{"missing payload", `{"name":"stream","tags":[{"name":"4.22.1","phase":"Accepted"}]}`, 200},
		{"missing name", `{"name":"stream","tags":[{"pullSpec":"image","phase":"Accepted"}]}`, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(tc.status); fmt.Fprint(w, tc.body) }))
			defer server.Close()
			source, err := NewReleaseControllerClient(server.URL)
			require.NoError(t, err)
			_, err = source.ListReleases(context.Background(), "stream")
			require.Error(t, err)
		})
	}
}

func TestReleaseControllerConfigurationAndCancellation(t *testing.T) {
	for _, endpoint := range []string{"", "localhost", "ftp://host", "https://host?query=x", "https://host/#fragment"} {
		_, err := NewReleaseControllerClient(endpoint)
		require.Error(t, err)
	}
	source, err := NewReleaseControllerClient("https://example.invalid")
	require.NoError(t, err)
	for _, stream := range []string{"", "..", "foo/bar", "foo?bar"} {
		_, err := source.ListReleases(context.Background(), stream)
		require.Error(t, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = source.ListReleases(ctx, "stream")
	require.ErrorIs(t, err, context.Canceled)
}

func TestReleaseControllerStreamDiscovery(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/prefix/api/v1/releasestreams/all", r.URL.Path)
		assert.Empty(t, r.URL.RawQuery)
		assert.Equal(t, "application/json", r.Header.Get("Accept"))
		fmt.Fprint(w, `{"5-stable":null,"4.23.0-0.nightly":[],"4-stable":["4.23.1"]}`)
	}))
	defer server.Close()
	source, err := NewReleaseControllerClient(server.URL + "/prefix")
	require.NoError(t, err)
	streams, err := source.ListStreams(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{"4-stable", "4.23.0-0.nightly", "5-stable"}, streams)
}

func TestReleaseControllerStreamDiscoveryErrors(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"outage", "", 503}, {"missing endpoint", "", 404},
		{"invalid JSON", "{", 200}, {"null index", "null", 200},
		{"array instead of map", "[]", 200}, {"malformed entries", `{"4-stable":42}`, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(tc.status); fmt.Fprint(w, tc.body) }))
			defer server.Close()
			source, err := NewReleaseControllerClient(server.URL)
			require.NoError(t, err)
			_, err = source.ListStreams(context.Background())
			require.Error(t, err)
		})
	}
}

func TestStreamMatchesChannel(t *testing.T) {
	for _, tc := range []struct {
		stream, channel string
		major, minor    int
		want            bool
	}{
		{"4-stable", "stable", 4, 23, true}, {"5-stable", "stable", 4, 23, true},
		{"4-stable", "stable", 5, 0, false}, {"4-stable-scos", "stable", 4, 23, false},
		{"4.23.0-0.nightly", "nightly", 4, 23, true}, {"5.0.0-0.nightly", "nightly", 4, 23, true},
		{"4.22.0-0.nightly", "nightly", 4, 23, false}, {"5.0.0-0.nightly-art23398", "nightly", 4, 23, false},
		{"4.23.0-0.ci", "nightly", 4, 23, false}, {"5.0.0-0.ci", "ci", 5, 0, true},
		{"5-dev-preview", "dev-preview", 5, 0, true}, {"6.0.0-0.custom", "custom", 5, 0, true},
		{"05-stable", "stable", 4, 23, false}, {"5.00.0-0.nightly", "nightly", 5, 0, false},
		{"5.0.1-0.nightly", "nightly", 5, 0, false}, {"5.10.0-0.nightly", "nightly", 5, 9, true},
		{"5.9.0-0.nightly", "nightly", 5, 10, false},
	} {
		t.Run(tc.stream+"/"+tc.channel, func(t *testing.T) {
			assert.Equal(t, tc.want, streamMatchesChannel(tc.stream, tc.channel, tc.major, tc.minor))
		})
	}
}
