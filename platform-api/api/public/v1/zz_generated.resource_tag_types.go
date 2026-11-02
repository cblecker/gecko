package v1

// GCPResourceTag selects a pre-existing Google Cloud Resource Manager tag by
// its parent, short key, and short value. The tag must be defined in the
// cluster project or an organization before the cluster is created.
// See https://cloud.google.com/resource-manager/docs/tags/tags-overview.
type GCPResourceTag struct {
	// parentID is the ID of the project or organization that defines the TagKey.
	// When omitted, the cluster's GCP project is used.
	// An explicit value must be the cluster's GCP project ID or a numeric
	// organization ID. A numeric value is treated as an organization ID; project
	// numbers are not supported.
	// An organization ID consists of 1 to 32 decimal digits and must not start with 0.
	// A project ID is 6 to 30 characters long, contains only lowercase letters, digits
	// and hyphens, starts with a letter and must not end with a hyphen.
	//

	// +optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=32
	// +kubebuilder:validation:XValidation:rule="self.matches('(^[1-9][0-9]{0,31}$)|(^[a-z][a-z0-9-]{4,28}[a-z0-9]$)')",message="parentID must be a numeric organization ID or a valid GCP project ID"
	ParentID *string `json:"parentID,omitempty"`

	// key is required and is the short name of the pre-existing Resource Manager TagKey.
	// A key is 1-63 characters, begins and ends with
	// an ASCII alphanumeric character, and may contain letters, digits, '.',
	// '_', or '-' between them.
	//

	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:XValidation:rule="self.matches('^[a-zA-Z0-9]([0-9A-Za-z_.-]{0,61}[a-zA-Z0-9])?$')",message="key must start and end with an ASCII letter or digit and contain only ASCII letters, digits, '.', '_' or '-'"
	Key string `json:"key"`

	// value is required and is the short name of the pre-existing Resource Manager TagValue for
	// key. Exactly one value for a TagKey can be attached to a resource. A value
	// is 1-63 characters, begins and ends with an ASCII alphanumeric character,
	// and may contain ASCII letters and digits, `_-.@%=+:,*#&()[]{}`, and
	// spaces between them.
	//

	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:XValidation:rule="self.matches('^[a-zA-Z0-9]([0-9A-Za-z_.@%=+:,*#&()\\\\[\\\\]{}\\\\- ]{0,61}[a-zA-Z0-9])?$')",message="value must start and end with an ASCII letter or digit and contain only ASCII letters, digits, spaces or the characters _-.@%=+:,*#&()[]{}"
	Value string `json:"value"`
}
