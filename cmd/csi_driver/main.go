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
	"strings"

	"github.com/GoogleCloudPlatform/lustre-csi-driver/pkg/cloud_provider/lustre"
	"github.com/GoogleCloudPlatform/lustre-csi-driver/pkg/cloud_provider/metadata"
	driver "github.com/GoogleCloudPlatform/lustre-csi-driver/pkg/csi_driver"
	kmod "github.com/GoogleCloudPlatform/lustre-csi-driver/pkg/kmod_installer"
	"github.com/GoogleCloudPlatform/lustre-csi-driver/pkg/labelcontroller"
	"github.com/GoogleCloudPlatform/lustre-csi-driver/pkg/metrics"
	"github.com/GoogleCloudPlatform/lustre-csi-driver/pkg/network"
	"k8s.io/klog/v2"
	"k8s.io/mount-utils"
)

var (
	endpoint                = flag.String("endpoint", "unix:/tmp/csi.sock", "CSI endpoint")
	nodeID                  = flag.String("nodeid", "", "node id")
	runController           = flag.Bool("controller", false, "run controller service")
	runNode                 = flag.Bool("node", false, "run node service")
	httpEndpoint            = flag.String("http-endpoint", "", "The TCP network address where the prometheus metrics endpoint will listen (example: `:8080`). The default is empty string, which means metrics endpoint is disabled.")
	metricsPath             = flag.String("metrics-path", "/metrics", "The HTTP path where prometheus metrics will be exposed. Default is `/metrics`.")
	lustreAPIEndpoint       = flag.String("lustre-endpoint", "", "Lustre API service endpoint, supported values are autopush, staging and prod.")
	cloudConfigFilePath     = flag.String("cloud-config", "", "Path to GCE cloud provider config")
	enableLegacyLustrePort  = flag.Bool("enable-legacy-lustre-port", false, "If set to true, the CSI driver controller will provision Lustre instance with the gkeSupportEnabled flag")
	disableMultiNIC         = flag.Bool("disable-multi-nic", false, "If set to true, multi-NIC support is disabled and the driver will only use the default NIC (eth0).")
	disableKmodInstall      = flag.Bool("disable-kmod-install", true, "If true, Lustre CSI driver will not install kmod and user will need to manage Lustre kmod independently.")
	enableLabelController   = flag.Bool("enable-label-controller", true, "If true, the label controller will be started.")
	leaderElectionNamespace = flag.String("leader-election-namespace", "", "Namespace where the leader election resource will be created. Required for out-of-cluster deployments.")

	// customModuleArgs contains custom module-args arguments for cos-dkms installation provided by user.
	customModuleArgs stringSlice

	// These are set at compile time.
	version = "unknown"
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
	mm := metrics.NewMetricsManager()
	if *httpEndpoint != "" {
		mm.InitializeHTTPHandler(*httpEndpoint, *metricsPath)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	netlinker := network.NewNetlink()
	nodeClient := network.NewK8sClient()
	networkIntf := network.Manager(netlinker, nodeClient)

	config := &driver.LustreDriverConfig{
		Name:                   driver.DefaultName,
		Version:                version,
		RunController:          *runController,
		RunNode:                *runNode,
		EnableLegacyLustrePort: *enableLegacyLustrePort,
	}

	if *runNode {
		if *nodeID == "" {
			klog.Fatalf("NodeID cannot be empty for node service")
		}
		config.NodeID = *nodeID

		meta, err := metadata.NewMetadataService(ctx)
		if err != nil {
			klog.Fatalf("Failed to set up metadata service: %v", err)
		}
		klog.Infof("Metadata service setup: %+v", meta)
		config.MetadataService = meta

		config.Mounter = mount.New("")
		nics, err := networkIntf.GetGvnicNames()
		if err != nil {
			klog.Fatalf("Error getting nic names: %v", err)
		}
		if len(nics) == 0 {
			klog.Fatal("No nics with eth prefix found")
		}

		disableMultiNIC, err := networkIntf.CheckDisableMultiNic(ctx, *nodeID, nics, *disableMultiNIC)
		if err != nil {
			klog.Fatal(err)
		}
		if !disableMultiNIC && metrics.IsGKEComponentVersionAvailable() {
			if err := mm.EmitUsingMultiNic(); err != nil {
				klog.Fatalf("Failed to emit GKE component version: %v", err)
			}
		}
		if !*disableKmodInstall {
			if err := kmod.InstallLustreKmod(ctx, *enableLegacyLustrePort, customModuleArgs, nics, disableMultiNIC); err != nil {
				klog.Fatalf("Kmod install failure: %v", err)
			}
		}
		// additional NICs are any additional NICs that are not default (eth0).
		// These NICs will additional setup for multi nic feature.
		additionalNics := []string{}
		for _, nic := range nics {
			if nic != "eth0" {
				additionalNics = append(additionalNics, nic)
			}
		}
		klog.V(4).Infof("Additional nic(s) %v on Node %v", additionalNics, *nodeID)
		config.AdditionalNics = additionalNics
		config.DisableMultiNIC = disableMultiNIC
	}

	if *runController {
		if metrics.IsGKEComponentVersionAvailable() {
			if err := mm.EmitGKEComponentVersion(); err != nil {
				klog.Fatalf("Failed to emit GKE component version: %v", err)
			}
			mm.RegisterSuccessfullyLabeledVolumeMetric()
		}

		if *lustreAPIEndpoint == "" {
			*lustreAPIEndpoint = "prod"
		}
		cloudProvider, err := lustre.NewCloud(ctx, *cloudConfigFilePath, version, *lustreAPIEndpoint)
		if err != nil {
			klog.Fatalf("Failed to initialize cloud provider: %v", err)
		}
		config.Cloud = cloudProvider
		if *enableLabelController {
			go func() {
				// Pass empty string for kubeconfig to let controller-runtime handle the flag
				if err := labelcontroller.Start(ctx, cloudProvider, &mm, *leaderElectionNamespace); err != nil {
					klog.Errorf("Label controller failed: %v", err)
				}
			}()
		}
	}

	lustreDriver, err := driver.NewLustreDriver(config)
	if err != nil {
		klog.Fatalf("Failed to initialize Lustre CSI Driver: %v", err)
	}

	klog.Infof("Running Lustre CSI driver version %v", version)
	lustreDriver.Run(*endpoint)
}
