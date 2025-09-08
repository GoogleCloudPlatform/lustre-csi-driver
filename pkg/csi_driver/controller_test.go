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
	"reflect"
	"testing"

	"github.com/GoogleCloudPlatform/lustre-csi-driver/pkg/cloud_provider/lustre"
	csi "github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/google/go-cmp/cmp"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/testing/protocmp"
)

const (
	testCSIVolume = "test-instance"
	testFSName    = "fake-fs"
)

func initTestController(t *testing.T) csi.ControllerServer {
	t.Helper()
	driver := initTestDriver(t)
	cloudProvider, err := lustre.NewFakeCloud()
	if err != nil {
		t.Fatalf("Failed to get cloud provider: %v", err)
	}

	return newControllerServer(driver, cloudProvider)
}

func TestCreateVolume(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		req       *csi.CreateVolumeRequest
		resp      *csi.CreateVolumeResponse
		expectErr error
	}{
		{
			name: "valid defaults",
			req: &csi.CreateVolumeRequest{
				Name: testCSIVolume,
				VolumeCapabilities: []*csi.VolumeCapability{
					{
						AccessType: &csi.VolumeCapability_Mount{
							Mount: &csi.VolumeCapability_MountVolume{},
						},
						AccessMode: &csi.VolumeCapability_AccessMode{
							Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
						},
					},
				},
				Parameters: map[string]string{
					keyFilesystem:               testFSName,
					keyPerUnitStorageThroughput: "1000",
				},
			},
			resp: &csi.CreateVolumeResponse{
				Volume: &csi.Volume{
					CapacityBytes: MinVolumeSizeBytes,
					VolumeId:      testVolumeID,
					VolumeContext: map[string]string{
						keyFilesystem: testFSName,
						keyInstanceIP: testIP,
					},
				},
			},
		},
		{
			name: "invalid PerUnitStorageThroughput",
			req: &csi.CreateVolumeRequest{
				Name: testCSIVolume,
				VolumeCapabilities: []*csi.VolumeCapability{
					{
						AccessType: &csi.VolumeCapability_Mount{
							Mount: &csi.VolumeCapability_MountVolume{},
						},
						AccessMode: &csi.VolumeCapability_AccessMode{
							Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
						},
					},
				},
				Parameters: map[string]string{
					keyFilesystem:               testFSName,
					keyPerUnitStorageThroughput: "100",
				},
			},
			expectErr: status.Error(codes.InvalidArgument, "invalid perUnitStorageThroughput 100, must be 1000, 500, 250 or 125"),
		},
		{
			name: "perUnitStorageThroughput not specified",
			req: &csi.CreateVolumeRequest{
				Name: testCSIVolume,
				VolumeCapabilities: []*csi.VolumeCapability{
					{
						AccessType: &csi.VolumeCapability_Mount{
							Mount: &csi.VolumeCapability_MountVolume{},
						},
						AccessMode: &csi.VolumeCapability_AccessMode{
							Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
						},
					},
				},
				Parameters: map[string]string{
					keyFilesystem: testFSName,
				},
			},
			expectErr: status.Error(codes.InvalidArgument, "parameter 'perUnitStorageThroughput' is required; supported values are 1000, 500, 250, or 125"),
		},
		{
			name: "empty name",
			req: &csi.CreateVolumeRequest{
				VolumeCapabilities: []*csi.VolumeCapability{
					{
						AccessType: &csi.VolumeCapability_Mount{
							Mount: &csi.VolumeCapability_MountVolume{},
						},
						AccessMode: &csi.VolumeCapability_AccessMode{
							Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
						},
					},
				},
			},
			expectErr: status.Error(codes.InvalidArgument, "CreateVolume name must be provided"),
		},
		{
			name: "invalid volume capability",
			req: &csi.CreateVolumeRequest{
				Name: testCSIVolume,
				VolumeCapabilities: []*csi.VolumeCapability{
					{
						AccessMode: &csi.VolumeCapability_AccessMode{
							Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
						},
					},
				},
			},
			expectErr: status.Error(codes.InvalidArgument, "volume capability access type not set"),
		},
		{
			name: "instance existed - state active",
			req: &csi.CreateVolumeRequest{
				Name: "existing-instance",
				VolumeCapabilities: []*csi.VolumeCapability{
					{
						AccessType: &csi.VolumeCapability_Mount{
							Mount: &csi.VolumeCapability_MountVolume{},
						},
						AccessMode: &csi.VolumeCapability_AccessMode{
							Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
						},
					},
				},
				Parameters: map[string]string{
					keyFilesystem:               "existing",
					keyPerUnitStorageThroughput: "1000",
				},
			},
			resp: &csi.CreateVolumeResponse{
				Volume: &csi.Volume{
					CapacityBytes: MinVolumeSizeBytes,
					VolumeId:      "test-project/us-central1-a/existing-instance",
					VolumeContext: map[string]string{
						keyFilesystem: "existing",
						keyInstanceIP: "192.168.1.1",
					},
				},
			},
		},
		{
			name: "instance existed - state creating",
			req: &csi.CreateVolumeRequest{
				Name: "creating-instance",
				VolumeCapabilities: []*csi.VolumeCapability{
					{
						AccessType: &csi.VolumeCapability_Mount{
							Mount: &csi.VolumeCapability_MountVolume{},
						},
						AccessMode: &csi.VolumeCapability_AccessMode{
							Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
						},
					},
				},
				Parameters: map[string]string{
					keyFilesystem:               "creating",
					keyPerUnitStorageThroughput: "1000",
				},
			},
			expectErr: status.Error(codes.DeadlineExceeded, "Volume creating-instance not ready, current state: CREATING"),
		},
		{
			name: "instance existed - state unknown",
			req: &csi.CreateVolumeRequest{
				Name: "unknown-instance",
				VolumeCapabilities: []*csi.VolumeCapability{
					{
						AccessType: &csi.VolumeCapability_Mount{
							Mount: &csi.VolumeCapability_MountVolume{},
						},
						AccessMode: &csi.VolumeCapability_AccessMode{
							Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
						},
					},
				},
				Parameters: map[string]string{
					keyFilesystem:               "unknown",
					keyPerUnitStorageThroughput: "1000",
				},
			},
			expectErr: status.Error(codes.Unavailable, "Volume unknown-instance not ready, current state: STATE_UNSPECIFIED"),
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			cs := initTestController(t)
			resp, err := cs.CreateVolume(t.Context(), test.req)
			if test.expectErr == nil && err != nil {
				t.Errorf("test %q failed:\ngot error %q,\nexpected error nil", test.name, err)
			}
			if test.expectErr != nil && !errors.Is(err, test.expectErr) {
				t.Errorf("test %q failed:\ngot error %q,\nexpected error %q", test.name, err, test.expectErr)
			}
			if !cmp.Equal(resp, test.resp, protocmp.Transform()) {
				t.Errorf("test %q failed:\ngot resp %+v,\nexpected %+v, diff: %s", test.name, resp, test.resp, cmp.Diff(resp, test.resp, protocmp.Transform()))
			}
		})
	}
}

func TestDeleteVolume(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		req       *csi.DeleteVolumeRequest
		resp      *csi.DeleteVolumeResponse
		expectErr error
	}{
		{
			name: "valid",
			req: &csi.DeleteVolumeRequest{
				VolumeId: testVolumeID,
			},
			resp: &csi.DeleteVolumeResponse{},
		},
		{
			name: "invalid id",
			req: &csi.DeleteVolumeRequest{
				VolumeId: testVolumeID + "/foo",
			},
			resp: &csi.DeleteVolumeResponse{},
		},
		{
			name:      "empty id",
			req:       &csi.DeleteVolumeRequest{},
			expectErr: status.Error(codes.InvalidArgument, "DeleteVolume volumeID must be provided"),
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			cs := initTestController(t)
			resp, err := cs.DeleteVolume(t.Context(), test.req)
			if test.expectErr == nil && err != nil {
				t.Errorf("test %q failed:\ngot error %q,\nexpected error nil", test.name, err)
			}
			if test.expectErr != nil && !errors.Is(err, test.expectErr) {
				t.Errorf("test %q failed:\ngot error %q,\nexpected error %q", test.name, err, test.expectErr)
			}
			if !reflect.DeepEqual(resp, test.resp) {
				t.Errorf("test %q failed:\ngot resp %+v,\nexpected resp %+v", test.name, resp, test.resp)
			}
		})
	}
}
