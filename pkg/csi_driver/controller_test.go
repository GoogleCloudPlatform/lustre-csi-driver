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
	"github.com/GoogleCloudPlatform/lustre-csi-driver/pkg/util"
	csi "github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/google/go-cmp/cmp"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/testing/protocmp"
)

const (
	testCSIVolume            = "test-instance"
	testFSName               = "fake-fs"
	existingInstanceVolumeID = "test-project/us-central1-a/existing-instance"
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

func TestControllerExpandVolume(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		req       *csi.ControllerExpandVolumeRequest
		resp      *csi.ControllerExpandVolumeResponse
		expectErr error
	}{
		{
			name: "volume not found",
			req: &csi.ControllerExpandVolumeRequest{
				VolumeId: "test-project/us-central1-a/not-found-instance",
				CapacityRange: &csi.CapacityRange{
					RequiredBytes: MinVolumeSizeBytes,
				},
			},
			expectErr: status.Error(codes.NotFound, "googleapi: got HTTP response code 404 with body: "),
		},
		{
			name: "invalid volume Id",
			req: &csi.ControllerExpandVolumeRequest{
				VolumeId: "invalid/id",
				CapacityRange: &csi.CapacityRange{
					RequiredBytes: MinVolumeSizeBytes,
				},
			},
			expectErr: status.Error(codes.InvalidArgument, "invalid volume ID \"invalid/id\": expected format PROJECT_ID/LOCATION/INSTANCE_NAME"),
		},
		{
			name: "no volume Id",
			req: &csi.ControllerExpandVolumeRequest{
				CapacityRange: &csi.CapacityRange{
					RequiredBytes: MinVolumeSizeBytes,
				},
			},
			expectErr: status.Error(codes.InvalidArgument, "ControllerExpandVolume volumeID must be provided"),
		},
		{
			name: "valid expansion request",
			req: &csi.ControllerExpandVolumeRequest{
				VolumeId: existingInstanceVolumeID,
				CapacityRange: &csi.CapacityRange{
					RequiredBytes: 18000 * util.Gib,
				},
			},
			resp: &csi.ControllerExpandVolumeResponse{
				CapacityBytes:         18000 * util.Gib,
				NodeExpansionRequired: false,
			},
		},
		{
			name: "expansion request with smaller size",
			req: &csi.ControllerExpandVolumeRequest{
				VolumeId: existingInstanceVolumeID,
				CapacityRange: &csi.CapacityRange{
					RequiredBytes: 2000 * util.Gib,
				},
			},
			resp: &csi.ControllerExpandVolumeResponse{
				CapacityBytes:         MinVolumeSizeBytes,
				NodeExpansionRequired: false,
			},
		},
		{
			name: "expansion request with existing size",
			req: &csi.ControllerExpandVolumeRequest{
				VolumeId: existingInstanceVolumeID,
				CapacityRange: &csi.CapacityRange{
					RequiredBytes: MinVolumeSizeBytes,
				},
			},
			resp: &csi.ControllerExpandVolumeResponse{
				CapacityBytes:         MinVolumeSizeBytes,
				NodeExpansionRequired: false,
			},
		},
		{
			name: "update in progress",
			req: &csi.ControllerExpandVolumeRequest{
				VolumeId: "test-project/us-central1-a/updating-instance",
				CapacityRange: &csi.CapacityRange{
					RequiredBytes: 2 * MinVolumeSizeBytes,
				},
			},
			expectErr: status.Error(codes.DeadlineExceeded, "Expansion for volume \"test-project/us-central1-a/updating-instance\" to new capacity 18000 GiB is already in progress"),
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			cs := initTestController(t)
			resp, err := cs.ControllerExpandVolume(t.Context(), test.req)
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

func TestGetRequestCapacity(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		capRange  *csi.CapacityRange
		expectCap int64
		expectErr error
	}{
		{
			name:      "nil capacity range",
			capRange:  nil,
			expectCap: MinVolumeSizeBytes,
		},
		{
			name: "limit bytes less than required bytes",
			capRange: &csi.CapacityRange{
				RequiredBytes: 2 * MinVolumeSizeBytes,
				LimitBytes:    MinVolumeSizeBytes,
			},
			expectErr: errors.New("limit bytes is less than required bytes"),
		},
		{
			name: "limit bytes less than minimum volume size",
			capRange: &csi.CapacityRange{
				LimitBytes: MinVolumeSizeBytes - 1,
			},
			expectErr: errors.New("limit bytes is less than minimum volume size"),
		},
		{
			name: "required bytes set",
			capRange: &csi.CapacityRange{
				RequiredBytes: 2 * MinVolumeSizeBytes,
			},
			expectCap: 2 * MinVolumeSizeBytes,
		},
		{
			name: "required bytes less than minimum",
			capRange: &csi.CapacityRange{
				RequiredBytes: MinVolumeSizeBytes - 1,
			},
			expectCap: MinVolumeSizeBytes - 1,
		},
		{
			name: "required bytes greater than minimum",
			capRange: &csi.CapacityRange{
				RequiredBytes: MinVolumeSizeBytes + 1,
			},
			expectCap: MinVolumeSizeBytes + 1,
		},
		{
			name: "required bytes is not set",
			capRange: &csi.CapacityRange{
				LimitBytes: MinVolumeSizeBytes + 1,
			},
			expectCap: MinVolumeSizeBytes,
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			capacity, err := getRequestCapacity(test.capRange)
			if test.expectErr == nil && err != nil {
				t.Errorf("test %q failed:\ngot error %q,\nexpected error nil", test.name, err)
			}
			if test.expectErr != nil && err == nil {
				t.Errorf("test %q failed:\ngot error nil,\nexpected error %q", test.name, test.expectErr)
			}
			if capacity != test.expectCap {
				t.Errorf("test %q failed:\ngot capacity %d,\nexpected capacity %d", test.name, capacity, test.expectCap)
			}
		})
	}
}
