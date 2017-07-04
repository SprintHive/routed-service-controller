package controller

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"time"

	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/tools/cache"

	"github.com/SprintHive/go-kong/kong"
	"github.com/SprintHive/routed-service-controller/apis/routedservice/v1alpha1"
	"github.com/golang/glog"
	"github.com/pkg/errors"
)

// LookupHost resolves a hostname to an array of IP Addresses
type LookupHost func(host string) (addrs []string, err error)

// RoutedServiceController watches routed service updates and makes corresponding changes to the service proxy
type RoutedServiceController struct {
	RoutedServiceClient cache.Getter
	KongClient          *kong.Client
	LookupHost          LookupHost
	Namespace           string
}

// New returns an instance of a RoutedServiceController
func New(client cache.Getter, kongClient *kong.Client, namespace string) *RoutedServiceController {
	return &RoutedServiceController{
		client,
		kongClient,
		net.LookupHost,
		namespace,
	}
}

// Matches upstream urls in the format http://upstreamName and has a group for the part after http://
var upstreamNameRe = regexp.MustCompile("^.*//(.*)+$")

// FullResyncInterval determines how often a a full reconciliation of the kong and routed service configurations is done
var FullResyncInterval = time.Minute

// Run starts the RoutedServiceController
func (controller *RoutedServiceController) Run(ctx context.Context) error {
	glog.Infof("Starting watch for RoutedService updates in namespace '%s'", controller.Namespace)

	_, err := controller.createWatches(ctx)
	if err != nil {
		return errors.Wrap(err, "Failed to register watchers for RoutedService resources")
	}

	go apiReaper(ctx, controller)

	<-ctx.Done()
	return ctx.Err()
}

func apiReaper(ctx context.Context, controller *RoutedServiceController) {
	glog.Info("Reaper: watching for orphaned apis to kill")

	for {
		glog.V(2).Info("Reaper: Looking for orphaned apis to kill...")
		select {
		case <-ctx.Done():
			return
		default:
			err := reapOrphanedApis(controller.KongClient, controller.RoutedServiceClient, controller.Namespace)
			if err != nil {
				glog.Errorf("Failed to reap orphaned kong apis: %v", err)
			}
		}

		glog.V(2).Info("Reaper: Finshed reap cycle")
		time.Sleep(FullResyncInterval)
	}
}

func reapOrphanedApis(kongClient *kong.Client, routedServiceClient cache.Getter, namespace string) error {
	kongApis, _, err := kongClient.Apis.GetAll(nil)
	if err != nil {
		return errors.Wrapf(err, "Failed to get kong api list")
	}

	routedServiceObjects, err := routedServiceClient.
		Get().
		Namespace(namespace).
		Resource(v1alpha1.RoutedServicePlural).
		Do().
		Get()
	if err != nil {
		return errors.Wrapf(err, "Failed to get routed services list")
	}

	routedServiceList := routedServiceObjects.(*v1alpha1.RoutedServiceList)
	rsMap := map[string]bool{}
	for _, routedService := range routedServiceList.Items {
		rsMap[routedService.ObjectMeta.Name] = true
	}

	for _, api := range kongApis.Data {
		if !rsMap[api.Name] {
			err := deleteKongAPI(kongClient, api.Name)
			if err != nil {
				glog.Errorf("Error reaping orphaned kong api '%s': %v", api.Name, err)
			} else {
				glog.Infof("Reaper: Die, die, die! Orphaned kong api '%s' was reaped", api.Name)
			}
		}
	}

	return nil
}

func (controller *RoutedServiceController) createWatches(ctx context.Context) (cache.Controller, error) {
	watchedSource := cache.NewListWatchFromClient(
		controller.RoutedServiceClient,
		v1alpha1.RoutedServicePlural,
		controller.Namespace,
		fields.Everything())

	_, informController := cache.NewInformer(
		watchedSource,
		&v1alpha1.RoutedService{},
		FullResyncInterval,
		cache.ResourceEventHandlerFuncs{
			AddFunc:    routedServiceChanged(controller.KongClient, controller.LookupHost),
			UpdateFunc: routedServiceUpdated(controller.KongClient, controller.LookupHost),
			DeleteFunc: routedServiceDeleted(controller.KongClient),
		},
	)

	go informController.Run(ctx.Done())
	return informController, nil
}

func routedServiceChanged(kongClient *kong.Client, lookupHost LookupHost) func(interface{}) {
	return func(obj interface{}) {
		routedService := obj.(*v1alpha1.RoutedService)
		err := validateRSWellFormed(routedService)
		if err != nil {
			glog.Errorf("RoutedService '%s' is malformed and will be skipped: %v", routedService.ObjectMeta.Name, err)
			return
		}

		glog.V(2).Infof("Reconciling RoutedService '%s' with Kong API", routedService.ObjectMeta.Name)
		// TODO: Fix this hack. Kong doesn't handle DNS properly within Kubernetes so I'm
		// bypassing this issue by resolving hostnames before adding them to Kong
		err = updateBackendTargetsToIPs(routedService, lookupHost)
		if err != nil {
			glog.Errorf("Could not resolve routed service '%s' backends to IPs: %v", routedService.ObjectMeta.Name, err)
			return
		}
		err = reconcileAPI(kongClient, routedService, lookupHost)
		if err != nil {
			glog.Errorf("An error occurred attempting to create or update API '%s': %v", routedService.ObjectMeta.Name, err)
			return
		}
	}
}

func updateBackendTargetsToIPs(routedService *v1alpha1.RoutedService, lookupHost LookupHost) error {
	for index, backend := range routedService.Spec.Backends {
		newTarget, err := getIPTarget(backend.Service, lookupHost)
		if err != nil {
			return errors.Wrapf(err, "Failed to convert target backend '%s' into an ip-based target", backend.Service)
		}
		routedService.Spec.Backends[index].Service = newTarget
		//backend.Service = newTarget
	}

	return nil
}

func reconcileAPI(kongClient *kong.Client, routedService *v1alpha1.RoutedService, lookupHost LookupHost) error {
	// First get the upstream in order
	err := reconcileUpstream(kongClient, routedService, lookupHost)
	if err != nil {
		return errors.Wrapf(err, "Failed create or update upstream")
	}

	apiName := routedService.ObjectMeta.Name

	api, resp, err := kongClient.Apis.Get(apiName)
	if err != nil && (resp == nil || resp.StatusCode != http.StatusNotFound) {
		return errors.Wrapf(err, "Failed to fetch API '%s'", apiName)
	}

	if resp.StatusCode == http.StatusNotFound {
		glog.Infof("Creating new API '%s'", apiName)
		kongAPI := apiRequestFromRoutedService(routedService)
		_, err := kongClient.Apis.Post(&kongAPI)
		if err != nil {
			return errors.Wrapf(err, "Failed to create API '%s'", apiName)
		}
	} else {
		correctUpstreamURL := getUpstreamURL(apiName)
		if api.UpstreamURL != correctUpstreamURL {
			glog.Infof("Updating upstream URL from '%s' to '%s' on API '%s'", api.UpstreamURL, correctUpstreamURL, api.Name)
			_, err := kongClient.Apis.Patch(&kong.ApiRequest{
				ID:          api.ID,
				UpstreamURL: correctUpstreamURL,
			})
			if err != nil {
				return errors.Wrapf(err, "Failed to patch API '%s'", apiName)
			}
		}
	}

	return nil
}

func reconcileUpstream(kongClient *kong.Client, routedService *v1alpha1.RoutedService, lookupHost LookupHost) error {
	upstreamName := routedService.ObjectMeta.Name
	_, resp, err := kongClient.Upstreams.Get(upstreamName)
	if err != nil && resp == nil {
		return errors.Wrapf(err, "Failed to fetch upstream '%s'", upstreamName)
	}

	if resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Unexpected response code '%v' fetching upstream '%v'", resp.StatusCode, upstreamName)
	}

	// Does the upstream need to be created?
	if resp.StatusCode == http.StatusNotFound {
		_, err = kongClient.Upstreams.Post(&kong.Upstream{
			Name: upstreamName,
		})
		if err != nil {
			return errors.Wrapf(err, "Failed to create new upstream '%s'", upstreamName)
		}
		glog.Infof("Created new upstream '%s'", upstreamName)

		return putTargets(kongClient, upstreamName, &routedService.Spec.Backends)
	}

	return updateTargets(kongClient, routedService, lookupHost)
}

func putTargets(kongClient *kong.Client, upstreamName string, backends *[]v1alpha1.Backend) error {
	for _, backend := range *backends {

		target := targetFromBackend(&backend)
		_, err := kongClient.Targets.Post(upstreamName, target)
		if err != nil {
			return errors.Wrapf(err, "Failed to create a new target for upstream '%s': %v", upstreamName, err)
		}
		glog.Infof("Created target '%s' on upstream '%s' with weight '%d'", target.Target, upstreamName, target.Weight)
	}

	return nil
}

func updateTargets(kongClient *kong.Client, routedService *v1alpha1.RoutedService, lookupHost LookupHost) error {
	upstreamName := routedService.ObjectMeta.Name
	targets, _, err := kongClient.Targets.GetAllActive(upstreamName)
	if err != nil {
		return errors.Wrapf(err, "Failed to fetch targets for upstream '%s'", upstreamName)
	}

	backendMap := map[string]*v1alpha1.Backend{}
	for index, backend := range routedService.Spec.Backends {
		backendMap[fmt.Sprintf("%s:%d", backend.Service, backend.Port)] = &routedService.Spec.Backends[index]
	}

	targetsToDelete := []*kong.Target{}
	for _, target := range targets.Data {
		if backend, ok := backendMap[target.Target]; ok {
			needsUpdate := backend.Weight != target.Weight
			glog.V(2).Infof("Found existing target '%s'. Needs update?: %v", target.Target, needsUpdate)
			if !needsUpdate {
				delete(backendMap, target.Target)
			}
		} else {
			glog.V(2).Infof("Marking target '%s' for deletion", target.Target)
			targetsToDelete = append(targetsToDelete, target)
		}
	}

	// Add or replace targets for the backends that were not already configured correctly
	remainingBackends := []v1alpha1.Backend{}
	for _, backend := range backendMap {
		remainingBackends = append(remainingBackends, *backend)
	}
	putTargets(kongClient, upstreamName, &remainingBackends)

	// Cleanup the targets that are queued for deletion
	for _, obsoleteTarget := range targetsToDelete {
		_, err := kongClient.Targets.Delete(upstreamName, obsoleteTarget.ID)
		if err != nil {
			glog.Warningf("Failed to delete obsolete target '%s': %v", obsoleteTarget.ID, err)
		} else {
			glog.Infof("Deleted target '%s' from upstream '%s'", obsoleteTarget.Target, upstreamName)
		}
	}

	return nil
}

func routedServiceUpdated(kongClient *kong.Client, lookupHost LookupHost) func(interface{}, interface{}) {
	return func(previousObj, newObj interface{}) {
		routedServiceChanged(kongClient, lookupHost)(newObj)
	}
}

func routedServiceDeleted(kongClient *kong.Client) func(interface{}) {
	return func(obj interface{}) {
		routedService := obj.(*v1alpha1.RoutedService)
		glog.Infof("Routed service '%s' was deleted. Removing it from Kong.", routedService.ObjectMeta.Name)
		err := deleteKongAPI(kongClient, routedService.ObjectMeta.Name)
		if err != nil {
			glog.Errorf("Failed to delete kong API '%s': %v", routedService.ObjectMeta.Name, err)
		}
	}
}

func deleteKongAPI(kongClient *kong.Client, apiName string) error {
	removedAPI, _, err := kongClient.Apis.Get(apiName)
	if err != nil {
		return errors.Wrapf(err, "Failed to retrieve kong api '%s'", apiName)
	}

	_, err = kongClient.Apis.Delete(apiName)
	if err != nil {
		return errors.Wrapf(err, "Failed to delete kong api '%s'", apiName)
	}
	glog.Infof("Kong api '%s' was deleted", apiName)

	// Upstreams on an API are in URL format but the upstream object names are not
	upstreamURLParts := upstreamNameRe.FindStringSubmatch(removedAPI.UpstreamURL)
	var upstreamName string
	if len(upstreamURLParts) == 2 {
		upstreamName = upstreamURLParts[1]
	} else {
		upstreamName = removedAPI.UpstreamURL
	}

	_, err = kongClient.Upstreams.Delete(upstreamName)
	if err != nil {
		return errors.Wrapf(err, "Failed to delete service upstream '%s'", upstreamName)
	}

	return nil
}

func targetFromBackend(backend *v1alpha1.Backend) *kong.Target {
	return &kong.Target{
		Target: fmt.Sprintf("%s:%d", backend.Service, backend.Port),
		Weight: backend.Weight,
	}
}

func apiRequestFromRoutedService(routedService *v1alpha1.RoutedService) kong.ApiRequest {
	serviceName := routedService.ObjectMeta.Name
	upstreamURL := getUpstreamURL(serviceName)
	return kong.ApiRequest{
		UpstreamURL: upstreamURL,
		Name:        serviceName,
		Hosts:       serviceName,
	}
}

func getUpstreamURL(apiName string) string {
	return "http://" + apiName
}

func getIPTarget(address string, lookupHost LookupHost) (string, error) {
	var ipString string
	parsedIP := net.ParseIP(address)
	// This is already an IP, just return it
	if parsedIP != nil {
		return address, nil
	}

	hostIPs, err := lookupHost(address)
	if err != nil {
		return "", errors.Wrapf(err, "Failed to lookup host '%s'", address)
	}
	if len(hostIPs) == 0 {
		return "", errors.Wrapf(err, "Host '%s' does not resolve to an IP", address)
	}

	ipString = hostIPs[0]
	glog.V(2).Infof("Resolved target address '%s' to %s", address, ipString)

	return ipString, nil
}

func validateRSWellFormed(routedService *v1alpha1.RoutedService) error {
	if routedService.ObjectMeta.Name == "" {
		return fmt.Errorf("Name is empty")
	}
	if len(routedService.Spec.Backends) == 0 {
		return fmt.Errorf("Spec.Backends is empty")
	}
	for index, backend := range routedService.Spec.Backends {
		if backend.Service == "" {
			return fmt.Errorf("Spec.Backend[%d].Service is empty", index)
		}
		if backend.Port == 0 {
			return fmt.Errorf("Spec.Backend[%d].Port of 0 is invalid", index)
		}
	}

	return nil
}
