package client

import (
	"github.com/SprintHive/routed-service-controller/apis/routedservice/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/pkg/apis/extensions/v1beta1"
)

// CreateRoutedServiceResourceType creates a third party resource for RoutedServices
func CreateRoutedServiceResourceType(clientSet kubernetes.Interface) error {
	resource := &v1beta1.ThirdPartyResource{
		ObjectMeta: metav1.ObjectMeta{
			Name: "routed-service." + v1alpha1.GroupName,
		},
		Versions: []v1beta1.APIVersion{
			{Name: v1alpha1.SchemaGroupVersion.Version},
		},
		Description: "A managed routed service that updates an API gateway to reflect the routed service resources",
	}

	_, err := clientSet.ExtensionsV1beta1().ThirdPartyResources().Create(resource)
	return err
}
