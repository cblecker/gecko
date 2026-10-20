package tsname

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// TimestampAtBad uses "At" suffix for a metav1.Time field — must be reported.
type TimestampAtBad struct {
	// decidedAt is when the decision was made.
	// +optional
	DecidedAt *metav1.Time `json:"decidedAt,omitempty"` // want `metav1.Time field .* must end with .Time.`
}

// TimestampStampBad uses "Stamp" suffix — must be reported.
type TimestampStampBad struct {
	// creationStamp is the creation time.
	// +optional
	CreationStamp *metav1.Time `json:"creationStamp,omitempty"` // want `metav1.Time field .* must end with .Time.`
}

// TimestampTimestampBad uses "Timestamp" suffix — must be reported.
type TimestampTimestampBad struct {
	// creationTimestamp is the creation time.
	// +optional
	CreationTimestamp metav1.Time `json:"creationTimestamp,omitempty"` // want `metav1.Time field .* must end with .Time.`
}

// TimestampTimestampOverlapsStamp uses "Timestamp" which also contains "Stamp".
// Must produce exactly one diagnostic with the correct suggestion "CreationTime"
// (not two diagnostics, and not the bogus "CreationTimeTime" from matching
// "Stamp" before "Timestamp").
type TimestampTimestampOverlapsStamp struct {
	// creationTimestamp overlaps "Stamp"; the longest suffix must win.
	// +optional
	CreationTimestamp *metav1.Time `json:"creationTimestamp,omitempty"` // want `use "CreationTime" instead`
}

// TimestampGood uses the correct "Time" suffix — must not be reported.
type TimestampGood struct {
	// decidedTime is when the decision was made.
	// +optional
	DecidedTime *metav1.Time `json:"decidedTime,omitempty"`
}
