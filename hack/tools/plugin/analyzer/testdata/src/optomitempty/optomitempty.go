package optomitempty

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// OptionalMissingOmitemptyBad has +optional but no omitempty — must be reported.
type OptionalMissingOmitemptyBad struct {
	// name is the resource name.
	// +optional
	Name string `json:"name"` // want "optional field must have omitempty"
}

// OptionalWithOmitemptyGood has +optional with omitempty — must not be reported.
type OptionalWithOmitemptyGood struct {
	// name is the resource name.
	// +optional
	Name string `json:"name,omitempty"`
}

// RequiredNoOmitemptyNotReported has +required (not +optional) — must not be reported.
type RequiredNoOmitemptyNotReported struct {
	// name is the resource name.
	// +required
	Name string `json:"name"`
}

// NoMarkerNotReported has neither +optional nor +required — must not be reported.
type NoMarkerNotReported struct {
	// name has no optionality marker.
	Name string `json:"name"`
}

// NoJSONTagNotReported has no json tag — must not be reported.
type NoJSONTagNotReported struct {
	// internal is not serialised.
	// +optional
	Internal string
}

// EmbeddedNotReported uses embedding — must not be reported.
type EmbeddedNotReported struct {
	metav1.TypeMeta   `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`
}

// MetaTimePlainSkipped has plain metav1.Time without pointer — must not be
// reported because the serialisation of a plain struct ignores omitempty.
type MetaTimePlainSkipped struct {
	// startTime is the start time.
	// +optional
	StartTime metav1.Time `json:"startTime"`
}

// MetaTimePointerGood has *metav1.Time with omitempty — must not be reported.
type MetaTimePointerGood struct {
	// startTime is the start time.
	// +optional
	StartTime *metav1.Time `json:"startTime,omitempty"`
}

// MetaTimePointerBad has *metav1.Time without omitempty — must be reported.
type MetaTimePointerBad struct {
	// startTime is the start time.
	// +optional
	StartTime *metav1.Time `json:"startTime"` // want "optional field must have omitempty"
}
