package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	// SchemeBuilder enables the registration of new types on a scheme
	SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)
	// AddToScheme adds new types from the SchemeBuilder to a scheme
	AddToScheme = SchemeBuilder.AddToScheme
	// GroupName is the owner of the resources to be registered
	GroupName = "sprinthive.com"
	// SchemaGroupVersion is the group version used to register these objects
	SchemaGroupVersion = schema.GroupVersion{Group: GroupName, Version: "v1alpha1"}
)

func addKnownTypes(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(SchemaGroupVersion,
		&RoutedService{},
		&RoutedServiceList{},
	)
	metav1.AddToGroupVersion(scheme, SchemaGroupVersion)
	return nil
}
