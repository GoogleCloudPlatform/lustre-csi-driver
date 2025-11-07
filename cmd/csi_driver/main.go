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
	"github.com/GoogleCloudPlatform/lustre-csi-driver/pkg/metrics"
	"k8s.io/klog/v2"
	"k8s.io/mount-utils"
)

var (
	endpoint               = flag.String("endpoint", "unix:/tmp/csi.sock", "CSI endpoint")
	nodeID                 = flag.String("nodeid", "", "node id")
	runController          = flag.Bool("controller", false, "run controller service")
	runNode                = flag.Bool("node", false, "run node service")
	httpEndpoint           = flag.String("http-endpoint", "", "The TCP network address where the prometheus metrics endpoint will listen (example: `:8080`). The default is empty string, which means metrics endpoint is disabled.")
	metricsPath            = flag.String("metrics-path", "/metrics", "The HTTP path where prometheus metrics will be exposed. Default is `/metrics`.")
	lustreAPIEndpoint      = flag.String("lustre-endpoint", "", "Lustre API service endpoint, supported values are autopush, staging and prod.")
	cloudConfigFilePath    = flag.String("cloud-config", "", "Path to GCE cloud provider config")
	enableLegacyLustrePort = flag.Bool("enable-legacy-lustre-port", false, "If set to true, the CSI driver controller will provision Lustre instance with the gkeSupportEnabled flag")
	disableKmodInstall     = flag.Bool("disable-kmod-install", true, "If true, Lustre CSI driver will not install kmod and user will need to manage Lustre kmod independently.")

	// dkmsArgsOverride contains custom arguments for cos-dkms installation provided by user.
	dkmsArgsOverride stringSlice

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
	flag.Var(&dkmsArgsOverride, "cos-dkms-args-override", "Custom override cos-dkms install arguments. (Can be specified multiple times).")
	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if !*disableKmodInstall {
		if err := kmod.InstallLustreKmod(ctx, *nodeID, *enableLegacyLustrePort, dkmsArgsOverride); err != nil {
			klog.Fatalf("Kmod install failure: %v", err)
		}
	}

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
	}

	if *runController {
		if *httpEndpoint != "" && metrics.IsGKEComponentVersionAvailable() {
			mm := metrics.NewMetricsManager()
			mm.InitializeHTTPHandler(*httpEndpoint, *metricsPath)
			if err := mm.EmitGKEComponentVersion(); err != nil {
				klog.Fatalf("Failed to emit GKE component version: %v", err)
			}
		}

		if *lustreAPIEndpoint == "" {
			*lustreAPIEndpoint = "prod"
		}
		cloudProvider, err := lustre.NewCloud(ctx, *cloudConfigFilePath, version, *lustreAPIEndpoint)
		if err != nil {
			klog.Fatalf("Failed to initialize cloud provider: %v", err)
		}
		config.Cloud = cloudProvider
	}

	lustreDriver, err := driver.NewLustreDriver(config)
	if err != nil {
		klog.Fatalf("Failed to initialize Lustre CSI Driver: %v", err)
	}

	klog.Infof("Running Lustre CSI driver version %v", version)
	lustreDriver.Run(*endpoint)
}
