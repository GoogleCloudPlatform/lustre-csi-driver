/*
Copyright 2025 Google LLC

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    https://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package lustre

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"runtime"
	"strings"

	"cloud.google.com/go/longrunning/autogen/longrunningpb"
	lustre "github.com/GoogleCloudPlatform/lustre-csi-driver/pkg/cloud_provider/lustre/apiv1alpha"
	"github.com/GoogleCloudPlatform/lustre-csi-driver/pkg/cloud_provider/lustre/apiv1alpha/lustrepb"
	"github.com/GoogleCloudPlatform/lustre-csi-driver/pkg/common"
	"github.com/googleapis/gax-go/v2/apierror"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
	locationpb "google.golang.org/genproto/googleapis/cloud/location"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"k8s.io/klog/v2"
)

const (
	// Endpoint URLs.
	autopushEndpoint = "autopush-lustre.sandbox.googleapis.com"
	stagingEndpoint  = "staging-lustre.sandbox.googleapis.com"
	prodEndpoint     = "lustre.googleapis.com"
	// Endpoint names.
	autopush = "autopush"
	staging  = "staging"
	prod     = "prod"

	v1alphaMessageType = "google.cloud.lustre.v1alpha.OperationMetadata"
)

// userErrorCodeMap tells how API error types are translated to error codes.
var userErrorCodeMap = map[int]codes.Code{
	http.StatusForbidden:       codes.PermissionDenied,
	http.StatusBadRequest:      codes.InvalidArgument,
	http.StatusTooManyRequests: codes.ResourceExhausted,
	http.StatusNotFound:        codes.NotFound,
}

type ServiceInstance struct {
	Project     string
	Location    string
	Name        string
	Filesystem  string
	Network     string
	IP          string
	Description string
	State       string
	Labels      map[string]string
	CapacityGib int64
}

type ListFilter struct {
	Project  string
	Location string
}

type Service interface {
	CreateInstance(ctx context.Context, instance *ServiceInstance) (*ServiceInstance, error)
	DeleteInstance(ctx context.Context, instance *ServiceInstance) error
	GetInstance(ctx context.Context, instance *ServiceInstance) (*ServiceInstance, error)
	ListInstance(ctx context.Context, instance *ListFilter) ([]*ServiceInstance, error)
	GetCreateInstanceOp(ctx context.Context, instance *ServiceInstance) (*longrunningpb.Operation, error)
	ListLocations(ctx context.Context, instance *ListFilter) ([]string, error)
}

type lustreServiceManager struct {
	lustreClient *lustre.Client
}

var _ Service = &lustreServiceManager{}

func NewLustreService(ctx context.Context, client *http.Client, version, endpoint string) (Service, error) {
	endpointMap := map[string]string{
		autopush: autopushEndpoint,
		staging:  stagingEndpoint,
		prod:     prodEndpoint,
	}
	address, exists := endpointMap[endpoint]
	if !exists {
		return nil, fmt.Errorf("invalid lustre API endpoint %q. Supported endpoints are autopush, staging, or prod", endpoint)
	}

	opts := []option.ClientOption{
		option.WithHTTPClient(client),
		option.WithEndpoint(address),
		option.WithUserAgent(fmt.Sprintf("Lustre CSI Driver/%s (%s %s)", version, runtime.GOOS, runtime.GOARCH)),
	}
	lustreClient, err := lustre.NewRESTClient(ctx, opts...)
	if err != nil {
		return nil, err
	}
	klog.Infof("Using %s endpoint %q for lustre service", endpoint, address)

	return &lustreServiceManager{
		lustreClient: lustreClient,
	}, nil
}

func (sm *lustreServiceManager) CreateInstance(ctx context.Context, instance *ServiceInstance) (*ServiceInstance, error) {
	req := &lustrepb.CreateInstanceRequest{
		Parent:     fmt.Sprintf("projects/%s/locations/%s", instance.Project, instance.Location),
		InstanceId: instance.Name,
		Instance: &lustrepb.Instance{
			Network:     instance.Network,
			Description: instance.Description,
			Labels:      instance.Labels,
			CapacityGib: instance.CapacityGib,
			Filesystem:  instance.Filesystem,
		},
	}
	klog.V(4).Infof("Creating Lustre instance %+v", instance)
	op, err := sm.lustreClient.CreateInstance(ctx, req)
	if err != nil {
		return nil, err
	}
	klog.V(4).Infof("Waiting for the completion of creation op %q for instance %q", op.Name(), instance.Name)
	resp, err := op.Wait(ctx)
	if err != nil {
		return nil, err
	}

	return cloudInstanceToServiceInstance(resp)
}

func (sm *lustreServiceManager) DeleteInstance(ctx context.Context, instance *ServiceInstance) error {
	instanceFullName := instanceFullName(instance)
	req := &lustrepb.DeleteInstanceRequest{
		Name: instanceFullName,
	}
	klog.V(4).Infof("Deleting Lustre instance %q", instanceFullName)
	op, err := sm.lustreClient.DeleteInstance(ctx, req)
	if err != nil {
		if IsNotFoundErr(err) {
			return nil
		}

		return fmt.Errorf("failed to delete lustre instance %q: %w", instanceFullName, err)
	}
	klog.V(4).Infof("Waiting for the completion of deletion op %q for instance %q", op.Name(), instanceFullName)

	return op.Wait(ctx)
}

func (sm *lustreServiceManager) ListInstance(ctx context.Context, filter *ListFilter) ([]*ServiceInstance, error) {
	req := &lustrepb.ListInstancesRequest{
		Parent: fmt.Sprintf("projects/%s/locations/%s", filter.Project, filter.Location),
	}
	var instances []*ServiceInstance
	it := sm.lustreClient.ListInstances(ctx, req)
	for {
		resp, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("ListInstances failed for request %v: %w", req, err)
		}
		serviceInstance, err := cloudInstanceToServiceInstance(resp)
		if err != nil {
			return nil, err
		}
		instances = append(instances, serviceInstance)
	}
	klog.Infof("Listed %d instances for project %s in zone %s", len(instances), filter.Project, filter.Location)

	return instances, nil
}

func (sm *lustreServiceManager) ListLocations(ctx context.Context, filter *ListFilter) ([]string, error) {
	req := &locationpb.ListLocationsRequest{
		Name: fmt.Sprintf("projects/%s", filter.Project),
	}

	var locations []string
	it := sm.lustreClient.ListLocations(ctx, req)
	for {
		resp, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("ListLocations failed for request %v: %w", req, err)
		}
		locations = append(locations, resp.GetLocationId())
	}
	klog.Infof("Listed %d available zones for project %s", len(locations), filter.Project)

	return locations, nil
}

func (sm *lustreServiceManager) GetInstance(ctx context.Context, instance *ServiceInstance) (*ServiceInstance, error) {
	instanceFullName := instanceFullName(instance)
	req := &lustrepb.GetInstanceRequest{
		Name: instanceFullName,
	}
	klog.V(4).Infof("Getting lustre instance %q", instanceFullName)
	resp, err := sm.lustreClient.GetInstance(ctx, req)
	if err != nil {
		klog.V(4).Infof("Failed to get instance %q: %v", instanceFullName, err)

		return nil, err
	}
	if resp != nil {
		return cloudInstanceToServiceInstance(resp)
	}

	return nil, fmt.Errorf("failed to get instance %q: empty response", instanceFullName)
}

func (sm *lustreServiceManager) GetCreateInstanceOp(ctx context.Context, instance *ServiceInstance) (*longrunningpb.Operation, error) {
	req := &longrunningpb.ListOperationsRequest{
		Name: fmt.Sprintf("projects/%s/locations/%s", instance.Project, instance.Location),
	}
	it := sm.lustreClient.ListOperations(ctx, req)
	for {
		resp, err := it.Next()
		if errors.Is(err, iterator.Done) {
			return nil, nil
		}
		if err != nil {
			return nil, fmt.Errorf("ListOperations failed for request %v: %w", req, err)
		}

		if resp.GetMetadata().MessageName() != v1alphaMessageType {
			klog.V(4).Infof("Skipping operation %q due to invalid message type: got %q, expected %q", resp.GetName(), resp.GetMetadata().MessageName(), v1alphaMessageType)

			continue
		}

		var metaData lustrepb.OperationMetadata
		err = anypb.UnmarshalTo(resp.GetMetadata(), &metaData, proto.UnmarshalOptions{})
		if err != nil {
			return nil, fmt.Errorf("failed to unmarshal %+v: %w", resp, err)
		}

		// Return the response since a single instance creation call generates only one operation.
		// The CSI driver will never initiate more than one instance creation call for the same volume.
		if strings.ToLower(metaData.GetVerb()) == "create" && metaData.GetTarget() == instanceFullName(instance) {
			klog.V(4).Infof("Existing operation found for instance %q: %+v ", instanceFullName(instance), resp)

			return resp, nil
		}
	}
}

func instanceFullName(instance *ServiceInstance) string {
	return fmt.Sprintf("projects/%s/locations/%s/instances/%s", instance.Project, instance.Location, instance.Name)
}

func parseInstanceFullName(fullName string) (project, location, name string, err error) {
	regex := regexp.MustCompile(`^projects/([^/]+)/locations/([^/]+)/instances/([^/]+)$`)

	substrings := regex.FindStringSubmatch(fullName)
	if substrings == nil {
		err = fmt.Errorf("failed to parse instance full name %v", fullName)

		return
	}

	return substrings[1], substrings[2], substrings[3], nil
}

func cloudInstanceToServiceInstance(instance *lustrepb.Instance) (*ServiceInstance, error) {
	project, location, name, err := parseInstanceFullName(instance.GetName())
	if err != nil {
		return nil, err
	}

	ip, err := parseInstanceIP(instance.GetMountPoint())
	if err != nil {
		return nil, err
	}

	return &ServiceInstance{
		Name:        name,
		Location:    location,
		Project:     project,
		Network:     instance.GetNetwork(),
		Description: instance.GetDescription(),
		State:       instance.GetState().String(),
		Labels:      instance.GetLabels(),
		CapacityGib: instance.GetCapacityGib(),
		Filesystem:  instance.GetFilesystem(),
		IP:          ip,
	}, nil
}

// ParseIP extracts the IP address from the Mountpoint field of a Lustre instance.
// The Mountpoint is expected to be a formatted string in the format "10.90.24.35@tcp:/lfs".
func parseInstanceIP(instanceMountpoint string) (string, error) {
	// Lustre instance has an empty mountpoint field while it is in the "creating" state.
	if instanceMountpoint == "" {
		return "", nil
	}

	parts := strings.Split(instanceMountpoint, "@")
	if len(parts) < 2 {
		return "", fmt.Errorf("invalid instanceMountpoint %s", instanceMountpoint)
	}

	return parts[0], nil
}

func IsNotFoundErr(err error) bool {
	var apiErr *googleapi.Error
	if !errors.As(err, &apiErr) {
		return false
	}

	return apiErr.Code == http.StatusNotFound
}

func CompareInstances(a, b *ServiceInstance) error {
	mismatches := []string{}
	if a.Name != b.Name {
		mismatches = append(mismatches, "instance name")
	}
	if a.Project != b.Project {
		mismatches = append(mismatches, "instance project")
	}
	if a.Location != b.Location {
		mismatches = append(mismatches, "instance location")
	}
	if a.CapacityGib != b.CapacityGib {
		mismatches = append(mismatches, "instance size")
	}
	if a.Network != b.Network {
		mismatches = append(mismatches, "network name")
	}

	if len(mismatches) > 0 {
		return fmt.Errorf("instance %s already exists but doesn't match expected: %+v", a.Name, mismatches)
	}

	return nil
}

// Status error returns the error as a grpc status error, and
// sets the grpc error code according to CodeForError.
func StatusError(err error) error {
	if err == nil {
		return nil
	}

	return status.Error(*codeForError(err), err.Error())
}

// codeForError returns a pointer to the grpc error code that maps to the http
// error code for the passed in user googleapi error or context error. Returns
// codes.Internal if the given error is not a googleapi error caused by the user.
// The following http error codes are considered user errors:
// (1) http 400 Bad Request, returns grpc InvalidArgument,
// (2) http 403 Forbidden, returns grpc PermissionDenied,
// (3) http 404 Not Found, returns grpc NotFound
// (4) http 429 Too Many Requests, returns grpc ResourceExhausted
// The following errors are considered context errors:
// (1) "context deadline exceeded", returns grpc DeadlineExceeded,
// (2) "context canceled", returns grpc Canceled.
func codeForError(err error) *codes.Code {
	if err == nil {
		return nil
	}
	if errCode := existingErrorCode(err); errCode != nil {
		return errCode
	}
	if errCode := isUserOperationError(err); errCode != nil {
		return errCode
	}
	if errCode := isContextError(err); errCode != nil {
		return errCode
	}
	if errCode := isGoogleAPIError(err); errCode != nil {
		return errCode
	}

	return errCodePtr(codes.Internal)
}

// isUserError returns a pointer to the grpc error code that maps to the http
// error code for the passed in user googleapi error. Returns nil if the
// given error is not a googleapi error caused by the user. The following
// http error codes are considered user errors:
// (1) http 400 Bad Request, returns grpc InvalidArgument,
// (2) http 403 Forbidden, returns grpc PermissionDenied,
// (3) http 404 Not Found, returns grpc NotFound
// (4) http 429 Too Many Requests, returns grpc ResourceExhausted.
func isUserOperationError(err error) *codes.Code {
	// Upwrap the error
	var apiErr *googleapi.Error
	if !errors.As(err, &apiErr) {
		// Fallback to check for expected error code in the error string
		return containsUserErrStr(err)
	}

	return nil
}

func containsUserErrStr(err error) *codes.Code {
	if err == nil {
		return nil
	}

	// Error string picked up from https://cloud.google.com/apis/design/errors#handling_errors
	if strings.Contains(err.Error(), "PERMISSION_DENIED") {
		return errCodePtr(codes.PermissionDenied)
	}
	if strings.Contains(err.Error(), "RESOURCE_EXHAUSTED") {
		return errCodePtr(codes.ResourceExhausted)
	}
	if strings.Contains(err.Error(), "INVALID_ARGUMENT") {
		return errCodePtr(codes.InvalidArgument)
	}
	if strings.Contains(err.Error(), "NOT_FOUND") {
		return errCodePtr(codes.NotFound)
	}

	return nil
}

// isContextError returns a pointer to the grpc error code DeadlineExceeded
// if the passed in error contains the "context deadline exceeded" string and returns
// the grpc error code Canceled if the error contains the "context canceled" string.
func isContextError(err error) *codes.Code {
	if err == nil {
		return nil
	}

	errStr := err.Error()
	if strings.Contains(errStr, context.DeadlineExceeded.Error()) {
		return errCodePtr(codes.DeadlineExceeded)
	}
	if strings.Contains(errStr, context.Canceled.Error()) {
		return errCodePtr(codes.Canceled)
	}

	return nil
}

// existingErrorCode returns a pointer to the grpc error code for the passed in error.
// Returns nil if the error is nil, or if the error cannot be converted to a grpc status.
// Since github.com/googleapis/gax-go/v2/apierror now wraps googleapi errors (returned from
// GCE API calls), and sets their status error code to Unknown, we now have to make sure we
// only return existing error codes from errors that do not wrap googleAPI errors. Otherwise,
// we will return Unknown for all GCE API calls that return googleapi errors.
func existingErrorCode(err error) *codes.Code {
	if err == nil {
		return nil
	}

	var te *common.TemporaryError
	// explicitly check if the error type is a `common.TemporaryError`.
	if errors.As(err, &te) {
		if status, ok := status.FromError(err); ok {
			return errCodePtr(status.Code())
		}
	}
	// We want to make sure we catch other error types that are statusable.
	// (eg. grpc-go/internal/status/status.go Error struct that wraps a status)
	var googleErr *googleapi.Error
	if !errors.As(err, &googleErr) {
		if status, ok := status.FromError(err); ok {
			return errCodePtr(status.Code())
		}
	}

	return nil
}

// isGoogleAPIError returns the gRPC status code for the given googleapi error by mapping
// the googleapi error's HTTP code to the corresponding gRPC error code. If the error is
// wrapped in an APIError (github.com/googleapis/gax-go/v2/apierror), it maps the wrapped
// googleAPI error's HTTP code to the corresponding gRPC error code. Returns an error if
// the given error is not a googleapi error.
func isGoogleAPIError(err error) *codes.Code {
	var googleErr *googleapi.Error
	if !errors.As(err, &googleErr) {
		return nil
	}
	var sourceCode int
	var apiErr *apierror.APIError
	if errors.As(err, &apiErr) {
		// When googleapi.Err is used as a wrapper, we return the error code of the wrapped contents.
		sourceCode = apiErr.HTTPCode()
	} else {
		// Rely on error code in googleapi.Err when it is our primary error.
		sourceCode = googleErr.Code
	}
	// Map API error code to user error code.
	if code, ok := userErrorCodeMap[sourceCode]; ok {
		return errCodePtr(code)
	}
	// Map API error code to user error code.
	return nil
}

func errCodePtr(code codes.Code) *codes.Code {
	return &code
}
