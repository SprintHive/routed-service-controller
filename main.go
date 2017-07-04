package main

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"io/ioutil"

	"github.com/SprintHive/go-kong/kong"
	"github.com/SprintHive/routed-service-controller/client"
	"github.com/SprintHive/routed-service-controller/controller"
	"github.com/golang/glog"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

func main() {
	var config *rest.Config
	var kubeConfig *string
	var err error
	externalAPIAccess := flag.Bool("externalapi", false, "connect to the API from outside the kubernetes cluster")
	kongAPIAddress := flag.String("kongaddress", "http://kong-admin:8001", "address of the kong API server")
	namespace := flag.String("namespace", "", "The Kubernetes namespace in which this controller will watch for changes")
	if home := homeDir(); home != "" {
		kubeConfig = flag.String("kubeconfig", filepath.Join(home, ".kube", "config"), "(optional) absolute path to the kubeconfig file")
	} else {
		kubeConfig = flag.String("kubeconfig", "", "absolute path to the kubeconfig file")
	}

	flag.Parse()

	if *externalAPIAccess {
		if len(*namespace) == 0 {
			panic("Namespace flag is required when running outside of Kubernetes")
		}

		// use the current context in kubeConfig
		config, err = clientcmd.BuildConfigFromFlags("", *kubeConfig)
		if err != nil {
			panic(err.Error())
		}
	} else {
		config, err = rest.InClusterConfig()
		if err != nil {
			panic(err.Error())
		}

		// Determine current namespace if it was not explicitly specified
		if len(*namespace) == 0 {
			namespaceBytes, err := ioutil.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace")
			if err != nil {
				panic(err.Error())
			}

			*namespace = string(namespaceBytes)
		}
	}

	clientSet, err := kubernetes.NewForConfig(config)
	if err != nil {
		panic(err.Error())
	}

	routedServiceClient, _, err := client.NewClient(config)
	if err != nil {
		panic(err.Error())
	}

	// Make sure the routed service kind exists
	err = client.CreateRoutedServiceResourceType(clientSet)
	if err == nil {
		// The third party resource was just created so lets wait a bit for it to be available for us within the cluster
		glog.Info("The RoutedService thirdpartyresource did not exist and was therefore created")
		time.Sleep(time.Second * 5)
	} else if !apierrors.IsAlreadyExists(err) {
		panic(err.Error())
	}

	// Create Kong client
	kongClient, err := kong.NewClient(nil, *kongAPIAddress)
	if err != nil {
		panic(err.Error())
	}

	rsController := controller.New(routedServiceClient, kongClient, *namespace)

	ctx := context.Background()
	go rsController.Run(ctx)

	<-ctx.Done()
}

func homeDir() string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	return os.Getenv("USERPROFILE") // windows
}
