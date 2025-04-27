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
	"errors"
	"fmt"

	"github.com/GoogleCloudPlatform/lustre-csi-driver/pkg/cloud_provider/lustre"
	"github.com/GoogleCloudPlatform/lustre-csi-driver/pkg/cloud_provider/metadata"
	csi "github.com/container-storage-interface/spec/lib/go/csi"
	"k8s.io/klog/v2"
	mount "k8s.io/mount-utils"
)

const DefaultName = "lustre.csi.storage.gke.io"

type LustreDriverConfig struct {
	Name                   string // Driver name
	Version                string // Driver version
	NodeID                 string // Node name
	RunController          bool   // Run CSI controller service
	RunNode                bool   // Run CSI node service
	Mounter                mount.Interface
	MetadataService        metadata.Service
	Cloud                  *lustre.Cloud
	EnableLegacyLustrePort bool
}

type LustreDriver struct {
	config *LustreDriverConfig

	// CSI RPC servers
	ids csi.IdentityServer
	ns  csi.NodeServer
	cs  csi.ControllerServer

	// Plugin capabilities
	vcap  map[csi.VolumeCapability_AccessMode_Mode]*csi.VolumeCapability_AccessMode
	cscap []*csi.ControllerServiceCapability
	nscap []*csi.NodeServiceCapability
}

func NewLustreDriver(config *LustreDriverConfig) (*LustreDriver, error) {
	if config.Name == "" {
		return nil, errors.New("driver name missing")
	}
	if config.Version == "" {
		return nil, errors.New("driver version missing")
	}
	if !config.RunController && !config.RunNode {
		return nil, errors.New("must run at least one controller or node service")
	}

	driver := &LustreDriver{
		config: config,
		vcap:   map[csi.VolumeCapability_AccessMode_Mode]*csi.VolumeCapability_AccessMode{},
	}

	vcam := []csi.VolumeCapability_AccessMode_Mode{
		csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
		csi.VolumeCapability_AccessMode_SINGLE_NODE_READER_ONLY,
		csi.VolumeCapability_AccessMode_MULTI_NODE_READER_ONLY,
		csi.VolumeCapability_AccessMode_MULTI_NODE_SINGLE_WRITER,
		csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER,
	}
	driver.addVolumeCapabilityAccessModes(vcam)

	// Setup RPC servers
	driver.ids = newIdentityServer(driver)
	if config.RunNode {
		nscap := []csi.NodeServiceCapability_RPC_Type{
			csi.NodeServiceCapability_RPC_STAGE_UNSTAGE_VOLUME,
			csi.NodeServiceCapability_RPC_VOLUME_MOUNT_GROUP,
			csi.NodeServiceCapability_RPC_GET_VOLUME_STATS,
		}
		driver.ns = newNodeServer(driver, config.Mounter)
		driver.addNodeServiceCapabilities(nscap)
	}

	if config.RunController {
		csc := []csi.ControllerServiceCapability_RPC_Type{
			csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME,
		}
		driver.addControllerServiceCapabilities(csc)

		// Configure controller server
		driver.cs = newControllerServer(driver, config.Cloud)
	}

	return driver, nil
}

func (driver *LustreDriver) addVolumeCapabilityAccessModes(vc []csi.VolumeCapability_AccessMode_Mode) {
	for _, c := range vc {
		klog.Infof("Enabling volume access mode: %v", c.String())
		mode := NewVolumeCapabilityAccessMode(c)
		driver.vcap[mode.GetMode()] = mode
	}
}

func (driver *LustreDriver) validateVolumeCapabilities(caps []*csi.VolumeCapability) error {
	if len(caps) == 0 {
		return errors.New("volume capabilities must be provided")
	}

	for _, c := range caps {
		if err := driver.validateVolumeCapability(c); err != nil {
			return err
		}
	}

	return nil
}

func (driver *LustreDriver) validateVolumeCapability(c *csi.VolumeCapability) error {
	if c == nil {
		return errors.New("volume capability must be provided")
	}

	// Validate access mode
	accessMode := c.GetAccessMode()
	if accessMode == nil {
		return errors.New("volume capability access mode not set")
	}
	if driver.vcap[accessMode.GetMode()] == nil {
		return fmt.Errorf("driver does not support access mode: %v", accessMode.GetMode().String())
	}

	// Validate access type
	accessType := c.GetAccessType()
	if accessType == nil {
		return errors.New("volume capability access type not set")
	}
	mountType := c.GetMount()
	if mountType == nil {
		return errors.New("driver only supports mount access type volume capability")
	}

	return nil
}

func (driver *LustreDriver) addControllerServiceCapabilities(cl []csi.ControllerServiceCapability_RPC_Type) {
	csc := []*csi.ControllerServiceCapability{}
	for _, c := range cl {
		klog.Infof("Enabling controller service capability: %v", c.String())
		csc = append(csc, NewControllerServiceCapability(c))
	}
	driver.cscap = csc
}

func (driver *LustreDriver) addNodeServiceCapabilities(nl []csi.NodeServiceCapability_RPC_Type) {
	nsc := []*csi.NodeServiceCapability{}
	for _, n := range nl {
		klog.Infof("Enabling node service capability: %v", n.String())
		nsc = append(nsc, NewNodeServiceCapability(n))
	}
	driver.nscap = nsc
}

func (driver *LustreDriver) Run(endpoint string) {
	klog.Infof("Running driver: %v", driver.config.Name)

	s := NewNonBlockingGRPCServer()
	s.Start(endpoint, driver.ids, driver.cs, driver.ns)
	s.Wait()
}
