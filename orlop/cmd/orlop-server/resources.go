package main

import (
	"fmt"

	privatev1 "github.com/openshift-online/gecko/orlop/apis/private/test/v1"
	publicv1 "github.com/openshift-online/gecko/orlop/apis/public/test/v1"
	"github.com/openshift-online/gecko/orlop/pkg/apiserver"

	"k8s.io/apimachinery/pkg/runtime"
)

// getPrivateResources returns the resource definitions for the private API.
// Uses generated ResourceInfo from the private API package.
func getPrivateResources() []apiserver.ResourceInfo {
	return privatev1.GetResourceInfos()
}

// getPublicResources returns the resource definitions for the public API.
// Uses generated ResourceInfo from the public API package.
func getPublicResources() []apiserver.ResourceInfo {
	return publicv1.GetResourceInfos()
}

// getPrivateScheme creates and returns a runtime.Scheme with private API types registered.
func getPrivateScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	if err := privatev1.AddToScheme(scheme); err != nil {
		panic(fmt.Sprintf("failed to register private API types: %v", err))
	}
	return scheme
}

// getPublicScheme creates and returns a runtime.Scheme with public API types registered.
func getPublicScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	if err := publicv1.AddToScheme(scheme); err != nil {
		panic(fmt.Sprintf("failed to register public API types: %v", err))
	}
	return scheme
}
