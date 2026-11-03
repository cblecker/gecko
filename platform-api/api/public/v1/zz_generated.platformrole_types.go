package v1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// PlatformRole is a cluster-scoped, system-managed role. PlatformRoles are
// seeded by Helm. Every authenticated user can discover their names and complete
// permission sets through the read-only public API.
//
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +orlop:authorization-exempt-verbs: get,list
type PlatformRole struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec describes the permission set included in this platform role.
	// +required
	Spec PlatformRoleSpec `json:"spec"`
}

// PlatformRoleList is a list of PlatformRole resources.
//
// +kubebuilder:object:root=true
type PlatformRoleList struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ListMeta `json:"metadata,omitempty"`

	// items contains the platform roles returned by this list.
	// +optional
	Items []PlatformRole `json:"items"`
}

// PlatformRoleSpec describes the permissions granted by a PlatformRole.
type PlatformRoleSpec struct {

	// permissions is the complete set of permissions included in this role.
	// +kubebuilder:validation:MinItems=1
	// +listType=set
	// +required
	Permissions []string `json:"permissions"`
}

func init() {
	register(&PlatformRole{}, &PlatformRoleList{})
}
