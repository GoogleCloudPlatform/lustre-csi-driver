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
	"testing"

	csi "github.com/container-storage-interface/spec/lib/go/csi"
)

const (
	driverName    = "test-driver"
	driverVersion = "test-version"
	nodeID        = "test-node"
)

func initTestDriver(t *testing.T) *LustreDriver {
	t.Helper()

	config := &LustreDriverConfig{
		Name:          driverName,
		NodeID:        nodeID,
		Version:       driverVersion,
		RunController: true,
		RunNode:       true,
	}
	driver, err := NewLustreDriver(config)
	if err != nil {
		t.Fatalf("Failed to init driver: %v", err)
	}
	if driver == nil {
		t.Fatalf("Driver is nil")
	}

	return driver
}

func TestDriverValidateVolumeCapability(t *testing.T) {
	t.Parallel()
	driver := initTestDriver(t)

	cases := []struct {
		name       string
		capability *csi.VolumeCapability
		expectErr  error
	}{
		{
			name:       "nil caps",
			capability: nil,
			expectErr:  errors.New("volume capability must be provided"),
		},
		{
			name: "missing access type",
			capability: &csi.VolumeCapability{
				AccessMode: &csi.VolumeCapability_AccessMode{
					Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
				},
			},
			expectErr: errors.New("volume capability access type not set"),
		},
		{
			name: "missing access mode",
			capability: &csi.VolumeCapability{
				AccessType: &csi.VolumeCapability_Mount{
					Mount: &csi.VolumeCapability_MountVolume{},
				},
			},
			expectErr: errors.New("volume capability access mode not set"),
		},
		{
			name: "mount, SINGLE_NODE_WRITER",
			capability: &csi.VolumeCapability{
				AccessType: &csi.VolumeCapability_Mount{
					Mount: &csi.VolumeCapability_MountVolume{},
				},
				AccessMode: &csi.VolumeCapability_AccessMode{
					Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
				},
			},
		},
		{
			name: "mount, SINGLE_NODE_READER_ONLY",
			capability: &csi.VolumeCapability{
				AccessType: &csi.VolumeCapability_Mount{
					Mount: &csi.VolumeCapability_MountVolume{},
				},
				AccessMode: &csi.VolumeCapability_AccessMode{
					Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_READER_ONLY,
				},
			},
		},
		{
			name: "mount, MULTI_NODE_READER_ONLY",
			capability: &csi.VolumeCapability{
				AccessType: &csi.VolumeCapability_Mount{
					Mount: &csi.VolumeCapability_MountVolume{},
				},
				AccessMode: &csi.VolumeCapability_AccessMode{
					Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_READER_ONLY,
				},
			},
		},
		{
			name: "mount, MULTI_NODE_SINGLE_WRITER",
			capability: &csi.VolumeCapability{
				AccessType: &csi.VolumeCapability_Mount{
					Mount: &csi.VolumeCapability_MountVolume{},
				},
				AccessMode: &csi.VolumeCapability_AccessMode{
					Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_SINGLE_WRITER,
				},
			},
		},
		{
			name: "mount, MULTI_NODE_MULTI_WRITER",
			capability: &csi.VolumeCapability{
				AccessType: &csi.VolumeCapability_Mount{
					Mount: &csi.VolumeCapability_MountVolume{},
				},
				AccessMode: &csi.VolumeCapability_AccessMode{
					Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER,
				},
			},
		},
		{
			name: "mount, unknown accessmode",
			capability: &csi.VolumeCapability{
				AccessType: &csi.VolumeCapability_Mount{
					Mount: &csi.VolumeCapability_MountVolume{},
				},
				AccessMode: &csi.VolumeCapability_AccessMode{
					Mode: csi.VolumeCapability_AccessMode_UNKNOWN,
				},
			},
			expectErr: errors.New("driver does not support access mode: UNKNOWN"),
		},
		{
			name: "block, MULTI_NODE_MULTI_WRITER",
			capability: &csi.VolumeCapability{
				AccessType: &csi.VolumeCapability_Block{
					Block: &csi.VolumeCapability_BlockVolume{},
				},
				AccessMode: &csi.VolumeCapability_AccessMode{
					Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER,
				},
			},
			expectErr: errors.New("driver only supports mount access type volume capability"),
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := driver.validateVolumeCapability(test.capability)
			if err == nil && test.expectErr != nil {
				t.Errorf("test %q failed:\nexpected error %q,\ngot error nil", test.name, test.expectErr)
			}
			if err != nil && (test.expectErr == nil || err.Error() != test.expectErr.Error()) {
				t.Errorf("test %q failed:\nexpected error %q,\ngot error %q", test.name, test.expectErr, err)
			}
		})
	}
}

func TestDriverValidateVolumeCapabilities(t *testing.T) {
	t.Parallel()
	driver := initTestDriver(t)

	cases := []struct {
		name         string
		capabilities []*csi.VolumeCapability
		expectErr    error
	}{
		{
			name:         "nil caps",
			capabilities: nil,
			expectErr:    errors.New("volume capabilities must be provided"),
		},
		{
			name: "multiple good capabilities",
			capabilities: []*csi.VolumeCapability{
				{
					AccessType: &csi.VolumeCapability_Mount{
						Mount: &csi.VolumeCapability_MountVolume{},
					},
					AccessMode: &csi.VolumeCapability_AccessMode{
						Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER,
					},
				},
				{
					AccessType: &csi.VolumeCapability_Mount{
						Mount: &csi.VolumeCapability_MountVolume{},
					},
					AccessMode: &csi.VolumeCapability_AccessMode{
						Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_READER_ONLY,
					},
				},
			},
		},
		{
			name:      "multiple bad capabilities",
			expectErr: errors.New("driver does not support access mode: UNKNOWN"),
			capabilities: []*csi.VolumeCapability{
				{
					AccessType: &csi.VolumeCapability_Mount{
						Mount: &csi.VolumeCapability_MountVolume{},
					},
					AccessMode: &csi.VolumeCapability_AccessMode{
						Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER,
					},
				},
				{
					AccessType: &csi.VolumeCapability_Mount{
						Mount: &csi.VolumeCapability_MountVolume{},
					},
					AccessMode: &csi.VolumeCapability_AccessMode{
						Mode: csi.VolumeCapability_AccessMode_UNKNOWN,
					},
				},
			},
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := driver.validateVolumeCapabilities(test.capabilities)
			if err == nil && test.expectErr != nil {
				t.Errorf("test %q failed:\nexpected error %q,\ngot error nil", test.name, test.expectErr)
			}
			if err != nil && (test.expectErr == nil || err.Error() != test.expectErr.Error()) {
				t.Errorf("test %q failed:\nexpected error %q,\ngot error %q", test.name, test.expectErr, err)
			}
		})
	}
}
