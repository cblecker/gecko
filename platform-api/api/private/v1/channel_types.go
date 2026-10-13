package v1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +orlop:public-verbs: get,list
// +orlop:authorization-exempt-verbs: get,list
// +kubebuilder:subresource:status
// Channel provides clients with the default version for cluster installation
// and platform controls for supported releases and automatic fleet upgrades.
// Channel resources are managed by the platform and are read-only to end users.
type Channel struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +orlop:public
	// +required
	Spec ChannelSpec `json:"spec"`

	// +orlop:public
	// +optional
	Status ChannelStatus `json:"status,omitempty,omitzero"`
}

// +kubebuilder:object:root=true
type ChannelList struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ListMeta `json:"metadata,omitempty"`

	// +orlop:public
	Items []Channel `json:"items"`
}

// ChannelSpec contains platform-managed controls for a channel group.
type ChannelSpec struct {
	// MinimumSupportedVersion is the oldest major.minor release line included
	// in this Channel's version catalog, for example 4.22 or 4.24. It is independent
	// of fleet upgrade authorization and the pinned installation default.
	// +required
	// +kubebuilder:validation:Pattern=`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`
	MinimumSupportedVersion string `json:"minimumSupportedVersion"`

	// InstallDefaultVersion is the exact release selected when a client does
	// not provide a version during cluster creation.
	// +orlop:public
	// +required
	// +kubebuilder:validation:MinLength=1
	InstallDefaultVersion string `json:"installDefaultVersion"`

	// FleetMinorVersion is the major.minor release line approved for automatic
	// fleet upgrades.
	// +required
	// +kubebuilder:validation:Pattern=`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`
	FleetMinorVersion string `json:"fleetMinorVersion"`
}

// ChannelDefaultVersionAvailable reports whether the pinned install default is
// present in this Channel's latest successfully synchronized release catalog.
// True means present, False means absent after a successful sync, and Unknown
// means availability could not be determined. A missing condition is unevaluated.
// Consumers must check observedGeneration after spec changes. An unavailable
// default does not prevent synchronization of other releases.
// This condition is private: it is not in Orlop's public-condition allowlist.
const ChannelDefaultVersionAvailable = "DefaultVersionAvailable"

// ChannelStatus contains observations made by the version-sync controller.
type ChannelStatus struct {
	// Conditions contains observations made by the version-sync controller.
	// Individual condition types are private unless explicitly allowlisted by
	// Orlop for exposure in public API responses.
	// +orlop:public
	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

func init() { register(&Channel{}, &ChannelList{}) }
