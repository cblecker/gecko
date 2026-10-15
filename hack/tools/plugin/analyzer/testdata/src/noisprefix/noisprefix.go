package noisprefix

// IsPrefixBad names a bool field with an Is prefix — must be reported.
type IsPrefixBad struct {
	// isEnabled controls the feature.
	// +optional
	IsEnabled bool `json:"isEnabled,omitempty"` // want `bool field .* must not start with .Is.`
}

// IsPrefixGood names a bool field without the Is prefix — must not be reported.
type IsPrefixGood struct {
	// enabled controls the feature.
	// +optional
	Enabled bool `json:"enabled,omitempty"`
}

// IsNotBoolOk has an Is-prefix field that is not bool — must not be reported.
type IsNotBoolOk struct {
	// isName is a string, not bool.
	// +optional
	IsName string `json:"isName,omitempty"`
}
