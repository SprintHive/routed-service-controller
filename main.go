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

	"github.com/SprintHive/go-kong/kong"
	"github.com/SprintHive/routed-service-controller/client"
	"github.com/SprintHive/routed-service-controller/controller"
	"github.com/golang/glog"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

func main() {
	var config *rest.Config
	var kubeconfig *string
	var err error
	externalAPIAccess := flag.Bool("externalapi", false, "connect to the API from outside the kubernetes cluster")
	kongAPIAddress := flag.String("kongaddress", "http://kong-admin.default:8001", "address of the kong API server")
	if home := homeDir(); home != "" {
		kubeconfig = flag.String("kubeconfig", filepath.Join(home, ".kube", "config"), "(optional) absolute path to the kubeconfig file")
	} else {
		kubeconfig = flag.String("kubeconfig", "", "absolute path to the kubeconfig file")
	}

	flag.Parse()

	if *externalAPIAccess {
		// use the current context in kubeconfig
		config, err = clientcmd.BuildConfigFromFlags("", *kubeconfig)
		if err != nil {
			panic(err.Error())
		}
	} else {
		config, err = rest.InClusterConfig()
		if err != nil {
			panic(err.Error())
		}
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		panic(err.Error())
	}

	routedServiceClient, _, err := client.NewClient(config)
	if err != nil {
		panic(err)
	}

	// Make sure the routed service kind exists
	err = client.CreateRoutedServiceResourceType(clientset)
	if err != nil && !apierrors.IsAlreadyExists(err) {
		panic(err)
	} else if !apierrors.IsAlreadyExists(err) {
		// The third party resource was just created so lets wait a bit for it to be available for us within the cluster
		glog.Info("The RoutedService thirdpartyresource did not exist and was therefore created")
		time.Sleep(time.Second * 5)
	}

	// Create Kong client
	kongClient, err := kong.NewClient(nil, *kongAPIAddress)

	controller := controller.New(routedServiceClient, kongClient)

	ctx, cancelFunc := context.WithCancel(context.Background())
	defer cancelFunc()
	go controller.Run(ctx)

	<-ctx.Done()
}

func homeDir() string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	return os.Getenv("USERPROFILE") // windows
}
