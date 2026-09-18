package test

import (
	v1 "github.com/openshift-online/gecko/orlop/apis/public/test/v1"
	v2 "github.com/openshift-online/gecko/orlop/apis/public/test/v2"

	"k8s.io/apimachinery/pkg/runtime"
)

// AddToSchemes may be used to add all resources defined in the project to a Scheme.
var (
	AddToSchemes runtime.SchemeBuilder = runtime.SchemeBuilder{
		v1.SchemeBuilder.AddToScheme,
		v2.SchemeBuilder.AddToScheme,
	}

	localSchemeBuilder = &AddToSchemes
)

// AddToScheme adds all core Resources to the Scheme.
func AddToScheme(s *runtime.Scheme) error {
	return AddToSchemes.AddToScheme(s)
}
