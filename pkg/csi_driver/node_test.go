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
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/GoogleCloudPlatform/lustre-csi-driver/pkg/cloud_provider/metadata"
	csi "github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	utiltesting "k8s.io/client-go/util/testing"
	mount "k8s.io/mount-utils"
)

const (
	testVolumeID   = "test-project/us-central1-a/test-instance"
	testIP         = "127.0.0.1"
	testFilesystem = "gcloud"
	testDevice     = "127.0.0.1@tcp:/gcloud"
)

var (
	testVolumeCapability = &csi.VolumeCapability{
		AccessType: &csi.VolumeCapability_Mount{
			Mount: &csi.VolumeCapability_MountVolume{},
		},
		AccessMode: &csi.VolumeCapability_AccessMode{
			Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
		},
	}

	testVolumeAttributes = map[string]string{
		keyInstanceIP: testIP,
		keyFilesystem: testFilesystem,
	}

	testVolumeAttributesIAM = map[string]string{
		keyInstanceIP:              testIP,
		keyFilesystem:              testFilesystem,
		keyPodUID:                  "test-pod-uid",
		keyServiceAccount:          "test-sa",
		keyPodNamespace:            "test-ns",
		keyIAMAccessControlEnabled: "true",
		keyServiceToken:            `{"test-project.svc.id.goog":{"token":"test-token","expirationTimestamp":"2099-01-01T00:00:00Z"}}`,
	}
)

type nodeServerTestEnv struct {
	ns csi.NodeServer
	fm *mount.FakeMounter
}

type mockMetadataService struct {
	project string
	zone    string
	nics    []metadata.NetworkInterface
}

func (m *mockMetadataService) GetZone() string {
	return m.zone
}

func (m *mockMetadataService) GetProject() string {
	return m.project
}

func (m *mockMetadataService) GetNetworkInterfaces() ([]metadata.NetworkInterface, error) {
	return m.nics, nil
}

type fakeMounter struct {
	*mount.FakeMounter
	// 'operationUnblocker' channel is used to block the execution of the respective function using it.
	// This is done by sending a channel of empty struct over 'operationUnblocker' channel,
	// and wait until the tester gives a go-ahead to proceed further in the execution of the function.
	operationUnblocker chan chan struct{}
	mountErr           error
	failTarget         string
}

type nodeRequestConfig struct {
	nodePublishReq   *csi.NodePublishVolumeRequest
	nodeUnpublishReq *csi.NodeUnpublishVolumeRequest
	nodeStageReq     *csi.NodeStageVolumeRequest
	nodeUnstageReq   *csi.NodeUnstageVolumeRequest
}

func initTestNodeServer(t *testing.T) *nodeServerTestEnv {
	t.Helper()
	mounter := &mount.FakeMounter{MountPoints: []mount.MountPoint{}}
	driver := initTestDriver(t)
	driver.config.MetadataService = &mockMetadataService{project: "test-project"}

	return &nodeServerTestEnv{
		ns: newNodeServer(driver, mounter),
		fm: mounter,
	}
}

func initBlockingTestNodeServer(t *testing.T, operationUnblocker chan chan struct{}) *nodeServerTestEnv {
	t.Helper()
	mounter := newFakeBlockingMounter(operationUnblocker)
	driver := initTestDriver(t)
	driver.config.MetadataService = &mockMetadataService{project: "test-project"}

	return &nodeServerTestEnv{
		ns: newNodeServer(driver, mounter),
		fm: nil,
	}
}

func newFakeBlockingMounter(operationUnblocker chan chan struct{}) *fakeMounter {
	return &fakeMounter{
		FakeMounter:        &mount.FakeMounter{MountPoints: []mount.MountPoint{}},
		operationUnblocker: operationUnblocker,
	}
}

func (m *fakeMounter) MountSensitiveWithoutSystemd(source string, target string, fstype string, options []string, sensitiveOptions []string) error {
	if m.operationUnblocker != nil {
		execute := make(chan struct{})
		m.operationUnblocker <- execute
		<-execute
	}

	if m.mountErr != nil && (m.failTarget == "" || target == m.failTarget) {
		return m.mountErr
	}

	return m.FakeMounter.MountSensitiveWithoutSystemd(source, target, fstype, options, sensitiveOptions)
}

func TestNodeStageVolme(t *testing.T) {
	t.Parallel()
	// Setup mount staging path
	base := t.TempDir()
	stagingTargetPath := filepath.Join(base, "staging")

	cases := []struct {
		name          string
		mounts        []mount.MountPoint // already existing mounts
		req           *csi.NodeStageVolumeRequest
		actions       []mount.FakeAction
		expectedMount *mount.MountPoint
		expectErr     bool
	}{
		{
			name:      "empty request",
			req:       &csi.NodeStageVolumeRequest{},
			expectErr: true,
		},
		{
			name: "IAM enabled request skipped",
			req: &csi.NodeStageVolumeRequest{
				VolumeId:          testVolumeID,
				StagingTargetPath: stagingTargetPath,
				VolumeCapability:  testVolumeCapability,
				VolumeContext: map[string]string{
					keyInstanceIP:              "127.0.0.2",
					keyFilesystem:              testFilesystem,
					keyIAMAccessControlEnabled: "true",
				},
			},
			expectErr:     false,
			expectedMount: nil,
		},
		{
			name:   "same fsname already mounted",
			mounts: []mount.MountPoint{{Device: testDevice, Path: stagingTargetPath}},
			req: &csi.NodeStageVolumeRequest{
				VolumeId:          testVolumeID,
				StagingTargetPath: stagingTargetPath,
				VolumeCapability:  testVolumeCapability,
				VolumeContext: map[string]string{
					keyInstanceIP: "127.0.0.2",
					keyFilesystem: testFilesystem,
				},
			},
			expectedMount: &mount.MountPoint{Device: testDevice, Path: stagingTargetPath},
			expectErr:     true,
		},
		{
			name: "valid request not mounted yet",
			req: &csi.NodeStageVolumeRequest{
				VolumeId:          testVolumeID,
				StagingTargetPath: stagingTargetPath,
				VolumeCapability:  testVolumeCapability,
				VolumeContext:     testVolumeAttributes,
			},
			actions:       []mount.FakeAction{{Action: mount.FakeActionMount}},
			expectedMount: &mount.MountPoint{Device: testDevice, Path: stagingTargetPath, Type: "lustre", Opts: []string{}},
		},
		{
			name:   "valid request already mounted",
			mounts: []mount.MountPoint{{Device: testDevice, Path: stagingTargetPath}},
			req: &csi.NodeStageVolumeRequest{
				VolumeId:          testVolumeID,
				StagingTargetPath: stagingTargetPath,
				VolumeCapability:  testVolumeCapability,
				VolumeContext:     testVolumeAttributes,
			},
			expectedMount: &mount.MountPoint{Device: testDevice, Path: stagingTargetPath},
		},
		{
			name: "valid request with user mount options",
			req: &csi.NodeStageVolumeRequest{
				VolumeId:          testVolumeID,
				StagingTargetPath: stagingTargetPath,
				VolumeCapability: &csi.VolumeCapability{
					AccessType: &csi.VolumeCapability_Mount{
						Mount: &csi.VolumeCapability_MountVolume{
							MountFlags: []string{"foo", "bar"},
						},
					},
					AccessMode: &csi.VolumeCapability_AccessMode{
						Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
					},
				},
				VolumeContext: testVolumeAttributes,
			},
			actions: []mount.FakeAction{{Action: mount.FakeActionMount}},

			expectedMount: &mount.MountPoint{Device: testDevice, Path: stagingTargetPath, Type: "lustre", Opts: []string{"foo", "bar"}},
		},
		{
			name: "empty staging target path",
			req: &csi.NodeStageVolumeRequest{
				VolumeId:         testVolumeID,
				VolumeCapability: testVolumeCapability,
				VolumeContext:    testVolumeAttributes,
			},
			expectErr: true,
		},
		{
			name: "invalid volume capability",
			req: &csi.NodeStageVolumeRequest{
				VolumeId:      testVolumeID,
				VolumeContext: testVolumeAttributes,
			},
			expectErr: true,
		},
		{
			name: "invalid volume attribute",
			req: &csi.NodeStageVolumeRequest{
				VolumeId:         testVolumeID,
				VolumeCapability: testVolumeCapability,
			},
			expectErr: true,
		},
		{
			name: "valid request with mountpoint",
			req: &csi.NodeStageVolumeRequest{
				VolumeId:          testVolumeID,
				StagingTargetPath: stagingTargetPath,
				VolumeCapability:  testVolumeCapability,
				VolumeContext: map[string]string{
					keyMountPoint: testDevice,
				},
			},
			actions:       []mount.FakeAction{{Action: mount.FakeActionMount}},
			expectedMount: &mount.MountPoint{Device: testDevice, Path: stagingTargetPath, Type: "lustre", Opts: []string{}},
		},
		{
			name: "invalid mountpoint format",
			req: &csi.NodeStageVolumeRequest{
				VolumeId:          testVolumeID,
				StagingTargetPath: stagingTargetPath,
				VolumeCapability:  testVolumeCapability,
				VolumeContext: map[string]string{
					keyMountPoint: "invalid-format",
				},
			},
			expectErr: true,
		},
	}
	for _, test := range cases {
		testEnv := initTestNodeServer(t)
		if test.mounts != nil {
			testEnv.fm.MountPoints = test.mounts
		}

		_, err := testEnv.ns.NodeStageVolume(t.Context(), test.req)
		if !test.expectErr && err != nil {
			t.Errorf("Test %q failed: %v", test.name, err)
		}
		if test.expectErr && err == nil {
			t.Errorf("Test %q failed: got success", test.name)
		}

		validateMountPoint(t, test.name, testEnv.fm, test.expectedMount)
	}
}

func TestNodeUnstageVolume(t *testing.T) {
	t.Parallel()
	defaultPerm := os.FileMode(0o750) + os.ModeDir

	// Setup mount stage path
	base := t.TempDir()
	testStagingPath := filepath.Join(base, "staging")
	if err := os.MkdirAll(testStagingPath, defaultPerm); err != nil {
		t.Fatalf("Failed to setup stage path: %v", err)
	}

	cases := []struct {
		name          string
		mounts        []mount.MountPoint // already existing mounts
		req           *csi.NodeUnstageVolumeRequest
		actions       []mount.FakeAction
		expectedMount *mount.MountPoint
		expectErr     bool
	}{
		{
			name:   "successful unmount",
			mounts: []mount.MountPoint{{Device: testDevice, Path: testStagingPath}},
			req: &csi.NodeUnstageVolumeRequest{
				VolumeId:          testVolumeID,
				StagingTargetPath: testStagingPath,
			},
			actions: []mount.FakeAction{{Action: mount.FakeActionUnmount}},
		},
		{
			name: "empty target path",
			req: &csi.NodeUnstageVolumeRequest{
				VolumeId: testVolumeID,
			},
			expectErr: true,
		},
		{
			name: "dir doesn't exist",
			req: &csi.NodeUnstageVolumeRequest{
				VolumeId:          testVolumeID,
				StagingTargetPath: "/node-unstage-dir-not-exists",
			},
		},
		{
			name: "dir not mounted",
			req: &csi.NodeUnstageVolumeRequest{
				VolumeId:          testVolumeID,
				StagingTargetPath: testStagingPath,
			},
		},
	}

	for _, test := range cases {
		testEnv := initTestNodeServer(t)
		if test.mounts != nil {
			testEnv.fm.MountPoints = test.mounts
		}

		_, err := testEnv.ns.NodeUnstageVolume(t.Context(), test.req)
		if !test.expectErr && err != nil {
			t.Errorf("Test %q failed: %v", test.name, err)
		}
		if test.expectErr && err == nil {
			t.Errorf("Test %q failed: got success", test.name)
		}

		validateMountPoint(t, test.name, testEnv.fm, test.expectedMount)
	}
}

func TestNodePublishVolume(t *testing.T) {
	// Disable t.Parallel() and redirect GlobalMountRoot to t.TempDir() to avoid
	// host permission errors.
	oldRoot := GlobalMountRoot
	defer func() { GlobalMountRoot = oldRoot }()
	GlobalMountRoot = t.TempDir()

	defaultPerm := os.FileMode(0o750) + os.ModeDir

	// Setup mount target path
	base := t.TempDir()
	testTargetPath := filepath.Join(base, "target")
	if err := os.MkdirAll(testTargetPath, defaultPerm); err != nil {
		t.Fatalf("Failed to setup target path: %v", err)
	}
	stagingTargetPath := filepath.Join(base, "staging")
	key := computeHash(testVolumeID + "test-ns/test-sa")
	globalMountPath := filepath.Join(GlobalMountRoot, key, "mount")
	cases := []struct {
		name           string
		mounts         []mount.MountPoint // already existing mounts
		req            *csi.NodePublishVolumeRequest
		actions        []mount.FakeAction
		expectedMounts []*mount.MountPoint
		expectErr      bool
	}{
		{
			name:      "empty request",
			req:       &csi.NodePublishVolumeRequest{},
			expectErr: true,
		},
		{
			name: "valid request not mounted yet",
			req: &csi.NodePublishVolumeRequest{
				VolumeId:          testVolumeID,
				StagingTargetPath: stagingTargetPath,
				TargetPath:        testTargetPath,
				VolumeCapability:  testVolumeCapability,
				VolumeContext:     testVolumeAttributes,
			},
			actions:        []mount.FakeAction{{Action: mount.FakeActionMount}},
			expectedMounts: []*mount.MountPoint{{Device: stagingTargetPath, Path: testTargetPath, Type: "lustre", Opts: []string{"bind"}}},
		},
		{
			name:   "valid request already mounted",
			mounts: []mount.MountPoint{{Device: "/test-device", Path: testTargetPath}},
			req: &csi.NodePublishVolumeRequest{
				VolumeId:          testVolumeID,
				StagingTargetPath: stagingTargetPath,
				TargetPath:        testTargetPath,
				VolumeCapability:  testVolumeCapability,
				VolumeContext:     testVolumeAttributes,
			},
			expectedMounts: []*mount.MountPoint{{Device: "/test-device", Path: testTargetPath}},
		},
		{
			name: "valid request with user mount options",
			req: &csi.NodePublishVolumeRequest{
				VolumeId:          testVolumeID,
				StagingTargetPath: stagingTargetPath,
				TargetPath:        testTargetPath,
				VolumeCapability: &csi.VolumeCapability{
					AccessType: &csi.VolumeCapability_Mount{
						Mount: &csi.VolumeCapability_MountVolume{
							MountFlags: []string{"foo", "bar"},
						},
					},
					AccessMode: &csi.VolumeCapability_AccessMode{
						Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
					},
				},
				VolumeContext: testVolumeAttributes,
			},
			actions:        []mount.FakeAction{{Action: mount.FakeActionMount}},
			expectedMounts: []*mount.MountPoint{{Device: stagingTargetPath, Path: testTargetPath, Type: "lustre", Opts: []string{"bind", "foo", "bar"}}},
		},
		{
			name: "valid request read only",
			req: &csi.NodePublishVolumeRequest{
				VolumeId:          testVolumeID,
				StagingTargetPath: stagingTargetPath,
				TargetPath:        testTargetPath,
				VolumeCapability:  testVolumeCapability,
				VolumeContext:     testVolumeAttributes,
				Readonly:          true,
			},
			actions:        []mount.FakeAction{{Action: mount.FakeActionMount}},
			expectedMounts: []*mount.MountPoint{{Device: stagingTargetPath, Path: testTargetPath, Type: "lustre", Opts: []string{"bind", "ro"}}},
		},
		{
			name: "valid request IAM (Global Mount)",
			req: &csi.NodePublishVolumeRequest{
				VolumeId:          testVolumeID,
				StagingTargetPath: stagingTargetPath,
				TargetPath:        testTargetPath,
				VolumeCapability:  testVolumeCapability,
				VolumeContext:     testVolumeAttributesIAM,
			},
			actions: []mount.FakeAction{{Action: mount.FakeActionMount}, {Action: mount.FakeActionMount}}, // Global + Bind
			expectedMounts: []*mount.MountPoint{
				{Device: testDevice, Path: globalMountPath, Type: "lustre", Opts: []string{"user=gke-wi://test-ns/test-sa+" + key}},
				{Device: testDevice, Path: testTargetPath, Type: "lustre", Opts: []string{"bind"}},
			},
		},
		{
			name: "valid request IAM with user mount options (Global Mount)",
			req: &csi.NodePublishVolumeRequest{
				VolumeId:          testVolumeID,
				StagingTargetPath: stagingTargetPath,
				TargetPath:        testTargetPath,
				VolumeCapability: &csi.VolumeCapability{
					AccessType: &csi.VolumeCapability_Mount{
						Mount: &csi.VolumeCapability_MountVolume{
							MountFlags: []string{"flock", "noatime"},
						},
					},
					AccessMode: &csi.VolumeCapability_AccessMode{
						Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
					},
				},
				VolumeContext: testVolumeAttributesIAM,
			},
			actions: []mount.FakeAction{{Action: mount.FakeActionMount}, {Action: mount.FakeActionMount}}, // Global + Bind
			expectedMounts: []*mount.MountPoint{
				{Device: testDevice, Path: globalMountPath, Type: "lustre", Opts: []string{"user=gke-wi://test-ns/test-sa+" + key, "flock", "noatime"}},
				{Device: testDevice, Path: testTargetPath, Type: "lustre", Opts: []string{"bind", "flock", "noatime"}},
			},
		},
		{
			name: "valid request IAM (Token Refresh)",
			mounts: []mount.MountPoint{
				{Device: testDevice, Path: globalMountPath, Type: "lustre", Opts: []string{"user=gke-wi://test-ns/test-sa+" + key}},
				{Device: testDevice, Path: testTargetPath, Type: "lustre", Opts: []string{"bind"}},
			},
			req: &csi.NodePublishVolumeRequest{
				VolumeId:          testVolumeID,
				StagingTargetPath: stagingTargetPath,
				TargetPath:        testTargetPath,
				VolumeCapability:  testVolumeCapability,
				VolumeContext:     testVolumeAttributesIAM,
			},
			actions: []mount.FakeAction{},
			expectedMounts: []*mount.MountPoint{
				{Device: testDevice, Path: globalMountPath, Type: "lustre", Opts: []string{"user=gke-wi://test-ns/test-sa+" + key}},
				{Device: testDevice, Path: testTargetPath, Type: "lustre", Opts: []string{"bind"}},
			},
		},
		{
			name: "empty target path",
			req: &csi.NodePublishVolumeRequest{
				VolumeId:          testVolumeID,
				StagingTargetPath: stagingTargetPath,
				VolumeCapability:  testVolumeCapability,
				VolumeContext:     testVolumeAttributes,
			},
			expectErr: true,
		},
		{
			name: "empty staging target path",
			req: &csi.NodePublishVolumeRequest{
				VolumeId:         testVolumeID,
				TargetPath:       testTargetPath,
				VolumeCapability: testVolumeCapability,
				VolumeContext:    testVolumeAttributes,
			},
			expectErr: true,
		},
		{
			name: "invalid volume capability",
			req: &csi.NodePublishVolumeRequest{
				VolumeId:      testVolumeID,
				TargetPath:    testTargetPath,
				VolumeContext: testVolumeAttributes,
			},
			expectErr: true,
		},
		{
			name: "invalid volume attribute",
			req: &csi.NodePublishVolumeRequest{
				VolumeId:         testVolumeID,
				TargetPath:       testTargetPath,
				VolumeCapability: testVolumeCapability,
			},
			expectErr: true,
		},
	}
	for _, test := range cases {
		testEnv := initTestNodeServer(t)
		if test.mounts != nil {
			testEnv.fm.MountPoints = test.mounts
		}

		_, err := testEnv.ns.NodePublishVolume(t.Context(), test.req)
		if !test.expectErr && err != nil {
			t.Errorf("Test %q failed: %v", test.name, err)
		}
		if test.expectErr && err == nil {
			t.Errorf("Test %q failed: got success", test.name)
		}

		validateMountPoint(t, test.name, testEnv.fm, test.expectedMounts...)
	}
}

func TestNodePublishVolume_SymlinkRace(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	targetPath := filepath.Join(base, "target")
	sensitivePath := filepath.Join(base, "sensitive")

	// Create a sensitive directory that an attacker targets
	if err := os.Mkdir(sensitivePath, 0o755); err != nil {
		t.Fatalf("failed to create sensitive path: %v", err)
	}

	// Create a symlink at the target path pointing to the sensitive directory
	// to simulates the attack.
	if err := os.Symlink(sensitivePath, targetPath); err != nil {
		t.Fatalf("failed to create symlink: %v", err)
	}

	testEnv := initTestNodeServer(t)
	req := &csi.NodePublishVolumeRequest{
		VolumeId:          testVolumeID,
		StagingTargetPath: filepath.Join(base, "staging"),
		TargetPath:        targetPath,
		VolumeCapability:  testVolumeCapability,
		VolumeContext:     testVolumeAttributes,
	}

	_, err := testEnv.ns.NodePublishVolume(t.Context(), req)

	// The call should fail because a symlink exists
	if err == nil {
		t.Error("expected error when target path is a symlink, but got success")
	} else if !strings.Contains(err.Error(), "exists but is not a directory") {
		t.Errorf("expected error containing 'exists but is not a directory', got: %v", err)
	}
}

func TestNodeUnpublishVolume(t *testing.T) {
	t.Parallel()
	defaultPerm := os.FileMode(0o750) + os.ModeDir

	// Setup mount target path
	base := t.TempDir()
	testTargetPath := filepath.Join(base, "target")
	if err := os.MkdirAll(testTargetPath, defaultPerm); err != nil {
		t.Fatalf("Failed to setup target path: %v", err)
	}

	cases := []struct {
		name          string
		mounts        []mount.MountPoint // already existing mounts
		req           *csi.NodeUnpublishVolumeRequest
		actions       []mount.FakeAction
		expectedMount *mount.MountPoint
		expectErr     bool
	}{
		{
			name:   "successful unmount",
			mounts: []mount.MountPoint{{Device: testDevice, Path: testTargetPath}},
			req: &csi.NodeUnpublishVolumeRequest{
				VolumeId:   testVolumeID,
				TargetPath: testTargetPath,
			},
			actions: []mount.FakeAction{{Action: mount.FakeActionUnmount}},
		},
		{
			name: "empty target path",
			req: &csi.NodeUnpublishVolumeRequest{
				VolumeId: testVolumeID,
			},
			expectErr: true,
		},
		{
			name: "dir doesn't exist",
			req: &csi.NodeUnpublishVolumeRequest{
				VolumeId:   testVolumeID,
				TargetPath: "/node-unpublish-dir-not-exists",
			},
		},
		{
			name: "dir not mounted",
			req: &csi.NodeUnpublishVolumeRequest{
				VolumeId:   testVolumeID,
				TargetPath: testTargetPath,
			},
		},
	}

	for _, test := range cases {
		testEnv := initTestNodeServer(t)
		if test.mounts != nil {
			testEnv.fm.MountPoints = test.mounts
		}

		_, err := testEnv.ns.NodeUnpublishVolume(t.Context(), test.req)
		if !test.expectErr && err != nil {
			t.Errorf("Test %q failed: %v", test.name, err)
		}
		if test.expectErr && err == nil {
			t.Errorf("Test %q failed: got success", test.name)
		}

		validateMountPoint(t, test.name, testEnv.fm, test.expectedMount)
	}
}

func TestNodeUnpublishVolume_IAM(t *testing.T) {
	// Reassign GlobalMountRoot to t.TempDir() for isolation.
	origGlobalMountRoot := GlobalMountRoot
	GlobalMountRoot = t.TempDir()
	defer func() { GlobalMountRoot = origGlobalMountRoot }()

	defaultPerm := os.FileMode(0o750) + os.ModeDir
	testKey := "71ac09669e8a14e7dca05e1bb9ef37802de0448c37bb195c6c1b68a5ff3f2c2e"

	basePath := t.TempDir()
	stagingTargetPath := filepath.Join(basePath, "staging")
	_ = stagingTargetPath

	podUID1 := "a1b2c3d4-e5f6-7a8b-9c0d-1e2f3a4b5c6d"
	podUID2 := "b2c3d4e5-f6a7-8b9c-0d1e-2f3a4b5c6d7e"

	targetPath1 := filepath.Join(basePath, "pods", podUID1, "volumes", "kubernetes.io~csi", "vol-a", "mount")
	targetPath2 := filepath.Join(basePath, "pods", podUID2, "volumes", "kubernetes.io~csi", "vol-b", "mount")

	cases := []struct {
		name                string
		targetPath          string
		targetPodUID        string
		targetVolumeID      string
		setupRefs           []string
		setupGlobalMounted  bool
		expectGlobalUnmount bool
		expectKeyDirDeleted bool
		expectRemainingRefs []string
		expectErr           bool
	}{
		{
			name:                "successful unpublish with other active pod references remaining",
			targetPath:          targetPath1,
			targetPodUID:        podUID1,
			targetVolumeID:      "vol-a",
			setupRefs:           []string{podUID1, podUID2},
			setupGlobalMounted:  true,
			expectGlobalUnmount: false,
			expectKeyDirDeleted: false,
			expectRemainingRefs: []string{podUID2},
		},
		{
			name:                "successful unpublish of last remaining pod (triggers global teardown)",
			targetPath:          targetPath2,
			targetPodUID:        podUID2,
			targetVolumeID:      "vol-b",
			setupRefs:           []string{podUID2},
			setupGlobalMounted:  true,
			expectGlobalUnmount: true,
			expectKeyDirDeleted: true,
			expectRemainingRefs: []string{},
		},
		{
			name:                "unpublish non-IAM volume is skipped or handled gracefully",
			targetPath:          filepath.Join(basePath, "legacy-mount"),
			targetPodUID:        "",
			targetVolumeID:      "vol-legacy",
			setupRefs:           []string{},
			expectGlobalUnmount: false,
			expectKeyDirDeleted: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			keyDir := filepath.Join(GlobalMountRoot, testKey)
			// Make sure to teardown the directory to handle cases where a subtest fails mid run
			t.Cleanup(func() { _ = os.RemoveAll(keyDir) })
			if err := os.RemoveAll(keyDir); err != nil {
				t.Fatalf("failed to clean up key dir: %v", err)
			}

			// Build existingMounts dynamically using the temporary paths
			var existingMounts []mount.MountPoint
			if tc.setupGlobalMounted {
				existingMounts = append(existingMounts, mount.MountPoint{
					Device: "10.0.0.1@tcp:/iamfs",
					Path:   filepath.Join(keyDir, "mount"),
					Type:   "lustre",
				})
			}
			existingMounts = append(existingMounts, mount.MountPoint{
				Device: filepath.Join(keyDir, "mount"),
				Path:   tc.targetPath,
				Type:   "lustre",
			})

			if len(tc.setupRefs) > 0 {
				refsDir := filepath.Join(keyDir, "refs")
				if err := os.MkdirAll(refsDir, defaultPerm); err != nil {
					t.Fatalf("Failed to create refs dir: %v", err)
				}
				for _, uid := range tc.setupRefs {
					refPath := filepath.Join(refsDir, uid)
					if err := os.WriteFile(refPath, []byte(tc.targetVolumeID), 0644); err != nil {
						t.Fatalf("Failed to write ref file %q: %v", refPath, err)
					}
				}
				globalMntPath := filepath.Join(keyDir, "mount")
				if err := os.MkdirAll(globalMntPath, defaultPerm); err != nil {
					t.Fatalf("Failed to create global mount dir: %v", err)
				}
			}

			testEnv := initTestNodeServer(t)
			testEnv.fm.MountPoints = existingMounts

			// Ensure the target path folder itself exists on disk so isMounted and cleanup checks pass
			if err := os.MkdirAll(tc.targetPath, defaultPerm); err != nil {
				t.Fatalf("Failed to create target path directory: %v", err)
			}

			req := &csi.NodeUnpublishVolumeRequest{
				VolumeId:   tc.targetVolumeID,
				TargetPath: tc.targetPath,
			}

			_, err := testEnv.ns.NodeUnpublishVolume(t.Context(), req)
			if (err != nil) != tc.expectErr {
				t.Fatalf("NodeUnpublishVolume returned unexpected error: %v", err)
			}

			// 1. Verify target path is unmounted
			for _, m := range testEnv.fm.MountPoints {
				if m.Path == tc.targetPath {
					t.Errorf("Target path %q was not unmounted", tc.targetPath)
				}
			}

			// 2. Verify remaining reference files
			if !tc.expectKeyDirDeleted {
				for _, expectedRef := range tc.expectRemainingRefs {
					refPath := filepath.Join(keyDir, "refs", expectedRef)
					exists, err := pathExists(refPath)
					if err != nil || !exists {
						t.Errorf("Expected reference file %q to remain, but it was missing", refPath)
					}
				}

				// Check that deleted references are actually gone
				if tc.targetPodUID != "" {
					deletedRefPath := filepath.Join(keyDir, "refs", tc.targetPodUID)
					exists, err := pathExists(deletedRefPath)
					if err == nil && exists {
						t.Errorf("Expected reference file %q to be deleted, but it still exists", deletedRefPath)
					}
				}
			}

			// 3. Verify global mount unmount action
			globalMntPath := filepath.Join(keyDir, "mount")
			isGlobalMntActive := false
			for _, m := range testEnv.fm.MountPoints {
				if m.Path == globalMntPath {
					isGlobalMntActive = true
					break
				}
			}
			if tc.expectGlobalUnmount && isGlobalMntActive {
				t.Errorf("Expected global mount path %q to be unmounted, but it was still active", globalMntPath)
			}
			if !tc.expectGlobalUnmount && tc.setupGlobalMounted && !isGlobalMntActive {
				t.Errorf("Expected global mount path %q to remain mounted, but it was unmounted", globalMntPath)
			}

			// 4. Verify state directories cleanup
			keyDirExists, err := pathExists(keyDir)
			if err != nil {
				t.Fatalf("Failed to check if key dir exists: %v", err)
			}
			if tc.expectKeyDirDeleted && keyDirExists {
				t.Errorf("Expected global directory %q to be deleted, but it still exists", keyDir)
			}
			if !tc.expectKeyDirDeleted && len(tc.setupRefs) > 0 && !keyDirExists {
				t.Errorf("Expected global directory %q to remain, but it was deleted", keyDir)
			}
		})
	}
}

func validateMountPoint(t *testing.T, name string, fm *mount.FakeMounter, expectedMounts ...*mount.MountPoint) {
	t.Helper()
	var validExpected []*mount.MountPoint
	for _, e := range expectedMounts {
		if e != nil {
			validExpected = append(validExpected, e)
		}
	}

	if len(validExpected) == 0 {
		if len(fm.MountPoints) != 0 {
			t.Errorf("Test %q failed: got mounts %+v, expected none", name, fm.MountPoints)
		}

		return
	}

	if mLen := len(fm.MountPoints); mLen != len(validExpected) {
		t.Errorf("Test %q failed: got %v mounts(%+v), expected %v", name, mLen, fm.MountPoints, len(validExpected))

		return
	}

	for idx, e := range validExpected {
		a := &fm.MountPoints[idx]
		if a.Device != e.Device {
			t.Errorf("Test %q [%d] failed: got device %q, expected %q", name, idx, a.Device, e.Device)
		}
		if a.Path != e.Path {
			t.Errorf("Test %q [%d] failed: got path %q, expected %q", name, idx, a.Path, e.Path)
		}
		if a.Type != e.Type {
			t.Errorf("Test %q [%d] failed: got type %q, expected %q", name, idx, a.Type, e.Type)
		}

		aLen := len(a.Opts)
		eLen := len(e.Opts)
		if aLen != eLen {
			t.Errorf("Test %q [%d] failed: got opts length %v, expected %v", name, idx, aLen, eLen)
		}

		for i := range a.Opts {
			aOpt := a.Opts[i]
			eOpt := e.Opts[i]
			if aOpt != eOpt {
				t.Errorf("Test %q [%d] failed: got opt %q, expected %q", name, idx, aOpt, eOpt)
			}
		}
	}
}

func TestConcurrentMounts(t *testing.T) {
	t.Parallel()
	// A channel of size 1 is sufficient, because the caller of runRequest() in below steps immediately blocks
	// and retrieves the channel of empty struct from 'operationUnblocker' channel.
	// The test steps are such that, at most one function pushes items on the 'operationUnblocker' channel,
	// to indicate that the function is blocked and waiting for a signal to proceed further in the execution.
	operationUnblocker := make(chan chan struct{}, 1)
	ns := initBlockingTestNodeServer(t, operationUnblocker)
	basePath := t.TempDir()
	stagingTargetPath := filepath.Join(basePath, "staging")
	targetPath1 := filepath.Join(basePath, "target1")
	targetPath2 := filepath.Join(basePath, "target2")

	runRequest := func(req *nodeRequestConfig) <-chan error {
		resp := make(chan error)
		go func() {
			var err error
			switch {
			case req.nodePublishReq != nil:
				_, err = ns.ns.NodePublishVolume(t.Context(), req.nodePublishReq)
			case req.nodeUnpublishReq != nil:
				_, err = ns.ns.NodeUnpublishVolume(t.Context(), req.nodeUnpublishReq)
			case req.nodeStageReq != nil:
				_, err = ns.ns.NodeStageVolume(t.Context(), req.nodeStageReq)
			case req.nodeUnstageReq != nil:
				_, err = ns.ns.NodeUnstageVolume(t.Context(), req.nodeUnstageReq)
			}
			resp <- err
			close(resp) // Ensure the channel is closed to prevent goroutine leaks
		}()

		return resp
	}

	// Node stage blocked after lock acquire.
	resp := runRequest(&nodeRequestConfig{
		nodeStageReq: &csi.NodeStageVolumeRequest{
			VolumeId:          testVolumeID,
			StagingTargetPath: stagingTargetPath,
			VolumeCapability:  testVolumeCapability,
			VolumeContext:     testVolumeAttributes,
		},
	})
	nodestageOpUnblocker := <-operationUnblocker

	// Same volume ID node stage should fail to acquire lock and return Aborted error.
	stageResp2 := runRequest(&nodeRequestConfig{
		nodeStageReq: &csi.NodeStageVolumeRequest{
			VolumeId:          testVolumeID,
			StagingTargetPath: stagingTargetPath,
			VolumeCapability:  testVolumeCapability,
			VolumeContext:     testVolumeAttributes,
		},
	})
	validateExpectedError(t, stageResp2, operationUnblocker, codes.Aborted)

	// Same volume ID node unstage should fail to acquire lock and return Aborted error.
	unstageResp := runRequest(&nodeRequestConfig{
		nodeUnstageReq: &csi.NodeUnstageVolumeRequest{
			VolumeId:          testVolumeID,
			StagingTargetPath: stagingTargetPath,
		},
	})
	validateExpectedError(t, unstageResp, operationUnblocker, codes.Aborted)

	// Unblock first node stage. Success expected.
	nodestageOpUnblocker <- struct{}{}
	if err := <-resp; err != nil {
		t.Errorf("Unexpected error: %v", err)
	}

	// Node publish blocked after lock acquire on the 'targetPath1'.
	targetPath1Publishresp := runRequest(&nodeRequestConfig{
		nodePublishReq: &csi.NodePublishVolumeRequest{
			VolumeId:          testVolumeID,
			StagingTargetPath: stagingTargetPath,
			TargetPath:        targetPath1,
			VolumeCapability:  testVolumeCapability,
			VolumeContext:     testVolumeAttributes,
		},
	})
	nodepublishOpTargetPath1Unblocker := <-operationUnblocker

	// Node publish for the same target path should fail to acquire lock and return Aborted error.
	targetPath1Publishresp2 := runRequest(&nodeRequestConfig{
		nodePublishReq: &csi.NodePublishVolumeRequest{
			VolumeId:          testVolumeID,
			StagingTargetPath: stagingTargetPath,
			TargetPath:        targetPath1,
			VolumeCapability:  testVolumeCapability,
			VolumeContext:     testVolumeAttributes,
		},
	})
	validateExpectedError(t, targetPath1Publishresp2, operationUnblocker, codes.Aborted)

	// Node unpublish for the same target path should fail to acquire lock and return Aborted error.
	targetPath1Unpublishresp := runRequest(&nodeRequestConfig{
		nodeUnpublishReq: &csi.NodeUnpublishVolumeRequest{
			VolumeId:   testVolumeID,
			TargetPath: targetPath1,
		},
	})
	validateExpectedError(t, targetPath1Unpublishresp, operationUnblocker, codes.Aborted)

	// Node publish succeeds for a second target path 'targetPath2'.
	targetPath2Publishresp2 := runRequest(&nodeRequestConfig{
		nodePublishReq: &csi.NodePublishVolumeRequest{
			VolumeId:          testVolumeID,
			StagingTargetPath: stagingTargetPath,
			TargetPath:        targetPath2,
			VolumeCapability:  testVolumeCapability,
			VolumeContext:     testVolumeAttributes,
		},
	})
	nodepublishOpTargetPath2Unblocker := <-operationUnblocker
	nodepublishOpTargetPath2Unblocker <- struct{}{}
	if err := <-targetPath2Publishresp2; err != nil {
		t.Errorf("Unexpected error: %v", err)
	}

	// Node unpublish succeeds for second target path.
	targetPath2Unpublishresp := runRequest(&nodeRequestConfig{
		nodeUnpublishReq: &csi.NodeUnpublishVolumeRequest{
			VolumeId:   testVolumeID,
			TargetPath: targetPath2,
		},
	})
	if err := <-targetPath2Unpublishresp; err != nil {
		t.Errorf("Unexpected error: %v", err)
	}

	// Unblock first node publish, and success expected.
	nodepublishOpTargetPath1Unblocker <- struct{}{}
	if err := <-targetPath1Publishresp; err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
}

func validateExpectedError(t *testing.T, errResp <-chan error, operationUnblocker chan chan struct{}, _ codes.Code) {
	t.Helper()
	select {
	case err := <-errResp:
		if err != nil {
			serverError, ok := status.FromError(err)
			if !ok {
				t.Fatalf("Could not get error status code from err: %v", err)
			}
			if serverError.Code() != codes.Aborted {
				t.Errorf("Expected error code: %v, got: %v. err : %v", codes.Aborted, serverError.Code(), err)
			}
		} else {
			t.Errorf("Expected error: %v, got no error", codes.Aborted)
		}
	case <-operationUnblocker:
		t.Errorf("The operation should have been aborted, but was started")
	}
}

func TestSetVolumeOwnershipOwner(t *testing.T) {
	t.Parallel()
	fsGroup := int64(3000)
	currentUID := os.Geteuid()
	if currentUID != 0 {
		t.Skip("running as non-root")
	}
	currentGID := os.Getgid()

	tests := []struct {
		description string
		fsGroup     string
		assertFunc  func(path string) error
	}{
		{
			description: "fsGroup=nil",
			fsGroup:     "",
			assertFunc: func(path string) error {
				if !verifyFileOwner(path, currentUID, currentGID) {
					return fmt.Errorf("invalid owner on %s", path)
				}

				return nil
			},
		},
		{
			description: "*fsGroup=3000",
			fsGroup:     "3000",
			assertFunc: func(path string) error {
				if !verifyFileOwner(path, currentUID, int(fsGroup)) {
					return fmt.Errorf("invalid owner on %s", path)
				}

				return nil
			},
		},
	}

	for _, test := range tests {
		t.Run(test.description, func(t *testing.T) {
			t.Parallel()
			tmpDir, err := utiltesting.MkTmpdir("volume_linux_ownership")
			if err != nil {
				t.Fatalf("error creating temp dir: %v", err)
			}

			defer func() {
				_ = os.RemoveAll(tmpDir)
			}()

			err = setVolumeOwnershipTopLevel(testVolumeID, tmpDir, test.fsGroup, false)
			if err != nil {
				t.Errorf("for %s error changing ownership with: %v", test.description, err)
			}
			err = test.assertFunc(tmpDir)
			if err != nil {
				t.Errorf("for %s error verifying permissions with: %v", test.description, err)
			}
		})
	}
}

// verifyFileOwner checks if given path is owned by uid and gid.
// It returns true if it is otherwise false.
func verifyFileOwner(path string, uid, gid int) bool {
	info, err := os.Lstat(path)
	if err != nil {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil {
		return false
	}

	if int(stat.Uid) != uid || int(stat.Gid) != gid {
		return false
	}

	return true
}

func TestPublishIAMVolume(t *testing.T) {
	// Isolate package-level global Go directory Root and inject hermetic MetadataService
	oldRoot := GlobalMountRoot
	defer func() { GlobalMountRoot = oldRoot }()
	GlobalMountRoot = t.TempDir()

	testVolumeID := "test-vol-publish-iam"
	testTargetPath := filepath.Join(t.TempDir(), "target-mount")
	testPodUID := "pod-uid-1234"
	testNamespace := "default"
	testServiceAccount := "lustre-client-sa"
	testPrincipal := fmt.Sprintf("%s/%s", testNamespace, testServiceAccount)
	testKey := computeHash(testVolumeID + testPrincipal)

	testGlobalMountPath := filepath.Join(GlobalMountRoot, testKey, "mount")
	testTokenPath := filepath.Join(GlobalMountRoot, testKey, "token")
	testRefPath := filepath.Join(GlobalMountRoot, testKey, "refs", testPodUID)

	validTokenJSON := `{"test-project.svc.id.goog": {"token": "jwt-token", "expirationTimestamp": "2027-01-01T00:00:00Z"}}`

	validVC := map[string]string{
		keyPodUID:         testPodUID,
		keyPodNamespace:   testNamespace,
		keyServiceAccount: testServiceAccount,
		keyServiceToken:   validTokenJSON,
		keyInstanceIP:     testIP,
		keyFilesystem:     testFilesystem,
	}

	cases := []struct {
		name           string
		targetMounted  bool
		existingMounts []mount.MountPoint
		preCreateRef   bool
		vc             map[string]string
		mountErr       error
		failTarget     string
		expectedMounts []*mount.MountPoint
		expectErr      bool
		expectToken    string
	}{
		{
			name:          "valid initial publish",
			targetMounted: false,
			vc:            validVC,
			expectedMounts: []*mount.MountPoint{
				{
					Device: testIP + "@tcp:/" + testFilesystem,
					Path:   testGlobalMountPath,
					Type:   "lustre",
					Opts:   []string{"user=gke-wi://default/lustre-client-sa+" + testKey},
				},
				{
					Device: testIP + "@tcp:/" + testFilesystem, // FakeMounter resolves bind mount source to underlying device
					Path:   testTargetPath,
					Type:   "lustre",
					Opts:   []string{"bind"},
				},
			},
			expectErr:   false,
			expectToken: "jwt-token",
		},
		{
			name:          "valid republish/token refresh (already mounted)",
			targetMounted: true,
			existingMounts: []mount.MountPoint{
				{Device: testIP + "@tcp:/" + testFilesystem, Path: testGlobalMountPath},
				{Device: testGlobalMountPath, Path: testTargetPath},
			},
			preCreateRef: true,
			vc: map[string]string{
				keyPodUID:         testPodUID,
				keyPodNamespace:   testNamespace,
				keyServiceAccount: testServiceAccount,
				keyServiceToken:   `{"test-project.svc.id.goog": {"token": "newer-refreshed-jwt-token"}}`,
				keyInstanceIP:     testIP,
				keyFilesystem:     testFilesystem,
			},
			expectedMounts: []*mount.MountPoint{
				{Device: testIP + "@tcp:/" + testFilesystem, Path: testGlobalMountPath},
				{Device: testGlobalMountPath, Path: testTargetPath},
			},
			expectErr:   false,
			expectToken: "newer-refreshed-jwt-token",
		},
		{
			name: "missing Pod UID",
			vc: map[string]string{
				keyPodNamespace:   testNamespace,
				keyServiceAccount: testServiceAccount,
			},
			expectErr: true,
		},
		{
			name: "missing Pod Namespace",
			vc: map[string]string{
				keyPodUID:         testPodUID,
				keyServiceAccount: testServiceAccount,
			},
			expectErr: true,
		},
		{
			name: "missing ServiceAccount",
			vc: map[string]string{
				keyPodUID:       testPodUID,
				keyPodNamespace: testNamespace,
			},
			expectErr: true,
		},
		{
			name: "missing Workload Identity token",
			vc: map[string]string{
				keyPodUID:         testPodUID,
				keyPodNamespace:   testNamespace,
				keyServiceAccount: testServiceAccount,
			},
			expectErr: true,
		},
		{
			name:          "bind mount failure returns error",
			targetMounted: false,
			vc:            validVC,
			mountErr:      fmt.Errorf("simulated bind mount failure"),
			failTarget:    testTargetPath,
			expectErr:     true,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			// Wipe GlobalMountRoot cleanly before each run
			if err := os.RemoveAll(GlobalMountRoot); err != nil {
				t.Fatalf("Failed to remove global mount root: %v", err)
			}

			if tc.preCreateRef {
				if err := makeGlobalDirs(testKey); err != nil {
					t.Fatalf("Failed to create global dirs: %v", err)
				}
				if err := addPodReference(testKey, testPodUID, testVolumeID); err != nil {
					t.Fatalf("Failed to pre-create ref: %v", err)
				}
			}

			fm := &mount.FakeMounter{MountPoints: tc.existingMounts}
			if fm.MountPoints == nil {
				fm.MountPoints = []mount.MountPoint{}
			}
			mounter := &fakeMounter{FakeMounter: fm, mountErr: tc.mountErr, failTarget: tc.failTarget}

			driver := initTestDriver(t)
			driver.config.MetadataService = &mockMetadataService{project: "test-project"}

			ns := newNodeServer(driver, mounter).(*nodeServer)

			normalizedVC := make(map[string]string)
			for k, v := range tc.vc {
				normalizedVC[normalize(k)] = v
			}

			err := ns.publishIAMVolume(testVolumeID, testTargetPath, tc.targetMounted, normalizedVC, []string{"bind"}, testVolumeCapability)
			if (err != nil) != tc.expectErr {
				t.Fatalf("Test %q: expected error %v, got %v", tc.name, tc.expectErr, err)
			}

			if !tc.expectErr {
				validateMountPoint(t, tc.name, fm, tc.expectedMounts...)

				// Verify token file on disk
				content, err := os.ReadFile(testTokenPath)
				if err != nil {
					t.Fatalf("Failed to read token file: %v", err)
				}
				if string(content) != tc.expectToken {
					t.Errorf("Expected token %q, got %q", tc.expectToken, string(content))
				}

				// Verify pod reference on disk
				refContent, err := os.ReadFile(testRefPath)
				if err != nil {
					t.Fatalf("Failed to read pod reference file: %v", err)
				}
				if string(refContent) != testVolumeID {
					t.Errorf("Expected ref content %q, got %q", testVolumeID, string(refContent))
				}
			}
		})
	}
}

func TestParseToken(t *testing.T) {
	t.Parallel()

	validJSON := `{"test-project.svc.id.goog":{"token":"test-token","expirationTimestamp":"2099-01-01T00:00:00Z"}}`

	cases := []struct {
		name        string
		tokenJSON   string
		audience    string
		expectToken string
		expectErr   bool
	}{
		{
			name:        "valid token parsed",
			tokenJSON:   validJSON,
			audience:    "test-project.svc.id.goog",
			expectToken: "test-token",
			expectErr:   false,
		},
		{
			name:        "audience not found",
			tokenJSON:   validJSON,
			audience:    "other-project.svc.id.goog",
			expectToken: "",
			expectErr:   true,
		},
		{
			name:        "invalid json",
			tokenJSON:   `{malformed-json`,
			audience:    "test-project.svc.id.goog",
			expectToken: "",
			expectErr:   true,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			token, err := parseToken(tc.tokenJSON, tc.audience)
			if (err != nil) != tc.expectErr {
				t.Fatalf("Test %q: expected error %v, got %v", tc.name, tc.expectErr, err)
			}
			if token != tc.expectToken {
				t.Fatalf("Test %q: expected token %q, got %q", tc.name, tc.expectToken, token)
			}
		})
	}
}

func TestUpdateTokenFile(t *testing.T) {
	oldRoot := GlobalMountRoot
	defer func() { GlobalMountRoot = oldRoot }()
	GlobalMountRoot = t.TempDir()

	testKey := "71ac09669e8a14e7dca05e1bb9ef37802de0448c37bb195c6c1b68a5ff3f2c2e"
	testToken := "secure-jwt-token"

	// Create global layout
	if err := makeGlobalDirs(testKey); err != nil {
		t.Fatalf("Failed to create global layout: %v", err)
	}

	if err := updateTokenFile(testKey, testToken); err != nil {
		t.Fatalf("updateTokenFile failed: %v", err)
	}

	tokenPath := filepath.Join(GlobalMountRoot, testKey, "token")
	info, err := os.Stat(tokenPath)
	if err != nil {
		t.Fatalf("Failed to stat token file: %v", err)
	}

	if info.Mode().Perm() != 0600 {
		t.Fatalf("Expected token file permission 0600, got %o", info.Mode().Perm())
	}

	content, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatalf("Failed to read token file: %v", err)
	}

	if string(content) != testToken {
		t.Fatalf("Expected token content %q, got %q", testToken, string(content))
	}
}

func TestMountGlobalIAM(t *testing.T) {
	// Do not use t.Parallel() because we modify GlobalMountRoot
	oldRoot := GlobalMountRoot
	defer func() { GlobalMountRoot = oldRoot }()
	GlobalMountRoot = t.TempDir()

	testVolumeID := "test-vol-iam"
	testPrincipal := "test-ns/test-sa"
	testKey := "71ac09669e8a14e7dca05e1bb9ef37802de0448c37bb195c6c1b68a5ff3f2c2e"
	testGlobalMountPath := filepath.Join(GlobalMountRoot, testKey, "mount")

	cases := []struct {
		name          string
		vc            map[string]string
		volCap        *csi.VolumeCapability
		mountErr      error
		expectedMount *mount.MountPoint
		expectErr     bool
	}{
		{
			name: "valid direct IP and FSName",
			vc: map[string]string{
				keyInstanceIP: testIP,
				keyFilesystem: testFilesystem,
			},
			volCap: testVolumeCapability,
			expectedMount: &mount.MountPoint{
				Device: testIP + "@tcp:/" + testFilesystem,
				Path:   testGlobalMountPath,
				Type:   "lustre",
				Opts:   []string{"user=gke-wi://test-ns/test-sa+71ac09669e8a14e7dca05e1bb9ef37802de0448c37bb195c6c1b68a5ff3f2c2e"},
			},
			expectErr: false,
		},
		{
			name: "valid mountPoint parse",
			vc: map[string]string{
				keyMountPoint: "10.0.0.1@tcp:/customfs",
			},
			volCap: testVolumeCapability,
			expectedMount: &mount.MountPoint{
				Device: "10.0.0.1@tcp:/customfs",
				Path:   testGlobalMountPath,
				Type:   "lustre",
				Opts:   []string{"user=gke-wi://test-ns/test-sa+71ac09669e8a14e7dca05e1bb9ef37802de0448c37bb195c6c1b68a5ff3f2c2e"},
			},
			expectErr: false,
		},
		{
			name: "valid with additional mount flags",
			vc: map[string]string{
				keyInstanceIP: testIP,
				keyFilesystem: testFilesystem,
			},
			volCap: &csi.VolumeCapability{
				AccessType: &csi.VolumeCapability_Mount{
					Mount: &csi.VolumeCapability_MountVolume{
						MountFlags: []string{"foo", "bar"},
					},
				},
				AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER},
			},
			expectedMount: &mount.MountPoint{
				Device: testIP + "@tcp:/" + testFilesystem,
				Path:   testGlobalMountPath,
				Type:   "lustre",
				Opts:   []string{"user=gke-wi://test-ns/test-sa+71ac09669e8a14e7dca05e1bb9ef37802de0448c37bb195c6c1b68a5ff3f2c2e", "foo", "bar"},
			},
			expectErr: false,
		},
		{
			name: "missing IP",
			vc: map[string]string{
				keyFilesystem: testFilesystem,
			},
			expectErr: true,
		},
		{
			name: "missing FSName",
			vc: map[string]string{
				keyInstanceIP: testIP,
			},
			expectErr: true,
		},
		{
			name: "invalid mountPoint format",
			vc: map[string]string{
				keyMountPoint: "invalid-spec-without-at-sign",
			},
			expectErr: true,
		},
		{
			name: "mount failure returns error",
			vc: map[string]string{
				keyInstanceIP: testIP,
				keyFilesystem: testFilesystem,
			},
			volCap:    testVolumeCapability,
			mountErr:  fmt.Errorf("simulated kernel mount error"),
			expectErr: true,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			fm := &mount.FakeMounter{MountPoints: []mount.MountPoint{}}
			mounter := &fakeMounter{FakeMounter: fm, mountErr: tc.mountErr}

			driver := initTestDriver(t)
			driver.config.MetadataService = &mockMetadataService{project: "test-project"}

			ns := newNodeServer(driver, mounter).(*nodeServer)

			err := ns.mountGlobalIAM(testVolumeID, testGlobalMountPath, testPrincipal, testKey, tc.vc, tc.volCap)
			if tc.expectErr && err == nil {
				t.Fatalf("Test %q: expected an error but got none", tc.name)
			}
			if !tc.expectErr && err != nil {
				t.Fatalf("Test %q: unexpected error: %v", tc.name, err)
			}
			if !tc.expectErr {
				validateMountPoint(t, tc.name, fm, tc.expectedMount)
			}
		})
	}
}
