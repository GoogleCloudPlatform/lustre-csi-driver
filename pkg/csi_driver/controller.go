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

package driver

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strconv"
	"strings"

	"cloud.google.com/go/lustre/apiv1/lustrepb"
	"github.com/GoogleCloudPlatform/lustre-csi-driver/pkg/cloud_provider/lustre"
	"github.com/GoogleCloudPlatform/lustre-csi-driver/pkg/util"
	csi "github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog/v2"
)

const (
	// Kubernetes prefixes.
	csiPrefixKubernetesStorage = "csi.storage.k8s.io"
	storagePrefixKubernetes    = "storage.kubernetes.io"

	// Storage Class K8s parameters.
	keyPVCName      = "csi.storage.k8s.io/pvc/name"
	keyPVCNamespace = "csi.storage.k8s.io/pvc/namespace"
	keyPVName       = "csi.storage.k8s.io/pv/name"

	// StorageClass user provided parameters.
	keyDescription              = "description"
	keyLabels                   = "labels"
	keyNetwork                  = "network"
	keyPerUnitStorageThroughput = "perunitstoragethroughput"

	// Shared keys between StorageClass parameters and PersistentVolume volumeAttributes.
	keyFilesystem = "filesystem"

	// Keys for tags to attach to the provisioned disk.
	tagKeyCreatedForClaimNamespace = "kubernetes_io_created-for_pvc_namespace"
	tagKeyCreatedForClaimName      = "kubernetes_io_created-for_pvc_name"
	tagKeyCreatedForVolumeName     = "kubernetes_io_created-for_pv_name"
	tagKeyCreatedBy                = "storage_gke_io_created-by"

	MinVolumeSizeBytes int64 = 9000 * util.Gib

	// Keys for Topology.
	TopologyKeyZone = "topology.gke.io/zone"

	// PV Volume attributes.
	keyInstanceIP      = "ip"
	keyMountPoint      = "mountpoint"
	keyAccessCheckType = "access_check_type"

	defaultNetwork = "default"

	fsnamePrefix = "lfs"
)

var (
	// Supported parameters.
	paramKeys = []string{
		keyDescription,
		keyLabels,
		keyNetwork,
		keyFilesystem,
		keyPerUnitStorageThroughput,
	}

	// Supported volume attribute keys.
	volumeAttributes = []string{
		keyInstanceIP,
		keyFilesystem,
		keyMountPoint,
		keyAccessCheckType,
	}
)

// controllerServer handles volume provisioning.
type controllerServer struct {
	// Embed UnimplementedIdentityServer to ensure the driver returns Unimplemented for any
	// new RPC methods that might be introduced in future versions of the spec.
	csi.UnimplementedControllerServer
	driver        *LustreDriver
	cloudProvider *lustre.Cloud
	volumeLocks   *util.VolumeLocks
}

func newControllerServer(driver *LustreDriver, cloud *lustre.Cloud) csi.ControllerServer {
	return &controllerServer{
		driver:        driver,
		cloudProvider: cloud,
		volumeLocks:   util.NewVolumeLocks(),
	}
}

func (s *controllerServer) ControllerGetCapabilities(_ context.Context, _ *csi.ControllerGetCapabilitiesRequest) (*csi.ControllerGetCapabilitiesResponse, error) {
	return &csi.ControllerGetCapabilitiesResponse{
		Capabilities: s.driver.cscap,
	}, nil
}

func (s *controllerServer) ValidateVolumeCapabilities(ctx context.Context, req *csi.ValidateVolumeCapabilitiesRequest) (*csi.ValidateVolumeCapabilitiesResponse, error) {
	// Validate arguments
	volumeID := req.GetVolumeId()
	if len(volumeID) == 0 {
		return nil, status.Error(codes.InvalidArgument, "ValidateVolumeCapabilities volumeID must be provided")
	}
	caps := req.GetVolumeCapabilities()
	// Validate that the volume matches the capabilities
	// Note that there is nothing in the instance that we actually need to validate
	if err := s.driver.validateVolumeCapabilities(caps); err != nil {
		return &csi.ValidateVolumeCapabilitiesResponse{
			Message: err.Error(),
		}, status.Error(codes.InvalidArgument, err.Error())
	}

	params, err := normalizeParams(req.GetParameters())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	vc, err := normalizeVolumeContext(req.GetVolumeContext())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	// Check the volume exists or not.
	instance, err := volumeIDToInstance(volumeID)
	if err != nil {
		return nil, status.Error(codes.NotFound, err.Error())
	}

	newInstance, err := s.cloudProvider.LustreService.GetInstance(ctx, instance)
	if err != nil && !lustre.IsNotFoundErr(err) {
		return nil, status.Error(codes.Internal, err.Error())
	}

	if newInstance == nil {
		return nil, status.Errorf(codes.NotFound, "volume %s not found", volumeID)
	}

	return &csi.ValidateVolumeCapabilitiesResponse{
		Confirmed: &csi.ValidateVolumeCapabilitiesResponse_Confirmed{
			VolumeContext:      vc,
			VolumeCapabilities: req.GetVolumeCapabilities(),
			Parameters:         params,
		},
	}, nil
}

func (s *controllerServer) CreateVolume(ctx context.Context, req *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
	// Validate arguments
	volumeName := strings.ToLower(req.GetName())
	if len(volumeName) == 0 {
		return nil, status.Error(codes.InvalidArgument, "CreateVolume name must be provided")
	}

	if err := s.driver.validateVolumeCapabilities(req.GetVolumeCapabilities()); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	capBytes, err := getRequestCapacity(req.GetCapacityRange())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	params, err := normalizeParams(req.GetParameters())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	newInstance, err := s.prepareNewInstance(volumeName, capBytes, params, req.GetAccessibilityRequirements())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	if acquired := s.volumeLocks.TryAcquire(volumeName); !acquired {
		return nil, status.Errorf(codes.Aborted, util.VolumeOperationAlreadyExistsFmt, volumeName)
	}
	defer s.volumeLocks.Release(volumeName)

	instance, err := s.cloudProvider.LustreService.GetInstance(ctx, newInstance)
	if err != nil && !lustre.IsNotFoundErr(err) {
		return nil, lustre.StatusError(err)
	}
	if instance != nil {
		klog.V(4).Infof("Found existing instance %+v, current instance %+v", instance, newInstance)
		// Instance already exists, check if it meets the request.
		if err := lustre.CompareInstances(newInstance, instance); err != nil {
			return nil, status.Error(codes.AlreadyExists, err.Error())
		}
		// Check if the Lustre instance is in the process of getting created.
		if instance.State == lustrepb.Instance_CREATING.String() {
			msg := fmt.Sprintf("Volume %v not ready, current state: %v", volumeName, instance.State)
			klog.V(4).Info(msg)

			return nil, status.Error(codes.DeadlineExceeded, msg)
		}
		if instance.State != lustrepb.Instance_ACTIVE.String() {
			msg := fmt.Sprintf("Volume %v not ready, current state: %v", volumeName, instance.State)
			klog.V(4).Info(msg)

			return nil, status.Error(codes.Unavailable, msg)
		}
	} else {
		// In the event of a stockout, the instance will be destroyed if the CreateInstance operation fails.
		// We should query the operation to retrieve the error and prevent the CSI driver from attempting to call CreateInstance again.
		op, err := s.cloudProvider.LustreService.GetCreateInstanceOp(ctx, newInstance)
		if err != nil {
			return nil, lustre.StatusError(err)
		}
		if op != nil && op.GetError() != nil {
			return nil, status.Error(codes.Code(op.GetError().GetCode()), op.GetError().GetMessage())
		}
		// Add labels.
		labels, err := extractLabels(params, s.driver.config.Name)
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
		newInstance.Labels = labels

		// The filesystem name for the lustre instance will be a randomly generated 8-character alphanumeric identifier to ensure uniqueness.
		// The CSI driver will validate this identifier by checking for duplicates against existing filesystem names within the target region.
		// If a conflict is found, a new identifier will be generated until uniqueness is confirmed.
		fsname, err := s.generateUniqueFilesystemName(ctx, newInstance)
		if err != nil {
			return nil, lustre.StatusError(err)
		}
		newInstance.Filesystem = fsname

		instance, err = s.cloudProvider.LustreService.CreateInstance(ctx, newInstance)
		if err != nil {
			return nil, lustre.StatusError(err)
		}
	}

	resp := &csi.CreateVolumeResponse{
		Volume: instanceToCSIVolume(instance),
	}
	klog.Infof("CreateVolume succeeded: %+v", resp)

	return resp, nil
}

func instanceToCSIVolume(instance *lustre.ServiceInstance) *csi.Volume {
	return &csi.Volume{
		VolumeId:      instanceToVolumeID(instance),
		CapacityBytes: instance.CapacityGib * util.Gib,
		VolumeContext: map[string]string{
			keyInstanceIP: instance.IP,
			keyFilesystem: instance.Filesystem,
		},
	}
}

func (s *controllerServer) DeleteVolume(ctx context.Context, req *csi.DeleteVolumeRequest) (*csi.DeleteVolumeResponse, error) {
	// Validate arguments
	volumeID := req.GetVolumeId()
	if len(volumeID) == 0 {
		return nil, status.Error(codes.InvalidArgument, "DeleteVolume volumeID must be provided")
	}

	instance, err := volumeIDToInstance(volumeID)
	if err != nil {
		// An invalid ID should be treated as doesn't exist
		klog.V(5).Infof("Failed to get instance for volume %v deletion: %v", volumeID, err)

		return &csi.DeleteVolumeResponse{}, nil
	}

	if acquired := s.volumeLocks.TryAcquire(volumeID); !acquired {
		return nil, status.Errorf(codes.Aborted, util.VolumeOperationAlreadyExistsFmt, volumeID)
	}
	defer s.volumeLocks.Release(volumeID)

	instance, err = s.cloudProvider.LustreService.GetInstance(ctx, instance)
	if err != nil && lustre.IsNotFoundErr(err) {
		return &csi.DeleteVolumeResponse{}, nil
	}
	if err != nil {
		return nil, lustre.StatusError(err)
	}
	if instance.State == "DELETING" {
		return nil, status.Errorf(codes.DeadlineExceeded, "Volume %s is under deletion", volumeID)
	}

	if err := s.cloudProvider.LustreService.DeleteInstance(ctx, instance); err != nil {
		return nil, lustre.StatusError(err)
	}

	klog.Infof("DeleteVolume succeeded for volume %s", volumeID)

	return &csi.DeleteVolumeResponse{}, nil
}

func getRequestCapacity(capRange *csi.CapacityRange) (int64, error) {
	// Default case where nothing is set
	if capRange == nil {
		return MinVolumeSizeBytes, nil
	}

	rBytes := capRange.GetRequiredBytes()
	rSet := rBytes > 0
	lBytes := capRange.GetLimitBytes()
	lSet := lBytes > 0

	if lSet && rSet && lBytes < rBytes {
		return 0, fmt.Errorf("limit bytes %v is less than required bytes %v", lBytes, rBytes)
	}
	if lSet && lBytes < MinVolumeSizeBytes {
		return 0, fmt.Errorf("limit bytes %v is less than minimum volume size: %v", lBytes, MinVolumeSizeBytes)
	}

	// If Required set just set capacity to that which is Required
	// We let Lustre backend handles cases where the requested capacity is less than the minimum or outside the required range.
	if rSet {
		return rBytes, nil
	}

	// If RequiredBytes is not set, we default to MinVolumeSizeBytes.
	return MinVolumeSizeBytes, nil
}

func (s *controllerServer) prepareNewInstance(name string, capBytes int64, params map[string]string, top *csi.TopologyRequirement) (*lustre.ServiceInstance, error) {
	location, err := s.pickZone(top)
	if err != nil {
		return nil, fmt.Errorf("invalid topology: %w", err)
	}
	networkFullNamePattern := regexp.MustCompile(`projects/([^/]+)/global/networks/([^/]+)`)
	networkNamePattern := regexp.MustCompile(`^[a-z]([-a-z0-9]*[a-z0-9])?$`)
	networkFullName := fmt.Sprintf("projects/%s/global/networks/%s", s.cloudProvider.Project, defaultNetwork)
	if v, exists := params[keyNetwork]; exists {
		if networkNamePattern.MatchString(v) {
			networkFullName = fmt.Sprintf("projects/%s/global/networks/%s", s.cloudProvider.Project, v)
		}
		if networkFullNamePattern.MatchString(v) {
			networkFullName = v
		}
	}
	instance := &lustre.ServiceInstance{
		Name:              name,
		Project:           s.cloudProvider.Project,
		Location:          location,
		CapacityGib:       capBytes / util.Gib, // TODO(tyuchn): investigate rounding mechanisms to enhance the UX when specifying capacities in TiB or GiB.
		Network:           networkFullName,
		GkeSupportEnabled: s.driver.config.EnableLegacyLustrePort,
	}
	if v, exists := params[keyDescription]; exists {
		if len(v) > 2048 {
			klog.Warningf("Instance %v description exceeds 2048 characters, truncating", name)
			v = v[:2048]
		}
		instance.Description = v
	}
	if v, exists := params[keyFilesystem]; exists {
		instance.Filesystem = v
	}

	v, exists := params[keyPerUnitStorageThroughput]
	if !exists {
		return nil, fmt.Errorf("parameter 'perUnitStorageThroughput' is required; supported values are 1000, 500, 250, or 125")
	}
	if !(v == "1000" || v == "500" || v == "250" || v == "125") {
		return nil, fmt.Errorf("invalid perUnitStorageThroughput %s, must be 1000, 500, 250 or 125", v)
	}
	perUnitStorageThroughput, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("failed to parse perUnitStorageThroughput %s: %w", v, err)
	}
	instance.PerUnitStorageThroughput = perUnitStorageThroughput

	return instance, nil
}

func (s *controllerServer) pickZone(top *csi.TopologyRequirement) (string, error) {
	if top == nil {
		return s.cloudProvider.Zone, nil
	}

	return pickZoneFromTopology(top)
}

// Pick the first available topology from preferred list or requisite list in that order.
func pickZoneFromTopology(top *csi.TopologyRequirement) (string, error) {
	reqZones, err := getZonesFromTopology(top.GetRequisite())
	if err != nil {
		return "", fmt.Errorf("could not get zones from requisite topology: %w", err)
	}

	prefZones, err := getZonesFromTopology(top.GetPreferred())
	if err != nil {
		return "", fmt.Errorf("could not get zones from preferred topology: %w", err)
	}

	if len(prefZones) == 0 && len(reqZones) == 0 {
		return "", errors.New("both requisite and preferred topology list empty")
	}

	if len(prefZones) != 0 {
		return prefZones[0], nil
	}

	return reqZones[0], nil
}

func getZonesFromTopology(topList []*csi.Topology) ([]string, error) {
	zones := []string{}
	for _, top := range topList {
		if top.GetSegments() == nil {
			return nil, errors.New("topologies specified but no segments")
		}

		zone, err := getZoneFromSegment(top.GetSegments())
		if err != nil {
			return nil, fmt.Errorf("could not get zone from topology: %w", err)
		}
		zones = append(zones, zone)
	}

	return zones, nil
}

func getZoneFromSegment(seg map[string]string) (string, error) {
	var zone string
	for k, v := range seg {
		switch k {
		case TopologyKeyZone:
			zone = v
		default:
			return "", fmt.Errorf("topology segment has unknown key %v", k)
		}
	}

	if len(zone) == 0 {
		return "", fmt.Errorf("topology specified but could not find zone in segment: %v", seg)
	}

	return zone, nil
}

func extractLabels(parameters map[string]string, driverName string) (map[string]string, error) {
	labels := make(map[string]string)
	scLables := make(map[string]string)
	for k, v := range parameters {
		switch strings.ToLower(k) {
		case keyPVCName:
			labels[tagKeyCreatedForClaimName] = v
		case keyPVCNamespace:
			labels[tagKeyCreatedForClaimNamespace] = v
		case keyPVName:
			labels[tagKeyCreatedForVolumeName] = v
		case keyLabels:
			var err error
			scLables, err = util.ConvertLabelsStringToMap(v)
			if err != nil {
				return nil, fmt.Errorf("parameters contain invalid labels parameter: %w", err)
			}
		}
	}

	labels[tagKeyCreatedBy] = strings.ReplaceAll(driverName, ".", "_")

	return mergeLabels(scLables, labels)
}

func mergeLabels(scLabels map[string]string, metedataLabels map[string]string) (map[string]string, error) {
	result := make(map[string]string)
	for k, v := range metedataLabels {
		result[k] = v
	}

	for k, v := range scLabels {
		if _, ok := result[k]; ok {
			return nil, fmt.Errorf("StorageClass labels cannot contain metadata label key %s", k)
		}

		result[k] = v
	}

	return result, nil
}

// generateUniqueFilesystemName generates a unique filesystem name for the Lustre instance.
func (s *controllerServer) generateUniqueFilesystemName(ctx context.Context, instance *lustre.ServiceInstance) (string, error) {
	if instance.Filesystem != "" {
		klog.Infof("Filesystem name auto-generation skipped for instance %+v: value is predefined in the storage class", instance)

		return instance.Filesystem, nil
	}
	targetRegion, err := util.GetRegionFromZone(instance.Location)
	if err != nil {
		return "", err
	}
	allZones, err := s.cloudProvider.LustreService.ListLocations(ctx, &lustre.ListFilter{Project: instance.Project})
	if err != nil {
		return "", err
	}
	targetZones, err := filterZonesByRegion(allZones, targetRegion)
	if err != nil {
		return "", err
	}

	// Create a set of existing filesystem names for quick lookup.
	existingFSNames := make(map[string]struct{})
	for _, zone := range targetZones {
		filter := &lustre.ListFilter{Project: instance.Project, Location: zone}
		instances, err := s.cloudProvider.LustreService.ListInstance(ctx, filter)
		if err != nil {
			return "", err
		}

		for _, inst := range instances {
			existingFSNames[inst.Filesystem] = struct{}{} // An empty struct is used because it consumes zero memory.
		}
	}

	// Generate unique identifier for lustre instance fsname.
	for {
		id, err := generateRandomID()
		if err != nil {
			return "", fmt.Errorf("failed to generate random lustre fsname: %w", err)
		}
		if _, exists := existingFSNames[id]; !exists {
			return id, nil
		}
	}
}

// filterZonesByRegion filters zones that belong to the specified region.
func filterZonesByRegion(zones []string, region string) ([]string, error) {
	var filteredZones []string
	for _, zone := range zones {
		zoneRegion, err := util.GetRegionFromZone(zone)
		if err != nil {
			return nil, err
		}
		if zoneRegion == region {
			filteredZones = append(filteredZones, zone)
		}
	}

	return filteredZones, nil
}

// generateRandomID generates an 8-character alphanumeric identifier with format "lfs-<NNNNN>".
func generateRandomID() (string, error) {
	num := "0123456789"
	id := make([]byte, 5)
	// Generate the remaining 5 characters (numeric).
	for i := 0; i < 5; i++ {
		charIndex, err := rand.Int(rand.Reader, big.NewInt(int64(len(num))))
		if err != nil {
			return "", err
		}
		id[i] = num[charIndex.Int64()]
	}

	return fsnamePrefix + string(id), nil
}

func (s *controllerServer) ControllerExpandVolume(ctx context.Context, req *csi.ControllerExpandVolumeRequest) (*csi.ControllerExpandVolumeResponse, error) {
	volumeID := req.GetVolumeId()
	if len(volumeID) == 0 {
		return nil, status.Error(codes.InvalidArgument, "ControllerExpandVolume volumeID must be provided")
	}

	if acquired := s.volumeLocks.TryAcquire(volumeID); !acquired {
		return nil, status.Errorf(codes.Aborted, util.VolumeOperationAlreadyExistsFmt, volumeID)
	}
	defer s.volumeLocks.Release(volumeID)

	instance, err := volumeIDToInstance(volumeID)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	instance, err = s.cloudProvider.LustreService.GetInstance(ctx, instance)
	if err != nil {
		return nil, lustre.StatusError(err)
	}

	if instance.State != lustrepb.Instance_ACTIVE.String() {
		return nil, status.Errorf(codes.FailedPrecondition, "Instance %s is in %s state", instance.Name, instance.State)
	}

	capBytes, err := getRequestCapacity(req.GetCapacityRange())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	if instance.CapacityGib*util.Gib >= capBytes {
		klog.V(4).Infof("Lustre instance %q already has capacity %d, which is greater than or equal to requested capacity %d", instance.Name, instance.CapacityGib*util.Gib, capBytes)

		return &csi.ControllerExpandVolumeResponse{
			CapacityBytes:         instance.CapacityGib * util.Gib,
			NodeExpansionRequired: false,
		}, nil
	}

	// Check if an expansion is already in progress. This can happen if a previous expansion request timed out on the client side
	// but was still processed by the backend.
	// Returning codes.DeadlineExceeded signals the csi-resizer to retry the operation, allowing the ongoing expansion to complete.
	opInProgress, err := s.cloudProvider.LustreService.IsOperationInProgress(ctx, instance, "update")
	if err != nil {
		return nil, lustre.StatusError(err)
	}
	if opInProgress {
		msg := fmt.Sprintf("Expansion for volume %q to new capacity %d GiB is already in progress", volumeID, util.BytesToGib(capBytes))
		klog.V(4).Info(msg)

		return nil, status.Error(codes.DeadlineExceeded, msg)
	}

	instance.CapacityGib = util.BytesToGib(capBytes)
	updatedInstance, err := s.cloudProvider.LustreService.ResizeInstance(ctx, instance)
	if err != nil {
		klog.Errorf("ResizeInstance failed for volume %q: %v", volumeID, err)

		return nil, lustre.StatusError(err)
	}

	klog.Infof("ControllerExpandVolume succeeded for volume %v, new size in bytes: %v", volumeID, updatedInstance.CapacityGib*util.Gib)

	return &csi.ControllerExpandVolumeResponse{
		CapacityBytes:         updatedInstance.CapacityGib * util.Gib,
		NodeExpansionRequired: false,
	}, nil
}
