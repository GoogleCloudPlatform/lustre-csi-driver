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

package sanitytest

import (
	"os"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/lustre-csi-driver/pkg/cloud_provider/lustre"
	"github.com/GoogleCloudPlatform/lustre-csi-driver/pkg/cloud_provider/metadata"
	driver "github.com/GoogleCloudPlatform/lustre-csi-driver/pkg/csi_driver"
	sanity "github.com/kubernetes-csi/csi-test/v5/pkg/sanity"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"k8s.io/mount-utils"
)

const (
	driverName                    = "test-driver"
	driverVersion                 = "test-driver-version"
	nodeID                        = "io.kubernetes.storage.mock"
	endpoint                      = "unix:/tmp/csi.sock"
	tmpDir                        = "/tmp/csi"
	targetPath                    = "/tmp/csi/target"
	stagingPath                   = "/tmp/csi/staging"
	paramPerUnitStorageThroughput = "perUnitStorageThroughput"

	GiB = 1024 * 1024 * 1024
)

func TestSanity(t *testing.T) {
	t.Parallel()
	// Set up temp working dir
	cleanUp := func() {
		if err := os.RemoveAll(tmpDir); err != nil {
			t.Fatalf("Failed to clean up sanity temp working dir %s: %v", tmpDir, err)
		}
	}
	err := os.MkdirAll(tmpDir, 0o755)
	if err != nil {
		if strings.Contains(err.Error(), "file exists") {
			cleanUp()
		} else {
			t.Fatalf("Failed to create sanity temp working dir %s: %v", tmpDir, err)
		}
	}
	defer cleanUp()

	// Set up driver and env.
	mounter := &mount.FakeMounter{MountPoints: []mount.MountPoint{}}
	cloudProvider, err := lustre.NewFakeCloud()
	if err != nil {
		t.Fatalf("Failed to get cloud provider: %v", err)
	}
	meta, err := metadata.NewFakeService()
	if err != nil {
		t.Fatalf("Failed to get metadata service: %v", err)
	}
	driverConfig := &driver.LustreDriverConfig{
		Name:            driverName,
		Version:         driverVersion,
		NodeID:          nodeID,
		RunController:   true,
		RunNode:         true,
		Mounter:         mounter,
		MetadataService: meta,
		Cloud:           cloudProvider,
	}

	lustreDriver, err := driver.NewLustreDriver(driverConfig)
	if err != nil {
		t.Fatalf("Failed to initialize Lustre CSI Driver: %v", err)
	}

	go func() {
		lustreDriver.Run(endpoint)
	}()

	// Run test
	testConfig := sanity.TestConfig{
		TargetPath:     targetPath,
		StagingPath:    stagingPath,
		Address:        endpoint,
		DialOptions:    []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())},
		IDGen:          &sanity.DefaultIDGenerator{},
		TestVolumeSize: int64(18000 * GiB),
		TestVolumeParameters: map[string]string{
			paramPerUnitStorageThroughput: "1000",
		},
	}
	sanity.Test(t, testConfig)
}
