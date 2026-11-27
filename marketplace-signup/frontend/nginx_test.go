package frontend

import (
	"regexp"
	"strings"
	"testing"
)

// A location that sets any header drops the headers of the enclosing blocks,
// so every location sets the security and caching headers itself: nosniff and
// a frame-ancestors policy (browsers ignore that directive in the page's meta
// policy) on everything, and a Cache-Control that keeps a rollout from pairing
// the page with stale assets.
func TestNginxLocationsSetSecurityAndCacheHeaders(t *testing.T) {
	conf := readFile(t, "nginx.conf")
	locations := regexp.MustCompile(`(?s)location\s+([^{]+?)\s*\{([^}]*)\}`).FindAllStringSubmatch(conf, -1)
	if len(locations) == 0 {
		t.Fatal("nginx.conf has no location blocks")
	}
	for _, location := range locations {
		for _, header := range []string{
			`X-Content-Type-Options "nosniff" always`,
			`Content-Security-Policy "frame-ancestors 'none'" always`,
			`Cache-Control "`,
		} {
			if !strings.Contains(location[2], header) {
				t.Errorf("location %s does not set %s", location[1], header)
			}
		}
	}
}
