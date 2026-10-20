package p

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ── requirednoomitempty ───────────────────────────────────────────────────────

type RequiredOmitemptyBad struct {
	// name is the resource name.
	// +required
	Name string `json:"name,omitempty"` // want `requirednoomitempty`
}

type RequiredNoOmitemptyGood struct {
	// name is the resource name.
	// +required
	Name string `json:"name"`
}

type OptionalOmitemptyGood struct {
	// name is the resource name.
	// +optional
	Name string `json:"name,omitempty"`
}

// ── noimmutablemarker ─────────────────────────────────────────────────────────

type ImmutableBad struct {
	// clusterID is immutable.
	// +immutable
	ClusterID string `json:"clusterID,omitempty"` // want `noimmutablemarker`
}

type ImmutableGood struct {
	// clusterID is immutable.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="clusterID is immutable"
	ClusterID string `json:"clusterID,omitempty"`
}

// ── preferstandardmarkers ─────────────────────────────────────────────────────

type DeprecatedDefaultMarker struct {
	// diskSizeGB is the disk size.
	// +kubebuilder:default=64
	DiskSizeGB int32 `json:"diskSizeGB,omitempty"` // want `preferstandardmarkers`
}

type GoodDefaultMarker struct {
	// diskSizeGB is the disk size.
	// +default=64
	DiskSizeGB int32 `json:"diskSizeGB,omitempty"`
}

type DeprecatedOptionalMarker struct {
	// name is optional.
	// +kubebuilder:validation:Optional
	Name string `json:"name,omitempty"` // want `preferstandardmarkers`
}

type GoodOptionalMarker struct {
	// name is optional.
	// +optional
	Name string `json:"name,omitempty"`
}

// ── noisprefixbool ────────────────────────────────────────────────────────────

type IsPrefixBad struct {
	// isEnabled controls the feature.
	// +optional
	IsEnabled bool `json:"isEnabled,omitempty"` // want `noisprefixbool`
}

type IsPrefixGood struct {
	// enabled controls the feature.
	// +optional
	Enabled bool `json:"enabled,omitempty"`
}

// ── timestampnaming ───────────────────────────────────────────────────────────

type TimestampAtBad struct {
	// decidedAt is when the decision was made.
	// +optional
	DecidedAt *metav1.Time `json:"decidedAt,omitempty"` // want `timestampnaming`
}

type TimestampStampBad struct {
	// creationStamp is the creation time.
	// +optional
	CreationStamp *metav1.Time `json:"creationStamp,omitempty"` // want `timestampnaming`
}

type TimestampTimestampBad struct {
	// creationTimestamp is the creation time.
	// +optional
	CreationTimestamp metav1.Time `json:"creationTimestamp,omitempty"` // want `timestampnaming`
}

type TimestampGood struct {
	// decidedTime is when the decision was made.
	// +optional
	DecidedTime *metav1.Time `json:"decidedTime,omitempty"`
}
