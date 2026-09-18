package main

import (
	"fmt"

	"github.com/openshift-online/gecko/orlop/pkg/apiserver/types"
	privatev1 "github.com/openshift-online/gecko/platform-api/api/private/v1"
	publicv1 "github.com/openshift-online/gecko/platform-api/api/public/v1"

	"k8s.io/apimachinery/pkg/runtime"
)

// getPrivateResources returns the resource definitions for the private API.
func getPrivateResources() []types.ResourceInfo {
	resources := privatev1.GetResourceInfos()
	for i := range resources {
		if resources[i].GVK.Kind == "NodePool" {
			resources[i].ParentResource = &types.ParentResourceInfo{
				Plural:    "clusters",
				GroupKind: privatev1.GroupVersion.WithKind("Cluster").GroupKind(),
				IDField:   "spec.clusterID",
			}
		}
	}
	return resources
}

// getPublicResources returns the resource definitions for the public API.
func getPublicResources() []types.ResourceInfo {
	resources := publicv1.GetResourceInfos()
	for i := range resources {
		if resources[i].GVK.Kind == "NodePool" {
			resources[i].ParentResource = &types.ParentResourceInfo{
				Plural:    "clusters",
				GroupKind: privatev1.GroupVersion.WithKind("Cluster").GroupKind(),
				IDField:   "spec.clusterID",
			}
		}
	}
	return resources
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
