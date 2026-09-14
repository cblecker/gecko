package v1

import (
	"regexp"
	"strings"
	"unicode"

	"k8s.io/apimachinery/pkg/types"
)

const (
	// MaxSafeNameLength keeps clusters-<uuid>-<safeName> within Kubernetes' 63-character namespace limit.
	MaxSafeNameLength = 17
	safeNamePrefix    = "hc-"
)

var repeatedHyphenRE = regexp.MustCompile(`-+`)

// SafeNameFromClusterName returns a DNS-safe internal name derived from the cluster name.
func SafeNameFromClusterName(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case unicode.IsDigit(r):
			b.WriteRune(r)
		case r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}

	safeName := repeatedHyphenRE.ReplaceAllString(b.String(), "-")
	safeName = strings.Trim(safeName, "-")
	if len(safeName) > MaxSafeNameLength {
		safeName = safeName[:MaxSafeNameLength]
		safeName = strings.TrimRight(safeName, "-")
	}
	return safeName
}

// DefaultSafeName returns a DNS-safe internal name, falling back to the cluster UID if needed.
func DefaultSafeName(name string, uid types.UID) string {
	safeName := SafeNameFromClusterName(name)
	if safeName != "" {
		return safeName
	}

	uidPart := strings.ReplaceAll(string(uid), "-", "")
	if len(uidPart) > MaxSafeNameLength-len(safeNamePrefix) {
		uidPart = uidPart[:MaxSafeNameLength-len(safeNamePrefix)]
	}
	uidPart = SafeNameFromClusterName(uidPart)
	if uidPart == "" {
		uidPart = "cluster"
	}
	return safeNamePrefix + uidPart
}
