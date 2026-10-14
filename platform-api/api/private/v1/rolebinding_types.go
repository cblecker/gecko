package v1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// RoleBinding binds an email principal to a PlatformRole or a namespace Role.
// PlatformRole is cluster-scoped, but the binding itself remains scoped to the
// namespace in which access is granted.
//
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced
type RoleBinding struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +orlop:public
	// +required
	Spec RoleBindingSpec `json:"spec"`
}

// RoleBindingList is a list of RoleBinding resources.
//
// +kubebuilder:object:root=true
type RoleBindingList struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ListMeta `json:"metadata,omitempty"`

	// +orlop:public
	// +optional
	Items []RoleBinding `json:"items"`
}

// RoleBindingSpec identifies the principal and role granted in a namespace.
type RoleBindingSpec struct {
	// +orlop:public
	// +kubebuilder:validation:MinLength=1
	// +required
	Subject string `json:"subject"`

	// +orlop:public
	// +required
	RoleRef RoleRef `json:"roleRef"`

	// condition is an optional Cedar expression that further limits the
	// resources this binding grants access to.
	// +orlop:public
	// +optional
	// +kubebuilder:validation:MaxLength=4096
	Condition *string `json:"condition,omitempty"`
}

// RoleRef identifies either a cluster-scoped PlatformRole or a namespaced
// Role. APIGroup is explicit so references cannot silently cross API groups.
type RoleRef struct {
	// +orlop:public
	// +required
	Kind string `json:"kind"`

	// +orlop:public
	// +required
	Name string `json:"name"`

	// +orlop:public
	// +required
	APIGroup string `json:"apiGroup"`
}

func init() {
	register(&RoleBinding{}, &RoleBindingList{})
}
