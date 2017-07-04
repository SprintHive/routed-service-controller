package client

import (
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"

	"github.com/SprintHive/routed-service-controller/apis/routedservice/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime/serializer"
)

// NewClient returns a REST client for the routed service API
func NewClient(configTemplate *rest.Config) (*rest.RESTClient, *runtime.Scheme, error) {
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		return nil, nil, err
	}

	config := *configTemplate
	config.GroupVersion = &v1alpha1.SchemaGroupVersion
	config.APIPath = "/apis"
	config.ContentType = runtime.ContentTypeJSON
	config.NegotiatedSerializer = serializer.DirectCodecFactory{CodecFactory: serializer.NewCodecFactory(scheme)}

	client, err := rest.RESTClientFor(&config)
	if err != nil {
		return nil, nil, err
	}

	return client, scheme, nil
}
