package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/rest/fake"

	"github.com/SprintHive/go-kong/kong"
	"github.com/SprintHive/routed-service-controller/apis/routedservice/v1alpha1"
	"github.com/SprintHive/routed-service-controller/client"
)

var (
	// HTTP mux used with test server
	mux *http.ServeMux

	// Kong client being tested
	kongClient *kong.Client

	// Test server used to stub Kong resources
	server *httptest.Server

	opTimeout time.Duration

	lookupHost = func(host string) ([]string, error) { return []string{"ip-" + host}, nil }

	// Kubernetes namespace
	namespace = "default"
)

type Payload struct {
	request    interface{}
	response   interface{}
	httpMethod string
}

func TestKongUpdatedOnDeletedRoutedService(t *testing.T) {
	setup()
	defer shutdown()
	waitGroup := sync.WaitGroup{}

	serviceName := "boringservice"

	waitGroup.Add(1)
	go testAPIDeleted(t, serviceName, &waitGroup)
	waitGroup.Add(1)
	go testKongOperationCalled(t, "/upstreams/"+serviceName, http.MethodDelete, nil, nil, &waitGroup)

	// This is the method under test
	routedService := sampleRoutedService(serviceName)
	routedServiceDeleted(kongClient)(&routedService)

	waitGroup.Wait()
}

func TestKongUpdatedOnNewRoutedService(t *testing.T) {
	setup()
	defer shutdown()
	waitGroup := sync.WaitGroup{}

	serviceName := "bestservice"
	sampleRS := sampleRoutedService(serviceName)

	// Create new upstream
	waitGroup.Add(1)
	go testKongOperationCalled(t, "/upstreams", http.MethodPost, upstreamFromRS(&sampleRS), nil, &waitGroup)

	// Add the new targets
	payloads := []Payload{}
	for _, backend := range sampleRS.Spec.Backends {
		payloads = append(payloads, Payload{
			request:    ipTargetFromBackend(&backend),
			httpMethod: http.MethodPost,
		})
	}
	waitGroup.Add(1)
	go testKongOperationCalledMultiple(t, fmt.Sprintf("/upstreams/%s/targets", serviceName), payloads, &waitGroup)

	// Create API
	waitGroup.Add(1)
	go testKongOperationCalled(t, "/apis", http.MethodPost, apiRequestFromRoutedService(&sampleRS), nil, &waitGroup)

	routedServiceChanged(kongClient, lookupHost)(&sampleRS)
	waitGroup.Wait()
}

func TestKongUpdatedOnRSBackendServiceUpdate(t *testing.T) {
	setup()
	defer shutdown()

	waitGroup := sync.WaitGroup{}

	serviceName := "bestservice"
	sampleRS := sampleRoutedService(serviceName)
	kongTargets := targetsFromRS(&sampleRS)
	kongTargets.Data[0].Target = "different:80"

	updatedTarget := ipTargetFromBackend(&sampleRS.Spec.Backends[0])

	// Does the upstream exist?
	waitGroup.Add(1)
	go testKongOperationCalled(t, fmt.Sprintf("/upstreams/%s", serviceName), http.MethodGet, nil, nil, &waitGroup)
	// What are the active targets?
	waitGroup.Add(1)
	go testKongOperationCalled(t, fmt.Sprintf("/upstreams/%s/targets/active", serviceName), http.MethodGet, nil, kongTargets, &waitGroup)
	// Add the new target
	waitGroup.Add(1)
	go testKongOperationCalled(t, fmt.Sprintf("/upstreams/%s/targets", serviceName), http.MethodPost, updatedTarget, nil, &waitGroup)
	// Remove the old target
	waitGroup.Add(1)
	go testKongOperationCalled(t, fmt.Sprintf("/upstreams/%s/targets/%s", serviceName, sampleRS.Spec.Backends[0].Service), http.MethodDelete, nil, nil, &waitGroup)
	// Make sure the API upstream is correct
	waitGroup.Add(1)
	go testKongOperationCalled(t, fmt.Sprintf("/apis/%s", serviceName), http.MethodGet, nil, apiFromRS(&sampleRS), &waitGroup)

	routedServiceChanged(kongClient, lookupHost)(&sampleRS)
	waitGroup.Wait()
}
func TestKongUpdatedOnRSBackendWeightUpdate(t *testing.T) {
	setup()
	defer shutdown()

	waitGroup := sync.WaitGroup{}

	serviceName := "bestservice"
	sampleRS := sampleRoutedService(serviceName)
	kongTargets := targetsFromRS(&sampleRS)
	kongTargets.Data[0].Weight++

	updatedTarget := ipTargetFromBackend(&sampleRS.Spec.Backends[0])

	// Does the upstream exist?
	waitGroup.Add(1)
	go testKongOperationCalled(t, fmt.Sprintf("/upstreams/%s", serviceName), http.MethodGet, nil, nil, &waitGroup)
	// What are the active targets?
	waitGroup.Add(1)
	go testKongOperationCalled(t, fmt.Sprintf("/upstreams/%s/targets/active", serviceName), http.MethodGet, nil, kongTargets, &waitGroup)
	// Replace the old target
	waitGroup.Add(1)
	go testKongOperationCalled(t, fmt.Sprintf("/upstreams/%s/targets", serviceName), http.MethodPost, updatedTarget, nil, &waitGroup)
	// Make sure the API upstream is correct
	waitGroup.Add(1)
	go testKongOperationCalled(t, fmt.Sprintf("/apis/%s", serviceName), http.MethodGet, nil, apiFromRS(&sampleRS), &waitGroup)

	routedServiceChanged(kongClient, lookupHost)(&sampleRS)
	waitGroup.Wait()
}

func TestKongUpdatedOnRoutedServiceNewBackend(t *testing.T) {
	setup()
	defer shutdown()

	waitGroup := sync.WaitGroup{}

	serviceName := "bestservice"
	sampleRS := sampleRoutedService(serviceName)
	sampleRSWithMissingBackend := sampleRoutedService(serviceName)
	sampleRSWithMissingBackend.Spec.Backends = []v1alpha1.Backend{sampleRS.Spec.Backends[0]}
	kongTargets := targetsFromRS(&sampleRSWithMissingBackend)

	// Does the upstream exist?
	waitGroup.Add(1)
	go testKongOperationCalled(t, fmt.Sprintf("/upstreams/%s", serviceName), http.MethodGet, nil, nil, &waitGroup)
	// What are the active targets?
	waitGroup.Add(1)
	go testKongOperationCalled(t, fmt.Sprintf("/upstreams/%s/targets/active", serviceName), http.MethodGet, nil, kongTargets, &waitGroup)
	// Add the new target
	waitGroup.Add(1)
	go testKongOperationCalled(t, fmt.Sprintf("/upstreams/%s/targets", serviceName), http.MethodPost, ipTargetFromBackend(&sampleRS.Spec.Backends[1]), nil, &waitGroup)
	// Make sure the API upstream is correct
	waitGroup.Add(1)
	go testKongOperationCalled(t, fmt.Sprintf("/apis/%s", serviceName), http.MethodGet, nil, apiFromRS(&sampleRS), &waitGroup)

	routedServiceChanged(kongClient, lookupHost)(&sampleRS)
	waitGroup.Wait()
}

func TestKongReconciledWithNewRoutedServices(t *testing.T) {
	setup()
	defer shutdown()

	waitGroup := sync.WaitGroup{}

	sampleRS := sampleRoutedService("sneakyservice")
	routedServiceListJSON, err := objectToJSON(v1alpha1.RoutedServiceList{
		TypeMeta: metav1.TypeMeta{
			Kind:       "RoutedServiceList",
			APIVersion: v1alpha1.SchemaGroupVersion.String(),
		},
		Items: []v1alpha1.RoutedService{sampleRS},
	})
	if err != nil {
		t.Fatal("Could not convert mock RoutedServiceList into JSON")
	}
	restClient, err := mockRESTClientRaw(routedServiceListJSON)

	// Create missing API
	waitGroup.Add(1)
	go testKongOperationCalled(t, "/apis", http.MethodPost, apiRequestFromRS(&sampleRS), nil, &waitGroup)

	// Create new upstream
	waitGroup.Add(1)
	go testKongOperationCalled(t, "/upstreams", http.MethodPost, upstreamFromRS(&sampleRS), nil, &waitGroup)

	payloads := []Payload{}
	for _, backend := range sampleRS.Spec.Backends {
		payloads = append(payloads, Payload{
			request:    ipTargetFromBackend(&backend),
			httpMethod: http.MethodPost,
		})
	}
	// Add the new targets
	waitGroup.Add(1)
	go testKongOperationCalledMultiple(t, fmt.Sprintf("/upstreams/%s/targets", sampleRS.ObjectMeta.Name), payloads, &waitGroup)

	controller := RoutedServiceController{restClient, kongClient, nil, namespace}
	controller.LookupHost = lookupHost
	ctx, _ := context.WithTimeout(context.Background(), time.Millisecond*5)
	controller.createWatches(ctx)

	<-ctx.Done()
	waitGroup.Wait()
}

func TestKongReconciledWithUpdatedRoutedServices(t *testing.T) {
	setup()
	defer shutdown()

	waitGroup := sync.WaitGroup{}

	originalRS := sampleRoutedService("originalservice")
	updatedRS := sampleRoutedService("updatedRS")
	updatedRS.Spec.Backends[0].Weight += 10

	restClient, err := mockRESTClient([]v1alpha1.RoutedService{updatedRS})
	if err != nil {
		t.Fatal("Could not create rest client")
	}

	serviceName := updatedRS.ObjectMeta.Name
	kongTargets := targetsFromRS(&originalRS)
	updatedTarget := ipTargetFromBackend(&updatedRS.Spec.Backends[0])
	// Does the upstream exist?
	waitGroup.Add(1)
	go testKongOperationCalled(t, fmt.Sprintf("/upstreams/%s", serviceName), http.MethodGet, nil, nil, &waitGroup)
	// What are the active targets?
	waitGroup.Add(1)
	go testKongOperationCalled(t, fmt.Sprintf("/upstreams/%s/targets/active", serviceName), http.MethodGet, nil, kongTargets, &waitGroup)
	// Replace the old target
	waitGroup.Add(1)
	go testKongOperationCalled(t, fmt.Sprintf("/upstreams/%s/targets", serviceName), http.MethodPost, updatedTarget, nil, &waitGroup)
	// Make sure the API upstream is correct
	waitGroup.Add(1)
	go testKongOperationCalled(t, fmt.Sprintf("/apis/%s", serviceName), http.MethodGet, nil, apiFromRS(&updatedRS), &waitGroup)

	controller := RoutedServiceController{restClient, kongClient, nil, namespace}
	controller.LookupHost = lookupHost
	ctx, _ := context.WithTimeout(context.Background(), time.Millisecond*5)
	controller.createWatches(ctx)

	<-ctx.Done()
	waitGroup.Wait()
}

func TestKongReconciledWithDeletedRoutedServices(t *testing.T) {
	setup()
	defer shutdown()

	waitGroup := sync.WaitGroup{}

	orphanedAPI1 := "orphanedAPI1"
	orphanedAPI2 := "orphanedAPI2"
	waitGroup.Add(1)
	go testKongOperationCalled(t, "/apis", http.MethodGet, nil, kong.Apis{
		Data: []*kong.Api{
			&kong.Api{Name: orphanedAPI1},
			&kong.Api{Name: orphanedAPI2},
		},
	}, &waitGroup)
	waitGroup.Add(1)
	go testKongOperationCalledMultiple(t, "/apis/"+orphanedAPI1, []Payload{
		Payload{
			request:    nil,
			response:   kong.Api{UpstreamURL: orphanedAPI1},
			httpMethod: http.MethodGet,
		},
		Payload{
			request:    nil,
			response:   nil,
			httpMethod: http.MethodDelete,
		},
	}, &waitGroup)
	waitGroup.Add(1)
	go testKongOperationCalledMultiple(t, "/apis/"+orphanedAPI2, []Payload{
		Payload{
			request:    nil,
			response:   kong.Api{UpstreamURL: orphanedAPI2},
			httpMethod: http.MethodGet,
		},
		Payload{
			request:    nil,
			response:   nil,
			httpMethod: http.MethodDelete,
		},
	}, &waitGroup)
	waitGroup.Add(1)
	go testKongOperationCalled(t, "/upstreams/"+orphanedAPI1, http.MethodDelete, nil, nil, &waitGroup)
	waitGroup.Add(1)
	go testKongOperationCalled(t, "/upstreams/"+orphanedAPI2, http.MethodDelete, nil, nil, &waitGroup)

	restClient, err := mockRESTClient([]v1alpha1.RoutedService{})
	if err != nil {
		t.Fatal("Could not create rest client")
	}

	controller := RoutedServiceController{restClient, kongClient, nil, namespace}
	ctx, _ := context.WithTimeout(context.Background(), time.Millisecond)
	controller.Run(ctx)

	waitGroup.Wait()
}

func TestResilienceToKongUnavailable(t *testing.T) {
	setup()
	defer shutdown()
	// This will match everything until we add more specific handlers
	mux.HandleFunc("/", func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusInternalServerError)
	})

	serviceName := "bestservice"
	sampleRS := sampleRoutedService(serviceName)

	waitGroup := sync.WaitGroup{}

	restClient, err := mockRESTClient([]v1alpha1.RoutedService{sampleRS})
	if err != nil {
		t.Fatal("Could not create mock REST client")
	}
	controller := RoutedServiceController{restClient, kongClient, nil, namespace}
	controller.LookupHost = lookupHost
	ctx, _ := context.WithTimeout(context.Background(), time.Millisecond*1100)

	// Start controller without starting mock Kong endpoint
	go controller.Run(ctx)

	// Wait a bit to give the controller an opportunity to fail at connecting to Kong
	time.Sleep(time.Millisecond * 950)

	// Make sure the usual Kong API calls are made

	// Does the upstream exist?
	go testKongOperationCalled(t, fmt.Sprintf("/upstreams/%s", serviceName), http.MethodGet, nil, nil, &waitGroup)
	waitGroup.Add(1)
	// What are the active targets?
	go testKongOperationCalled(t, fmt.Sprintf("/upstreams/%s/targets/active", serviceName), http.MethodGet, nil, targetsFromRS(&sampleRS), &waitGroup)
	waitGroup.Add(1)
	// Make sure the API upstream is correct
	go testKongOperationCalled(t, fmt.Sprintf("/apis/%s", serviceName), http.MethodGet, nil, apiFromRS(&sampleRS), &waitGroup)
	waitGroup.Add(1)

	<-ctx.Done()
	waitGroup.Wait()
}

func TestResilienceToMalformedRoutedServiceSpec(t *testing.T) {
	testCases := []string{"spec", "service", "port"}

	serviceName := "malformed"
	sampleRS := sampleRoutedService(serviceName)
	sampleRSJSON, err := objectToJSON(sampleRS)
	if err != nil {
		t.Fatal("Failed to convert RoutedService into a JSON")
	}

	for _, testCase := range testCases {
		malformedRS := strings.Replace(sampleRSJSON, testCase, "malformed_"+testCase, 1)
		testInvalidRS(t, serviceName, malformedRS)
	}
}

func testInvalidRS(t *testing.T, serviceName, invalidRS string) {
	setup()
	defer shutdown()
	wellformedRSName := "wellformedRS"
	waitGroup := sync.WaitGroup{}

	wellformedRSJSON, err := objectToJSON(sampleRoutedService(wellformedRSName))
	if err != nil {
		t.Fatal("Failed to convert RoutedService into JSON")
	}

	rsListJSONWithMalformedRS := `
		{
		"kind": "RoutedServiceList",
		"apiVersion": "` + v1alpha1.SchemaGroupVersion.String() + `",
		"items": [` + string(invalidRS) + `, ` + string(wellformedRSJSON) + `]
		}
		`
	restClient, err := mockRESTClientRaw(rsListJSONWithMalformedRS)
	if err != nil {
		t.Fatal("Failed to create rest client for malformed RoutedService")
	}
	controller := RoutedServiceController{restClient, kongClient, nil, namespace}
	controller.LookupHost = lookupHost

	ctx, _ := context.WithTimeout(context.Background(), time.Millisecond*600)
	go controller.Run(ctx)

	waitGroup.Add(1)
	// Canary for making sure calls are being made for the well-formed RS
	go testKongOperationCalled(t, fmt.Sprintf("/upstreams/%s", wellformedRSName), http.MethodGet, nil, nil, &waitGroup)

	// Canary for making sure resources are not created for the malformed RS
	waitGroup.Add(1)
	go testKongOperationNotCalled(t, fmt.Sprintf("/upstreams"), http.MethodPost, &waitGroup)

	waitGroup.Wait()
}

func testAPIDeleted(t *testing.T, apiName string, waitGroup *sync.WaitGroup) {
	defer waitGroup.Done()
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)

	mux.HandleFunc("/apis/"+apiName, func(writer http.ResponseWriter, request *http.Request) {
		apiDeleted := false
		switch request.Method {
		case http.MethodGet:
			if !apiDeleted {
				testRequestMatches(t, request, http.MethodGet, nil)
				writeObjectResponse(t, &writer, kong.Api{
					UpstreamURL: apiName,
				})
			} else {
				writer.WriteHeader(http.StatusNotFound)
				t.Error("Tried to GET the API that was just deleted.")
			}
		case http.MethodDelete:
			apiDeleted = true
			testRequestMatches(t, request, http.MethodDelete, nil)
			cancel()
		default:
			t.Errorf("Unexpected http method '%s' used on kong /apis/ endpoint", request.Method)
		}
	})

	<-ctx.Done()
	if ctx.Err() != context.Canceled {
		t.Errorf("Kong API associated with routed service was not deleted as expected")
	}
}

func testKongOperationCalled(t *testing.T, apiPath string, httpMethod string, expectedPayload interface{}, responsePayload interface{}, waitGroup *sync.WaitGroup) {
	testKongOperationCalledMultiple(t, apiPath, []Payload{Payload{
		request:    expectedPayload,
		response:   responsePayload,
		httpMethod: httpMethod,
	}}, waitGroup)
}

func testKongOperationCalledMultiple(t *testing.T, apiPath string, payloads []Payload, waitGroup *sync.WaitGroup) {
	defer waitGroup.Done()
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)

	payloadIndex := 0
	mux.HandleFunc(apiPath, func(writer http.ResponseWriter, request *http.Request) {
		if payloadIndex >= len(payloads) {
			return
		}

		payload := payloads[payloadIndex]
		payloadIndex++
		testRequestMatches(t, request, payload.httpMethod, payload.request)
		if payload.response != nil {
			writeObjectResponse(t, &writer, payload.response)
		}

		if payloadIndex == len(payloads) {
			cancel()
		}
	})

	<-ctx.Done()
	if ctx.Err() != context.Canceled {
		t.Errorf("Kong operation %s was not called as expected", apiPath)
	}
}

func testKongOperationNotCalled(t *testing.T, apiPath string, httpMethod string, waitGroup *sync.WaitGroup) {
	defer waitGroup.Done()
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)

	mux.HandleFunc(apiPath, func(writer http.ResponseWriter, request *http.Request) {
		if httpMethod == request.Method {
			cancel()
		}
	})

	<-ctx.Done()
	if ctx.Err() != context.DeadlineExceeded {
		t.Errorf("Kong operation %s was called unexpectedly", apiPath)
	}
}

func sampleRoutedService(name string) v1alpha1.RoutedService {
	return v1alpha1.RoutedService{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: v1alpha1.RoutedServiceSpec{
			Hosts: name+".host.name",
			Backends: []v1alpha1.Backend{
				v1alpha1.Backend{
					Service: "service-1",
					Port:    80,
					Weight:  80,
				},
				v1alpha1.Backend{
					Service: "service-2",
					Port:    80,
					Weight:  20,
				},
			},
		},
	}
}

func ipTargetFromBackend(backend *v1alpha1.Backend) *kong.Target {
	target := targetFromBackend(backend)
	target.Target = "ip-" + target.Target
	return target
}

func targetsFromRS(routedService *v1alpha1.RoutedService) kong.Targets {
	targets := []*kong.Target{}
	for _, backend := range routedService.Spec.Backends {
		newTarget := ipTargetFromBackend(&backend)
		newTarget.ID = backend.Service
		targets = append(targets, newTarget)
	}

	return kong.Targets{
		Data:  targets,
		Total: len(targets),
	}
}

func apiFromRS(routedService *v1alpha1.RoutedService) kong.Api {
	return kong.Api{
		UpstreamURL: "http://" + routedService.ObjectMeta.Name,
		Name:        routedService.ObjectMeta.Name,
	}
}

func apiRequestFromRS(routedService *v1alpha1.RoutedService) kong.ApiRequest {
	return kong.ApiRequest{
		UpstreamURL: "http://" + routedService.ObjectMeta.Name,
		Name:        routedService.ObjectMeta.Name,
		Hosts:       routedService.Spec.Hosts,
	}
}

func upstreamFromRS(routedService *v1alpha1.RoutedService) kong.Upstream {
	return kong.Upstream{
		Name: routedService.ObjectMeta.Name,
	}
}

func mockRESTClient(routedServices []v1alpha1.RoutedService) (*rest.RESTClient, error) {
	routedServiceList := v1alpha1.RoutedServiceList{
		TypeMeta: metav1.TypeMeta{
			Kind:       "RoutedServiceList",
			APIVersion: v1alpha1.SchemaGroupVersion.String(),
		},
		Items: routedServices,
	}
	routedServiceListJSON, err := objectToJSON(routedServiceList)
	if err != nil {
		return nil, err
	}

	return mockRESTClientRaw(routedServiceListJSON)
}

func mockRESTClientRaw(response string) (*rest.RESTClient, error) {
	restClient, _, err := client.NewClient(&rest.Config{})
	if err != nil {
		return nil, err
	}
	restClient.Client = fake.CreateHTTPClient(func(req *http.Request) (*http.Response, error) {
		httpResp := http.Response{
			StatusCode: 200,
			Body:       ioutil.NopCloser(strings.NewReader(response)),
			Request:    req,
		}
		return &httpResp, nil
	})

	return restClient, nil
}

func setup() {
	mux = http.NewServeMux()
	server = httptest.NewServer(mux)

	kongClient, _ = kong.NewClient(nil, server.URL)
	FullResyncInterval = time.Millisecond * 100
	opTimeout = time.Millisecond * 100
}

func shutdown() {
	server.Close()
}

func testRequestMatches(t *testing.T, request *http.Request, expectedMethod string, expectedObject interface{}) {
	if got := request.Method; got != expectedMethod {
		t.Errorf("Request method: %v, want %v at uri %v", got, expectedMethod, request.RequestURI)
	}

	if expectedObject != nil {
		bodyBytes, err := ioutil.ReadAll(request.Body)
		if err != nil {
			t.Errorf("Error reading request body: %v", err)
			return
		}
		expectedObjectJSON, err := objectToJSON(expectedObject)
		if err != nil {
			t.Errorf("Error converting expected objectin into json: %v", err)
			return
		}
		if got := string(bodyBytes); got != expectedObjectJSON {
			t.Errorf("Request body is '%s' but I want '%s'", got, expectedObjectJSON)
		}
	}
}

func writeObjectResponse(t *testing.T, writer *http.ResponseWriter, object interface{}) {
	objectJSON, err := objectToJSON(object)
	if err != nil {
		t.Errorf("Error converting expected object into json: %v", err)
		return
	}
	fmt.Fprint(*writer, objectJSON)
}

func objectToJSON(obj interface{}) (string, error) {
	var buf io.ReadWriter
	buf = new(bytes.Buffer)
	err := json.NewEncoder(buf).Encode(obj)
	if err != nil {
		return "", err
	}
	objectJSON, err := ioutil.ReadAll(buf)
	if err != nil {
		return "", err
	}

	return string(objectJSON), nil
}
