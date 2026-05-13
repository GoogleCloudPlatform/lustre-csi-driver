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

package main

import (
	"context"
	"flag"
	"fmt"
	"strings"

	"github.com/GoogleCloudPlatform/lustre-csi-driver/pkg/cloud_provider/metadata"
	kmod "github.com/GoogleCloudPlatform/lustre-csi-driver/pkg/kmod_installer"
	"github.com/GoogleCloudPlatform/lustre-csi-driver/pkg/network"
	"k8s.io/klog/v2"
)

var (
	nodeID                 = flag.String("nodeid", "", "Node ID")
	enableLegacyLustrePort = flag.Bool("enable-legacy-lustre-port", false, "If set to true, configure the legacy Lustre LNet port")
	disableMultiNIC        = flag.Bool("disable-multi-nic", false, "If set to true, multi-NIC support is disabled and the driver will only use the default NIC (eth0)")

	// customModuleArgs contains custom module-args arguments for cos-dkms installation provided by user.
	customModuleArgs stringSlice
)

type stringSlice []string

func (s *stringSlice) String() string {
	return strings.Join(*s, ", ")
}

func (s *stringSlice) Set(value string) error {
	*s = append(*s, value)
	return nil
}

func main() {
	klog.InitFlags(nil)
	flag.Var(&customModuleArgs, "custom-module-args", "Custom module-args for cos-dkms install command. (Can be specified multiple times).")
	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if *nodeID == "" {
		klog.Fatalf("nodeid flag cannot be empty")
	}

	klog.Infof("Starting Lustre kernel module installer for node %s", *nodeID)

	meta, err := metadata.NewMetadataService(ctx)
	if err != nil {
		klog.Fatalf("Failed to set up metadata service: %v", err)
	}

	netlinker := network.NewNetlink()
	nodeClient := network.NewK8sClient()
	networkIntf := network.Manager(netlinker, nodeClient, meta)

	nics, err := networkIntf.GetStandardNICs()
	if err != nil {
		klog.Fatalf("Failed to get standard NIC names: %v", err)
	}
	if len(nics) == 0 {
		klog.Fatal("No standard NICs (gve, idpf, or virtio_net) found")
	}

	effectiveDisableMultiNIC, err := networkIntf.CheckDisableMultiNIC(ctx, *nodeID, nics, *disableMultiNIC)
	if err != nil {
		klog.Fatalf("Failed to check multi-NIC status: %v", err)
	}

	// Check if the kernel modules are already installed.
	isInstalled, err := kmod.IsLustreKmodInstalled(*enableLegacyLustrePort)
	if err != nil {
		klog.Fatalf("Lustre kernel module installation check failed: %v", err)
	}

	if isInstalled {
		// Check if the current configuration matches the expectation.
		expectedNetwork := kmod.DefaultLnetNetwork
		if !effectiveDisableMultiNIC {
			expectedNetwork = fmt.Sprintf("tcp0(%s)", strings.Join(nics, ","))
		}

		if _, err = kmod.GetLnetNetwork(expectedNetwork); err != nil {
			klog.Fatalf("Failed to get LNET network parameters: %v", err)
		}

		klog.Info("Lustre kernel module is already installed. Exiting successfully.")
		return
	}

	klog.Info("Lustre kernel module is not installed. Proceeding with installation.")

	hostOS, err := kmod.HostOSFromNodeLabel(ctx, *nodeID, nodeClient)
	if err != nil {
		klog.Fatalf("Failed to read OS Host Info: %v", err)
	}

	switch hostOS {
	case "cos":
		err = kmod.InstallLustreKmodOnCos(ctx, *enableLegacyLustrePort, customModuleArgs, nics, effectiveDisableMultiNIC)
		if err != nil {
			klog.Fatalf("Failed to install lustre kernel modules on COS: %v", err)
		}
	case "ubuntu":
		err = kmod.InstallLustreKmodOnUbuntu(ctx, *enableLegacyLustrePort, customModuleArgs, nics, effectiveDisableMultiNIC)
		if err != nil {
			klog.Fatalf("Failed to install lustre kernel modules on Ubuntu: %v", err)
		}
	case "windows":
		klog.Warning("Lustre kernel modules are not supported on Windows nodes.")
	default:
		klog.Fatalf("Unsupported or unknown Host OS: %q. Cannot perform Lustre kernel module installation", hostOS)
	}

	klog.Info("Successfully installed Lustre kernel modules.")
}
