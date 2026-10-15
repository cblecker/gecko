// Package v1 provides a minimal stub of k8s.io/apimachinery/pkg/apis/meta/v1
// for use in analysistest testdata packages only.
package v1

import "time"

// Time is a wrapper around time.Time that serialises to and from JSON using
// RFC3339 format.
type Time struct {
	time.Time
}
