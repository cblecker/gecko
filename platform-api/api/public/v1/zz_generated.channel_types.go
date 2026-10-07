package v1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +orlop:authorization-exempt-verbs: get,list
// +kubebuilder:subresource:status
// Channel provides clients with the default version for cluster installation
// and platform controls for supported releases and automatic fleet upgrades.
// Channel resources are managed by the platform and are read-only to end users.
type Channel struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +required
	Spec ChannelSpec `json:"spec"`

	// +optional
	Status ChannelStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type ChannelList struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []Channel `json:"items"`
}

// ChannelSpec contains platform-managed controls for a channel group.
type ChannelSpec struct {

	// InstallDefaultVersion is the exact release selected when a client does
	// not provide a version during cluster creation.

	// +required
	// +kubebuilder:validation:MinLength=1
	InstallDefaultVersion string `json:"installDefaultVersion"`
}

// ChannelStatus contains observations made by the version-sync controller.
type ChannelStatus struct {
	// Conditions contains observations made by the version-sync controller.
	// Individual condition types are private unless explicitly allowlisted by
	// Orlop for exposure in public API responses.

	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

func init() { register(&Channel{}, &ChannelList{}) }
