package prefmarkers

// DeprecatedDefaultMarker uses +kubebuilder:default instead of +default.
type DeprecatedDefaultMarker struct {
	// diskSizeGB is the disk size.
	// +kubebuilder:default=64
	DiskSizeGB int32 `json:"diskSizeGB,omitempty"` // want "use .default.64 instead of .kubebuilder:default.64"
}

// GoodDefaultMarker uses the standard +default — must not be reported.
type GoodDefaultMarker struct {
	// diskSizeGB is the disk size.
	// +default=64
	DiskSizeGB int32 `json:"diskSizeGB,omitempty"`
}

// DeprecatedOptionalMarker uses +kubebuilder:validation:Optional instead of +optional.
type DeprecatedOptionalMarker struct {
	// name is optional.
	// +kubebuilder:validation:Optional
	Name string `json:"name,omitempty"` // want "kubebuilder:validation:Optional"
}

// GoodOptionalMarker uses the standard +optional — must not be reported.
type GoodOptionalMarker struct {
	// name is optional.
	// +optional
	Name string `json:"name,omitempty"`
}

// DeprecatedDefaultLiteralArgMarker uses +kubebuilder:default:= (literal-arg form).
type DeprecatedDefaultLiteralArgMarker struct {
	// diskSizeGB is the disk size.
	//+kubebuilder:default:=64
	DiskSizeGB int32 `json:"diskSizeGB,omitempty"` // want "use .default.64 instead"
}

// DeprecatedNoSpaceMarker uses //+kubebuilder:validation:Optional (no space after //).
type DeprecatedNoSpaceMarker struct {
	//+kubebuilder:validation:Optional
	Name string `json:"name,omitempty"` // want "kubebuilder:validation:Optional"
}
