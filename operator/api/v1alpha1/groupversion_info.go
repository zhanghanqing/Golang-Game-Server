// Package v1alpha1 contains the GameCluster API.
//
// +kubebuilder:object:generate=true
// +groupName=gameserver.dev
package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

var (
	// GroupVersion is the API group and version of GameCluster.
	GroupVersion = schema.GroupVersion{Group: "gameserver.dev", Version: "v1alpha1"}

	// SchemeBuilder registers the types of this package with a runtime.Scheme.
	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}

	// AddToScheme adds the types of this package to a scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)
