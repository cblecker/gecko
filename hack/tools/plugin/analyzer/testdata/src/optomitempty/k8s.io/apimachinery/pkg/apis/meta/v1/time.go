// Package v1 provides a minimal stub of k8s.io/apimachinery/pkg/apis/meta/v1
// for use in analysistest testdata packages only.
package v1

import "time"

// Time is a wrapper around time.Time that serialises to and from JSON using
// RFC3339 format.
type Time struct {
	time.Time
}

// TypeMeta describes an individual object in an API response or request.
type TypeMeta struct {
	Kind       string `json:"kind,omitempty"`
	APIVersion string `json:"apiVersion,omitempty"`
}

// ObjectMeta is metadata that all persisted resources must have.
type ObjectMeta struct {
	Name      string `json:"name,omitempty"`
	Namespace string `json:"namespace,omitempty"`
}
