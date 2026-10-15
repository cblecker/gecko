package scalarbounds

// ── string fields ─────────────────────────────────────────────────────────────

// NoMaxLengthBad has a string field without MaxLength — must be reported.
type NoMaxLengthBad struct {
	// name is the resource name.
	// +optional
	Name string `json:"name,omitempty"` // want "missing.*MaxLength"
}

// MaxLengthGood has a string field with MaxLength — must not be reported.
type MaxLengthGood struct {
	// name is the resource name.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name,omitempty"`
}

// MinLengthOnlyBad has MinLength but not MaxLength — must be reported.
type MinLengthOnlyBad struct {
	// name is the resource name.
	// +optional
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name,omitempty"` // want "missing.*MaxLength"
}

// ── numeric fields ────────────────────────────────────────────────────────────

// NoNumericBoundsBad has an int32 field with no bounds at all — must be reported.
type NoNumericBoundsBad struct {
	// count is a counter.
	// +optional
	Count int32 `json:"count,omitempty"` // want "missing.*Minimum.*Maximum"
}

// MinimumOnlyGood has only Minimum — that satisfies the "at least one" requirement.
type MinimumOnlyGood struct {
	// count is a counter.
	// +optional
	// +kubebuilder:validation:Minimum=0
	Count int32 `json:"count,omitempty"`
}

// MaximumOnlyGood has only Maximum — also satisfies the requirement.
type MaximumOnlyGood struct {
	// count is a counter.
	// +optional
	// +kubebuilder:validation:Maximum=100
	Count int64 `json:"count,omitempty"`
}

// BothBoundsGood has both Minimum and Maximum.
type BothBoundsGood struct {
	// count is a counter.
	// +optional
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=1000
	Count int32 `json:"count,omitempty"`
}

// ── slice fields ──────────────────────────────────────────────────────────────

// NoMaxItemsBad has a slice without MaxItems — must be reported.
type NoMaxItemsBad struct {
	// tags is a list of tags.
	// +optional
	// +listType=set
	Tags []string `json:"tags,omitempty"` // want "missing.*MaxItems"
}

// MaxItemsGood has MaxItems — must not be reported.
type MaxItemsGood struct {
	// tags is a list of tags.
	// +optional
	// +listType=set
	// +kubebuilder:validation:MaxItems=50
	Tags []string `json:"tags,omitempty"`
}

// ── map fields ────────────────────────────────────────────────────────────────

// NoMaxPropertiesBad has a map without MaxProperties — must be reported.
type NoMaxPropertiesBad struct {
	// labels are key-value pairs.
	// +optional
	Labels map[string]string `json:"labels,omitempty"` // want "missing.*MaxProperties"
}

// MaxPropertiesGood has MaxProperties — must not be reported.
type MaxPropertiesGood struct {
	// labels are key-value pairs.
	// +optional
	// +kubebuilder:validation:MaxProperties=50
	Labels map[string]string `json:"labels,omitempty"`
}

// ── fields that must not be reported ─────────────────────────────────────────

// NoJSONTag has no json tag — not part of the serialised API, must be skipped.
type NoJSONTag struct {
	// internal is not serialised.
	Internal string
}

// DashJSONTag is explicitly excluded from serialisation.
type DashJSONTag struct {
	// omitted is excluded.
	Omitted string `json:"-"`
}

// BoolField is not a string/int/slice/map — must not be reported.
type BoolField struct {
	// enabled is a bool.
	// +optional
	Enabled bool `json:"enabled,omitempty"`
}
