package noimmutable

// ImmutableBad uses the forbidden +immutable marker.
type ImmutableBad struct {
	// clusterID is immutable.
	// +immutable
	ClusterID string `json:"clusterID,omitempty"` // want "immutable has no defined semantics"
}

// ImmutableGood uses CEL XValidation — must not be reported.
type ImmutableGood struct {
	// clusterID is immutable.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="clusterID is immutable"
	ClusterID string `json:"clusterID,omitempty"`
}

// ImmutableTypeLevelBad has +immutable on the type declaration itself.
// +immutable
type ImmutableTypeLevelBad struct { // want "immutable has no defined semantics"
	ClusterID string `json:"clusterID,omitempty"`
}

// ImmutableNoSpaceBad uses the //+marker form (no space after //).
type ImmutableNoSpaceBad struct {
	//+immutable
	ClusterID string `json:"clusterID,omitempty"` // want "immutable has no defined semantics"
}

// ImmutableTrailingDocBad has +immutable in the field doc alongside its field
// line — exercises the field.Comment path via a doc-comment placement that
// analysistest can annotate. Pure trailing-comment placement (after the field
// on the same line) is covered by the ImmutableNoSpaceBad case above, which
// exercises the same reportImmutableInCG code path through field.Comment.
type ImmutableTrailingDocBad struct {
	// +immutable
	ClusterID string `json:"clusterID,omitempty"` // want "immutable has no defined semantics"
}
