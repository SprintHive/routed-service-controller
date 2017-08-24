package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// RoutedServicePlural is the plural form of the routedservice API resource
const RoutedServicePlural = "routedservices"

// RoutedService represents the configuration of a single cron job.
type RoutedService struct {
	metav1.TypeMeta `json:",inline"`
	// Standard object's metadata.
	// More info: https://git.k8s.io/community/contributors/devel/api-conventions.md#metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty" protobuf:"bytes,1,opt,name=metadata"`
	Spec              RoutedServiceSpec   `json:"spec"`
	Status            RoutedServiceStatus `json:"status,omitempty"`
}

// RoutedServiceSpec is the specification for a RoutedService
type RoutedServiceSpec struct {
	Hosts      string  `json:"hosts"`
	Backends []Backend `json:"backends"`
}

// Backend contains details of a routed service backend
type Backend struct {
	Service string `json:"service"`
	Port    int    `json:"port"`
	Weight  int    `json:"weight"`
}

// RoutedServiceStatus represents the status of a routed service
type RoutedServiceStatus struct {
	State   RoutedServiceState `json:"state,omitempty"`
	Message string             `json:"message,omitempty"`
}

// RoutedServiceState represents the state of a routed service
type RoutedServiceState string

// RoutedServiceList is a collection of cron jobs.
type RoutedServiceList struct {
	metav1.TypeMeta `json:",inline"`

	// Standard list metadata.
	// More info: https://git.k8s.io/community/contributors/devel/api-conventions.md#metadata
	// +optional
	metav1.ListMeta `json:"metadata,omitempty" protobuf:"bytes,1,opt,name=metadata"`

	// items is the list of CronJobs.
	Items []RoutedService `json:"items" protobuf:"bytes,2,rep,name=items"`
}
