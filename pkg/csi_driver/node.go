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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/GoogleCloudPlatform/lustre-csi-driver/pkg/network"
	"github.com/GoogleCloudPlatform/lustre-csi-driver/pkg/util"
	csi "github.com/container-storage-interface/spec/lib/go/csi"
	"golang.org/x/sys/unix"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog/v2"
	mount "k8s.io/mount-utils"
)

const (
	rwMask              = os.FileMode(0o660)
	roMask              = os.FileMode(0o440)
	execMask            = os.FileMode(0o110)
	initialRouteTableID = 100

	keyPodUID         = "csi.storage.k8s.io/pod.uid"
)

var (
	GlobalMountRoot   = "/var/lib/lustre/mounts"
	keyServiceAccount = "csi.storage.k8s.io/serviceAccount.name"
	keyPodNamespace   = "csi.storage.k8s.io/pod.namespace"
	keyServiceToken   = "csi.storage.k8s.io/serviceAccount.tokens"
)

var mountPointRegex = regexp.MustCompile(`^(.+)@tcp:/([^/]+)$`)

type nodeServer struct {
	// Embed UnimplementedIdentityServer to ensure the driver returns Unimplemented for any
	// new RPC methods that might be introduced in future versions of the spec.
	csi.UnimplementedNodeServer
	driver      *LustreDriver
	mounter     mount.Interface
	volumeLocks *util.VolumeLocks
}

func newNodeServer(driver *LustreDriver, mounter mount.Interface) csi.NodeServer {
	return &nodeServer{
		driver:      driver,
		mounter:     mounter,
		volumeLocks: util.NewVolumeLocks(),
	}
}

func (s *nodeServer) NodeGetInfo(_ context.Context, _ *csi.NodeGetInfoRequest) (*csi.NodeGetInfoResponse, error) {
	return &csi.NodeGetInfoResponse{
		NodeId: s.driver.config.NodeID,
		AccessibleTopology: &csi.Topology{
			Segments: map[string]string{TopologyKeyZone: s.driver.config.MetadataService.GetZone()},
		},
	}, nil
}

func (s *nodeServer) NodeGetCapabilities(_ context.Context, _ *csi.NodeGetCapabilitiesRequest) (*csi.NodeGetCapabilitiesResponse, error) {
	return &csi.NodeGetCapabilitiesResponse{
		Capabilities: s.driver.nscap,
	}, nil
}

func (s *nodeServer) NodeStageVolume(_ context.Context, req *csi.NodeStageVolumeRequest) (*csi.NodeStageVolumeResponse, error) {
	volumeID := req.GetVolumeId()
	if len(volumeID) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Volume ID not provided")
	}

	vc, err := normalizeVolumeContext(req.GetVolumeContext())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	ip := vc[keyInstanceIP]
	fsname := vc[keyFilesystem]
	mountPoint := vc[normalize(keyMountPoint)]

	if len(mountPoint) != 0 {
		var err error
		ip, fsname, err = parseMountPoint(mountPoint)
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
	}

	if len(ip) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Lustre instance IP is not provided")
	}

	if len(fsname) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Lustre filesystem name is not provided")
	}

	source := fmt.Sprintf("%s@tcp:/%s", ip, fsname)

	target := req.GetStagingTargetPath()
	if len(target) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Target path not provided")
	}

	volCap := req.GetVolumeCapability()
	if volCap == nil {
		return nil, status.Error(codes.InvalidArgument, "Volume capability not provided")
	}

	if err := s.driver.validateVolumeCapabilities([]*csi.VolumeCapability{req.GetVolumeCapability()}); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	if acquired := s.volumeLocks.TryAcquire(target); !acquired {
		return nil, status.Errorf(codes.Aborted, util.VolumeOperationAlreadyExistsFmt, target)
	}
	defer s.volumeLocks.Release(target)

	mountOptions := []string{}

	if m := volCap.GetMount(); m != nil {
		for _, f := range m.GetMountFlags() {
			if !hasOption(mountOptions, f) {
				mountOptions = append(mountOptions, f)
			}
		}
	}

	nodeName := s.driver.config.NodeID
	// Checking if the target directory is already mounted with a volume.
	mounted, err := s.isMounted(target)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Could not check if %q is mounted on node %s: %v", target, nodeName, err)
	}

	if mounted {
		klog.V(4).Infof("NodeStageVolume successfully mounted device %v to path %s on node %s, mount already exists.", volumeID, target, nodeName)

		return &csi.NodeStageVolumeResponse{}, nil
	}

	// Check for IAM access type
	accessType := vc[normalize(keyAccessCheckType)]
	isIAM := accessType == "IAM"

	if !isIAM {
		hasFSName, err := s.hasMountWithSameFSName(fsname)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "Could not check if there is an existing mountpoint with the same Lustre filesystem name %s on node %s: %v", fsname, nodeName, err)
		}
		if hasFSName {
			return nil, status.Errorf(codes.AlreadyExists, "A mountpoint with the same lustre filesystem name %q already exists on node %s. Please mount different lustre filesystems", fsname, nodeName)
		}
	}

	// Check if multi-nic is enabled by checking if there are additional NICs.
	if len(s.driver.config.AdditionalNics) > 0 {
		netlinker := network.NewNetlink()
		nodeClient := network.NewK8sClient()
		networkIntf := network.Manager(netlinker, nodeClient, s.driver.config.MetadataService)
		klog.V(4).Infof("Multi Nic feature is enabled and will configure route for Lustre instance: %v, IP: %v", volumeID, source)
		nics := s.driver.config.AdditionalNics
		for _, nicName := range nics {
			// Get NIC IP Addr
			nicIPAddr, err := networkIntf.GetNICIPAddr(nicName)
			if err != nil {
				return nil, status.Errorf(codes.Internal, "Could not get NIC IP address: %v", err)
			}
			// Find Table ID for NIC
			tableID, err := networkIntf.FindNextFreeTableID(initialRouteTableID, nicIPAddr)
			if err != nil {
				return nil, status.Errorf(codes.Internal, "Error trying to find a free Table ID: %v", err)
			}
			// Configure route for NIC & Lustre instance.
			err = networkIntf.ConfigureRoute(nicName, ip, tableID)
			if err != nil {
				return nil, status.Errorf(codes.Internal, "Route configuration for Multi-NIC failed: %v", err)
			}
		}
	}

	if isIAM {
		klog.Infof("NodeStageVolume: access_check_type=IAM, skipping node mount for volume %s", volumeID)
		return &csi.NodeStageVolumeResponse{}, nil
	}

	klog.V(5).Infof("NodeStageVolume creating dir %s on node %s", target, nodeName)
	if err := makeDir(target); err != nil {
		return nil, status.Errorf(codes.Internal, "Could not create dir %s on node %s: %v", target, nodeName, err)
	}

	klog.V(4).Infof("NodeStageVolume mounting volume %s to path %s on node %s with mountOptions %v", volumeID, target, nodeName, mountOptions)
	if err := s.mounter.MountSensitiveWithoutSystemd(source, target, "lustre", mountOptions, nil); err != nil {
		klog.Errorf("Mount %q failed on node %s, cleaning up", target, nodeName)
		if unmntErr := mount.CleanupMountPoint(target, s.mounter, false /* extensiveMountPointCheck */); unmntErr != nil {
			klog.Errorf("Unmount %q failed on node %s: %v", target, nodeName, unmntErr.Error())
		}

		return nil, status.Errorf(codes.Internal, "Could not mount %q at %q on node %s: %v", source, target, nodeName, err)
	}

	klog.V(4).Infof("NodeStageVolume successfully mounted volume %v to path %s on node %s", volumeID, target, nodeName)

	return &csi.NodeStageVolumeResponse{}, nil
}

func (s *nodeServer) NodeUnstageVolume(_ context.Context, req *csi.NodeUnstageVolumeRequest) (*csi.NodeUnstageVolumeResponse, error) {
	volumeID := req.GetVolumeId()
	if len(volumeID) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Volume ID not provided")
	}
	target := req.GetStagingTargetPath()
	if len(target) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Staging target path not provided")
	}

	// Acquire a lock on the target path instead of volumeID, since we do not want to serialize multiple node unpublish calls on the same volume.
	if acquired := s.volumeLocks.TryAcquire(target); !acquired {
		return nil, status.Errorf(codes.Aborted, util.VolumeOperationAlreadyExistsFmt, target)
	}
	defer s.volumeLocks.Release(target)

	// Check if the target path is mounted before unmounting.
	if notMnt, _ := s.mounter.IsLikelyNotMountPoint(target); notMnt {
		klog.V(5).InfoS("NodeUnstageVolume: staging target path not mounted, skipping unmount", "staging target", target)

		return &csi.NodeUnstageVolumeResponse{}, nil
	}
	// Always unmount the target path regardless of the detected mount state.
	// In cases where Lustre was force-unmounted, CleanupMountPoint may fail
	// to detect the state and error out with "cannot send after transport endpoint shutdown".
	klog.V(5).InfoS("NodeUnstageVolume attempting to unmount", "staging target", target)
	if err := s.mounter.Unmount(target); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to unmount staging target %q: %v", target, err)
	}

	if err := mount.CleanupMountPoint(target, s.mounter, false /* extensiveMountPointCheck */); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	klog.V(4).Infof("NodeUnstageVolume succeeded on volume %v from staging target path %s on node %s", volumeID, target, s.driver.config.NodeID)

	return &csi.NodeUnstageVolumeResponse{}, nil
}

func (s *nodeServer) NodePublishVolume(_ context.Context, req *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
	volumeID := req.GetVolumeId()
	if len(volumeID) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Volume ID not provided")
	}

	stagingTargetPath := req.GetStagingTargetPath()
	if len(stagingTargetPath) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Staging target path not provided")
	}

	targetPath := req.GetTargetPath()
	if len(targetPath) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Target path not provided")
	}

	volCap := req.GetVolumeCapability()
	if volCap == nil {
		return nil, status.Error(codes.InvalidArgument, "Volume capability not provided")
	}

	if err := s.driver.validateVolumeCapabilities([]*csi.VolumeCapability{req.GetVolumeCapability()}); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	// Global Mount Logic
	klog.Infof("NodePublishVolume: targetPath=%s", targetPath)
	// First acquire lock on targetPath to serialize operations on the same target
	if acquired := s.volumeLocks.TryAcquire(targetPath); !acquired {
		return nil, status.Errorf(codes.Aborted, util.VolumeOperationAlreadyExistsFmt, targetPath)
	}
	defer s.volumeLocks.Release(targetPath)

	// Check for IAM access type
	// Check for IAM access type
	vc, err := normalizeVolumeContext(req.GetVolumeContext())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	accessType := vc[normalize(keyAccessCheckType)]
	isIAM := accessType == "IAM"

	// Common bind mount options
	bindOptions := []string{"bind"}
	ro := req.GetReadonly()
	if ro {
		bindOptions = append(bindOptions, "ro")
	}

	klog.V(5).Infof("NodePublishVolume creating target dir %s", targetPath)
	if err := makeDir(targetPath); err != nil {
		return nil, status.Errorf(codes.Internal, "Could not create dir %q: %v", targetPath, err)
	}

	// Check if target is already mounted
	targetMounted, err := s.isMounted(targetPath)
	if err != nil {
		return nil, err
	}

	if isIAM {
		// --- VIRTUAL GLOBAL MOUNT PATH ---
		podUID := vc[normalize(keyPodUID)]
		namespace := vc[normalize(keyPodNamespace)]
		saName := vc[normalize(keyServiceAccount)]

		if podUID == "" || namespace == "" || saName == "" {
			return nil, status.Error(codes.InvalidArgument, "Missing Pod or ServiceAccount info in VolumeContext for IAM volume")
		}

		principal := fmt.Sprintf("%s/%s", namespace, saName)
		key := computeHash(volumeID + principal)
		globalMountPath := filepath.Join(GlobalMountRoot, key, "mount")

		// Acquire lock on the Global Key
		if acquired := s.volumeLocks.TryAcquire(key); !acquired {
			return nil, status.Errorf(codes.Aborted, "Operation exists for key %s", key)
		}
		defer s.volumeLocks.Release(key)

		if err := ensureGlobalDirectories(key); err != nil {
			return nil, status.Errorf(codes.Internal, "Failed to ensure global directories: %v", err)
		}

		projectID := s.driver.config.MetadataService.GetProject()
		audience := fmt.Sprintf("%s.svc.id.goog", projectID)
		if tokenJSON, ok := vc[normalize(keyServiceToken)]; ok {
			token, err := parseToken(tokenJSON, audience)
			if err != nil {
				klog.Warningf("Failed to parse token for key %s: %v", key, err)
			} else {
				if err := updateTokenFile(key, token); err != nil {
					klog.Errorf("Failed to update token file for key %s: %v", key, err)
					return nil, status.Errorf(codes.Internal, "Failed to update token file: %v", err)
				}
			}
		}

		// Check if Global Path is already mounted
		globalMounted, err := s.isMounted(globalMountPath)
		if err != nil {
			return nil, err
		}

		if !globalMounted {
			ip := vc[normalize(keyInstanceIP)]
			fsname := vc[normalize(keyFilesystem)]
			mountPoint := vc[normalize(keyMountPoint)]

			if len(mountPoint) != 0 {
				var err error
				ip, fsname, err = parseMountPoint(mountPoint)
				if err != nil {
					return nil, status.Error(codes.InvalidArgument, err.Error())
				}
			}
			if len(ip) == 0 || len(fsname) == 0 {
				return nil, status.Error(codes.InvalidArgument, "Lustre instance IP or filesystem name not provided")
			}
			source := fmt.Sprintf("%s@tcp:/%s", ip, fsname)
			// userOpt := fmt.Sprintf("user=gke-wi://%s+%s", principal, key)
			// mountOptions := []string{userOpt}
			mountOptions := []string{}
			

			klog.Infof("Mounting Global Path %s from %s with options %v", globalMountPath, source, mountOptions)
			if err := s.mounter.MountSensitiveWithoutSystemd(source, globalMountPath, "lustre", mountOptions, nil); err != nil {
				klog.Errorf("Global Mount %q failed: %v", globalMountPath, err)
				return nil, status.Errorf(codes.Internal, "Global Mount failed: %v", err)
			}
		}

		if err := addPodReference(key, podUID, req.GetVolumeId()); err != nil {
			return nil, status.Errorf(codes.Internal, "Failed to add pod reference: %v", err)
		}

		if !targetMounted {
			klog.Infof("Bind mounting %s to %s", globalMountPath, targetPath)
			if err := s.mounter.MountSensitiveWithoutSystemd(globalMountPath, targetPath, "", bindOptions, nil); err != nil {
				return nil, status.Errorf(codes.Internal, "Bind mount failed: %v", err)
			}
		}
	} else {
		// --- LEGACY PATH ---
		if !targetMounted {
			klog.Infof("NodePublishVolume: legacy bind mounting %s to %s", stagingTargetPath, targetPath)
			if err := s.mounter.MountSensitiveWithoutSystemd(stagingTargetPath, targetPath, "", bindOptions, nil); err != nil {
				return nil, status.Errorf(codes.Internal, "Legacy bind mount failed: %v", err)
			}
		}
	}

	// Handle FSGroup / Ownership
	var fsGroup string
	if m := volCap.GetMount(); m != nil && m.GetVolumeMountGroup() != "" {
		fsGroup = m.GetVolumeMountGroup()
	}
	if err := setVolumeOwnershipTopLevel(volumeID, targetPath, fsGroup, ro); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &csi.NodePublishVolumeResponse{}, nil
}

func (s *nodeServer) NodeUnpublishVolume(ctx context.Context, req *csi.NodeUnpublishVolumeRequest) (*csi.NodeUnpublishVolumeResponse, error) {
	targetPath := req.GetTargetPath()
	if len(targetPath) == 0 {
		return nil, status.Error(codes.InvalidArgument, "NodeUnpublishVolume target path must be provided")
	}

	if acquired := s.volumeLocks.TryAcquire(targetPath); !acquired {
		return nil, status.Errorf(codes.Aborted, util.VolumeOperationAlreadyExistsFmt, targetPath)
	}
	defer s.volumeLocks.Release(targetPath)

	// 1. Resolve Key using Pod UID and Volume ID
	// Extract Pod UID from target path
	podUID, err := extractPodUIDFromPath(targetPath)
	if err != nil {
		klog.Warningf("Likely legacy mount or error extracting pod UID for target %s: %v", targetPath, err)
		return s.nodeUnpublishLegacy(ctx, req)
	}

	key, err := s.findKeyForPod(podUID, req.GetVolumeId())
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	if key == "" {
		// If we can't find the key, check if it's already unmounted or valid legacy.
		isMnt, mntErr := s.isMounted(targetPath)
		if mntErr == nil && !isMnt {
			return &csi.NodeUnpublishVolumeResponse{}, nil
		}
		
		klog.Warningf("Global mount key not found for pod %s volume %s. Assuming legacy or already cleaned up.", podUID, req.GetVolumeId())
		return s.nodeUnpublishLegacy(ctx, req)
	}

	// 2. Unmount bind mount
	if err := mount.CleanupMountPoint(targetPath, s.mounter, false); err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to unmount target %s: %v", targetPath, err)
	}

	// 3. Remove Ref
	if err := removePodReference(key, podUID); err != nil {
		klog.Warningf("Failed to remove pod reference for key %s, pod %s: %v", key, podUID, err)
	}

	// 4. Garbage Collection (Global Unmount)
	if acquired := s.volumeLocks.TryAcquire(key); !acquired {
		// If we can't acquire lock, another pod might be mounting/unmounting.
		return nil, status.Errorf(codes.Aborted, "Operation exists for key %s", key)
	}
	defer s.volumeLocks.Release(key)

	// Check if any refs remain
	exists, err := globalMountRefExists(key)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to check refs for key %s: %v", key, err)
	}
	if !exists {
		klog.Infof("No refs remaining for key %s. Cleaning up global mount.", key)
		// Unmount Global Path
		mountPath := filepath.Join(GlobalMountRoot, key, "mount")
		if err := mount.CleanupMountPoint(mountPath, s.mounter, false); err != nil {
			return nil, status.Errorf(codes.Internal, "Failed to unmount global path %s: %v", mountPath, err)
		}
		// Remove Directory
		keyPath := filepath.Join(GlobalMountRoot, key)
		if err := os.RemoveAll(keyPath); err != nil {
			klog.Warningf("Failed to remove key directory %s: %v", keyPath, err)
		}
	}

	return &csi.NodeUnpublishVolumeResponse{}, nil
}

func (s *nodeServer) nodeUnpublishLegacy(ctx context.Context, req *csi.NodeUnpublishVolumeRequest) (*csi.NodeUnpublishVolumeResponse, error) {
	targetPath := req.GetTargetPath()
	klog.Infof("nodeUnpublishLegacy: cleaning up target %s", targetPath)
	if err := mount.CleanupMountPoint(targetPath, s.mounter, false); err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to unmount target %s: %v", targetPath, err)
	}
	return &csi.NodeUnpublishVolumeResponse{}, nil
}



func (s *nodeServer) NodeGetVolumeStats(_ context.Context, req *csi.NodeGetVolumeStatsRequest) (*csi.NodeGetVolumeStatsResponse, error) {
	if len(req.GetVolumeId()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "NodeGetVolumeStats volume ID was empty")
	}
	if len(req.GetVolumePath()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "NodeGetVolumeStats volume path was empty")
	}

	_, err := os.Lstat(req.GetVolumePath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, status.Errorf(codes.NotFound, "path %s does not exist", req.GetVolumePath())
		}

		return nil, status.Errorf(codes.Internal, "unknown error when stat on %s: %v", req.GetVolumePath(), err.Error())
	}

	available, capacity, used, inodesFree, inodes, inodesUsed, err := getFSStat(req.GetVolumePath())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get fs info on path %s: %v", req.GetVolumePath(), err.Error())
	}

	return &csi.NodeGetVolumeStatsResponse{
		Usage: []*csi.VolumeUsage{
			{
				Unit:      csi.VolumeUsage_BYTES,
				Available: available,
				Total:     capacity,
				Used:      used,
			},
			{
				Unit:      csi.VolumeUsage_INODES,
				Available: inodesFree,
				Total:     inodes,
				Used:      inodesUsed,
			},
		},
	}, nil
}

// isMounted checks if target is mounted. It does NOT return an error if target
// doesn't exist.
func (s *nodeServer) isMounted(target string) (bool, error) {
	/*
		Checking if it's a mount point using IsLikelyNotMountPoint. There are three different return values,
		1. true, err when the directory does not exist or corrupted.
		2. false, nil when the path is already mounted with a device.
		3. true, nil when the path is not mounted with any device.
	*/
	notMnt, err := s.mounter.IsLikelyNotMountPoint(target)
	if err != nil && !os.IsNotExist(err) {
		// Checking if the path exists and error is related to Corrupted Mount, in that case, the system could unmount and mount.
		_, pathErr := pathExists(target)
		if pathErr != nil && isCorruptedMnt(pathErr) {
			klog.V(4).Infof("Target path %q is a corrupted mount. Trying to unmount", target)
			if mntErr := s.mounter.Unmount(target); mntErr != nil {
				return false, status.Errorf(codes.Internal, "Unable to unmount the target %q : %v", target, mntErr)
			}
			// After successful unmount, the device is ready to be mounted.
			return false, nil
		}

		return false, status.Errorf(codes.Internal, "Could not check if %q is a mount point: %v, %v", target, err, pathErr)
	}

	// Do not return os.IsNotExist error. Other errors were handled above.  The
	// Existence of the target should be checked by the caller explicitly and
	// independently because sometimes prior to mount it is expected not to exist
	// (in Windows, the target must NOT exist before a symlink is created at it)
	// and in others it is an error (in Linux, the target mount directory must
	// exist before mount is called on it)
	if err != nil && os.IsNotExist(err) {
		klog.V(5).Infof("Target path %q does not exist", target)

		return false, nil
	}

	if !notMnt {
		klog.V(4).Infof("Target path %q is already mounted", target)
	}

	return !notMnt, nil
}

// hasMountWithSameFSName checks if there is an existing mountpoint on the node
// with the same Lustre filesystem name, regardless of the IP address.
func (s *nodeServer) hasMountWithSameFSName(fsname string) (bool, error) {
	mountedFS, err := s.mounter.List()
	if err != nil {
		return false, err
	}

	for _, m := range mountedFS {
		if extractFSName(m.Device) == fsname {
			klog.Infof("fsname %q already existed for mountpoint %+v", fsname, m)

			return true, nil
		}
	}

	return false, nil
}

// extractFSName extracts the Lustre fsname from the source string if it's in the format "%s@tcp:/%s".
// TODO(tyuchn): validate with lustre team whether the format is guaranteed.
func extractFSName(source string) string {
	// Regular expression to match format "hostname@tcp:/fsname".
	re := regexp.MustCompile(`.+@tcp:/([^/]+)`)

	// Check if the source matches the format
	if match := re.FindStringSubmatch(source); match != nil {
		// Return the part after "/@tcp:/" which is the fsname.
		return match[1]
	}

	return ""
}

func parseMountPoint(mountPoint string) (string, string, error) {
	match := mountPointRegex.FindStringSubmatch(mountPoint)
	if match == nil {
		return "", "", fmt.Errorf("invalid mountPoint format: %s, expected format: <ip>@tcp:/<fsname>", mountPoint)
	}

	return match[1], match[2], nil
}

func getFSStat(path string) (available, capacity, used, inodesFree, inodes, inodesUsed int64, err error) {
	statfs := &unix.Statfs_t{}
	err = unix.Statfs(path, statfs)
	if err != nil {
		err = fmt.Errorf("failed to get fs info on path %s: %w", path, err)

		return
	}

	// Available is blocks available * fragment size to root user
	available = int64(statfs.Bfree) * statfs.Bsize
	// Capacity is total block count * fragment size
	capacity = int64(statfs.Blocks) * statfs.Bsize
	// Usage is block being used * fragment size (aka block size).
	used = (int64(statfs.Blocks) - int64(statfs.Bfree)) * statfs.Bsize
	inodes = int64(statfs.Files)
	inodesFree = int64(statfs.Ffree)
	inodesUsed = inodes - inodesFree

	return
}

func hasOption(options []string, opt string) bool {
	for _, o := range options {
		if o == opt {
			return true
		}
	}

	return false
}

func makeDir(pathname string) error {
	err := os.MkdirAll(pathname, os.FileMode(0o755))
	if err != nil {
		if !os.IsExist(err) {
			return err
		}
	}

	return nil
}

// isCorruptedMnt return true if err is about corrupted mount point.
func isCorruptedMnt(err error) bool {
	return mount.IsCorruptedMnt(err)
}

func pathExists(path string) (bool, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return false, nil
	} else if err != nil {
		return false, err
	}

	return true, nil
}

// setVolumeOwnershipTopLevel modifies the top-level directory to be owned by
// fsGroup, and applies appropriate permissions. If fsGroup is nil, it does nothing.
func setVolumeOwnershipTopLevel(volumeID, dir, fsGroup string, readOnly bool) error {
	// Skip volume ownership change if the volume is read-only.
	if readOnly {
		klog.V(3).InfoS("Skipping setVolumeOwnershipTopLevel as volume is readOnly", "volume", volumeID, "path", dir)

		return nil
	}

	if fsGroup == "" {
		klog.V(3).InfoS("Skipping setVolumeOwnershipTopLevel as no fsGroup is provided", "volume", volumeID, "path", dir)

		return nil
	}

	klog.InfoS("NodePublishVolume starting setVolumeOwnershipTopLevel", "volume", volumeID, "path", dir, "fsGroup", fsGroup, "readOnly", readOnly)
	// Convert fsGroup string to integer.
	gid, err := strconv.Atoi(fsGroup)
	if err != nil {
		return fmt.Errorf("invalid fsGroup %s, must a numeric string: %w", fsGroup, err)
	}

	// Retrieve directory info.
	info, err := os.Lstat(dir)
	if err != nil {
		klog.ErrorS(err, "Failed to retrieve directory info", "path", dir, "volume", volumeID)

		return err
	}

	// Change ownership of the top-level directory.
	if err := os.Lchown(dir, -1, gid); err != nil {
		klog.ErrorS(err, "Failed to chown of directory", "path", dir, "volume", volumeID, "gid", gid)

		return err
	}

	// Apply permissions to the directory.
	mask := rwMask
	if readOnly {
		mask = roMask
	}
	mask |= os.ModeSetgid | execMask

	if err := os.Chmod(dir, info.Mode()|mask); err != nil {
		klog.ErrorS(err, "Failed to chmod of directory", "path", dir, "volume", volumeID, "mode", mask)

		return err
	}
	klog.InfoS("NodePublishVolume successfully changed ownership and permissions of top-level directory", "volume", volumeID, "path", dir, "fsGroup", fsGroup)

	return nil
}

// Global Mount Helpers

func computeHash(s string) string {
	h := sha256.New()
	h.Write([]byte(s))
	return hex.EncodeToString(h.Sum(nil))
}

func ensureGlobalDirectories(key string) error {
	mountPath := filepath.Join(GlobalMountRoot, key, "mount")
	refsPath := filepath.Join(GlobalMountRoot, key, "refs")
	if err := os.MkdirAll(mountPath, 0755); err != nil {
		return err
	}
	if err := os.MkdirAll(refsPath, 0755); err != nil {
		return err
	}
	return nil
}

func updateTokenFile(key, token string) error {
	tokenPath := filepath.Join(GlobalMountRoot, key, "token")
	// Write with 0600 to secure the token
	return os.WriteFile(tokenPath, []byte(token), 0600)
}

func parseToken(tokenJSON, audience string) (string, error) {
	// Format: {"audience": {"token": "...", "expirationTimestamp": "..."}}
	var tokens map[string]struct {
		Token               string `json:"token"`
		ExpirationTimestamp string `json:"expirationTimestamp"`
	}
	if err := json.Unmarshal([]byte(tokenJSON), &tokens); err != nil {
		return "", err
	}
	// We asked for audience "{PROJECT_ID}.svc.id.goog"
	if t, ok := tokens[audience]; ok {
		return t.Token, nil
	}
	return "", fmt.Errorf("token for audience %q not found", audience)
}

func addPodReference(key, podUID, volumeID string) error {
	refPath := filepath.Join(GlobalMountRoot, key, "refs", podUID)
	// Write volumeID to the ref file to verify ownership during unpublish
	return os.WriteFile(refPath, []byte(volumeID), 0644)
}

func removePodReference(key, podUID string) error {
	refPath := filepath.Join(GlobalMountRoot, key, "refs", podUID)
	if err := os.Remove(refPath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return nil
}

func globalMountRefExists(key string) (bool, error) {
	refsPath := filepath.Join(GlobalMountRoot, key, "refs")
	entries, err := os.ReadDir(refsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return len(entries) > 0, nil
}

func extractPodUIDFromPath(path string) (string, error) {
	// Path pattern: .../pods/<UID>/volumes/...
	re := regexp.MustCompile(`/pods/([^/]+)/volumes`)
	matches := re.FindStringSubmatch(path)
	if len(matches) < 2 {
		return "", fmt.Errorf("could not extract pod UID from path %s", path)
	}
	return matches[1], nil
}

func (s *nodeServer) findKeyForPod(podUID, volumeID string) (string, error) {
	// Search /var/lib/lustre/mounts/*/refs/<POD_UID>
	// We need to iterate over all keys in GlobalMountRoot
	entries, err := os.ReadDir(GlobalMountRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		key := entry.Name()
		refPath := filepath.Join(GlobalMountRoot, key, "refs", podUID)
		
		// Check if ref file exists
		content, err := os.ReadFile(refPath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			klog.Warningf("Failed to read ref file %s: %v", refPath, err)
			continue
		}

		// Check if VolumeID matches
		if strings.TrimSpace(string(content)) == volumeID {
			return key, nil
		}
	}

	return "", nil
}
