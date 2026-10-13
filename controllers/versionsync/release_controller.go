package versionsync

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/openshift-online/gecko/controllers/versionresolution"
)

// ReleaseControllerClient discovers CI streams and reads their accepted releases.
// The endpoint selects the architecture, for example the amd64 release controller.
// Graph nodes cannot be used here: the CI graph includes nodes from other streams.
type ReleaseControllerClient struct {
	baseURL    string
	httpClient *http.Client
}

func NewReleaseControllerClient(endpoint string) (*ReleaseControllerClient, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("release-controller endpoint must be an HTTP(S) base URL without query or fragment")
	}
	return &ReleaseControllerClient{baseURL: strings.TrimRight(endpoint, "/"), httpClient: &http.Client{Timeout: 30 * time.Second}}, nil
}

// ListStreams discovers active streams. The index includes streams with no tags,
// allowing the controller to distinguish an empty stream from a missing match.
func (c *ReleaseControllerClient) ListStreams(ctx context.Context) ([]string, error) {
	var streams map[string][]string
	if err := c.get(ctx, "/api/v1/releasestreams/all", &streams); err != nil {
		return nil, err
	}
	if streams == nil {
		return nil, fmt.Errorf("release-controller returned a null stream index")
	}
	names := make([]string, 0, len(streams))
	for name := range streams {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// streamMatchesChannel recognizes the source's major-wide and per-minor naming
// conventions without a built-in list of Channel names. Exact suffixes exclude
// variants such as nightly-art23398 when the Channel is named nightly.
func streamMatchesChannel(stream, channel string, minimumMajor, minimumMinor int) bool {
	if prefix, found := strings.CutSuffix(stream, "-"+channel); found {
		major, err := strconv.Atoi(prefix)
		if err == nil && strconv.Itoa(major) == prefix && major >= minimumMajor {
			return true
		}
	}
	prefix, found := strings.CutSuffix(stream, ".0-0."+channel)
	if !found {
		return false
	}
	major, minor, valid := parseMajorMinor(prefix)
	return valid && major >= 0 && minor >= 0 && fmt.Sprintf("%d.%d", major, minor) == prefix &&
		(major > minimumMajor || (major == minimumMajor && minor >= minimumMinor))
}

func (c *ReleaseControllerClient) get(ctx context.Context, path string, result any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("create release-controller request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		// url.Error includes the configured endpoint, which may be internal.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("fetch release-controller %q: %w", path, ctxErr)
		}
		return fmt.Errorf("fetch release-controller %q: transport error (%T)", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("release-controller %q returned HTTP %d", path, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(result); err != nil {
		return fmt.Errorf("decode release-controller %q: %w", path, err)
	}
	return nil
}

// ListReleases uses the unpaginated tags endpoint, not the latest-release endpoint.
func (c *ReleaseControllerClient) ListReleases(ctx context.Context, stream string) ([]versionresolution.ReleaseInfo, error) {
	if stream == "" || strings.ContainsAny(stream, "/?#") || stream == "." || stream == ".." {
		return nil, fmt.Errorf("invalid release stream %q", stream)
	}
	var result struct {
		Name string          `json:"name"`
		Tags json.RawMessage `json:"tags"`
	}
	if err := c.get(ctx, "/api/v1/releasestream/"+url.PathEscape(stream)+"/tags?phase=Accepted", &result); err != nil {
		return nil, err
	}
	if result.Name != stream {
		return nil, fmt.Errorf("requested stream %q, received %q", stream, result.Name)
	}
	var tags []struct {
		Name     string `json:"name"`
		Phase    string `json:"phase"`
		PullSpec string `json:"pullSpec"`
	}
	if err := json.Unmarshal(result.Tags, &tags); err != nil {
		return nil, fmt.Errorf("decode tags for stream %q: %w", stream, err)
	}
	releases := make([]versionresolution.ReleaseInfo, 0, len(tags))
	for _, tag := range tags {
		// Check the phase even when the server was asked to filter it.
		if tag.Phase != "Accepted" {
			continue
		}
		if tag.Name == "" || tag.PullSpec == "" {
			return nil, fmt.Errorf("accepted tag in stream %q has no name or pull spec", stream)
		}
		releases = append(releases, versionresolution.ReleaseInfo{Version: tag.Name, Payload: tag.PullSpec})
	}
	return releases, nil
}
