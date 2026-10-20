package reqomitempty

// RequiredOmitemptyBad has +required and omitempty — must be reported.
type RequiredOmitemptyBad struct {
	// name is the resource name.
	// +required
	Name string `json:"name,omitempty"` // want "required field must not have omitempty"
}

// RequiredNoOmitemptyGood has +required without omitempty — must not be reported.
type RequiredNoOmitemptyGood struct {
	// name is the resource name.
	// +required
	Name string `json:"name"`
}

// OptionalOmitemptyGood has +optional with omitempty — must not be reported.
type OptionalOmitemptyGood struct {
	// name is the resource name.
	// +optional
	Name string `json:"name,omitempty"`
}
