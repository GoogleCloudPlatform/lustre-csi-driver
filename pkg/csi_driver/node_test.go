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
	"syscall"
	"testing"

	csi "github.com/container-storage-interface/spec/lib/go/csi"
	"golang.org/x/net/context"
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
		attrInstanceIP:     testIP,
		attrFilesystemName: testFilesystem,
	}
)

type nodeServerTestEnv struct {
	ns csi.NodeServer
	fm *mount.FakeMounter
}

type fakeBlockingMounter struct {
	*mount.FakeMounter
	// 'operationUnblocker' channel is used to block the execution of the respective function using it.
	// This is done by sending a channel of empty struct over 'operationUnblocker' channel,
	// and wait until the tester gives a go-ahead to proceed further in the execution of the function.
	operationUnblocker chan chan struct{}
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

	return &nodeServerTestEnv{
		ns: newNodeServer(driver, mounter),
		fm: mounter,
	}
}

func initBlockingTestNodeServer(t *testing.T, operationUnblocker chan chan struct{}) *nodeServerTestEnv {
	t.Helper()
	mounter := newFakeBlockingMounter(operationUnblocker)
	driver := initTestDriver(t)

	return &nodeServerTestEnv{
		ns: newNodeServer(driver, mounter),
		fm: nil,
	}
}

func newFakeBlockingMounter(operationUnblocker chan chan struct{}) *fakeBlockingMounter {
	return &fakeBlockingMounter{
		FakeMounter:        &mount.FakeMounter{MountPoints: []mount.MountPoint{}},
		operationUnblocker: operationUnblocker,
	}
}

func (m *fakeBlockingMounter) MountSensitiveWithoutSystemd(source string, target string, fstype string, options []string, sensitiveOptions []string) error {
	execute := make(chan struct{})
	m.operationUnblocker <- execute
	<-execute

	return m.FakeMounter.MountSensitiveWithoutSystemd(source, target, fstype, options, sensitiveOptions)
}

func TestNodeStageVolme(t *testing.T) {
	t.Parallel()
	// Setup mount staging path
	base, err := os.MkdirTemp("", "node-stage-")
	if err != nil {
		t.Fatalf("Failed to setup testdir: %v", err)
	}
	stagingTargetPath := filepath.Join(base, "staging")

	defer os.RemoveAll(base)
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
			name:   "same fsname already mounted",
			mounts: []mount.MountPoint{{Device: testDevice, Path: stagingTargetPath}},
			req: &csi.NodeStageVolumeRequest{
				VolumeId:          testVolumeID,
				StagingTargetPath: stagingTargetPath,
				VolumeCapability:  testVolumeCapability,
				VolumeContext: map[string]string{
					attrInstanceIP:     "127.0.0.2",
					attrFilesystemName: testFilesystem,
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
	}
	for _, test := range cases {
		testEnv := initTestNodeServer(t)
		if test.mounts != nil {
			testEnv.fm.MountPoints = test.mounts
		}

		_, err = testEnv.ns.NodeStageVolume(context.TODO(), test.req)
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
	base, err := os.MkdirTemp("", "node-stage-")
	if err != nil {
		t.Fatalf("Failed to setup testdir: %v", err)
	}
	defer os.RemoveAll(base)
	testStagingPath := filepath.Join(base, "staging")
	if err = os.MkdirAll(testStagingPath, defaultPerm); err != nil {
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

		_, err = testEnv.ns.NodeUnstageVolume(context.TODO(), test.req)
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
	t.Parallel()
	defaultPerm := os.FileMode(0o750) + os.ModeDir

	// Setup mount target path
	base, err := os.MkdirTemp("", "node-publish-")
	if err != nil {
		t.Fatalf("Failed to setup testdir: %v", err)
	}
	testTargetPath := filepath.Join(base, "target")
	if err = os.MkdirAll(testTargetPath, defaultPerm); err != nil {
		t.Fatalf("Failed to setup target path: %v", err)
	}
	stagingTargetPath := filepath.Join(base, "staging")
	defer os.RemoveAll(base)
	cases := []struct {
		name          string
		mounts        []mount.MountPoint // already existing mounts
		req           *csi.NodePublishVolumeRequest
		actions       []mount.FakeAction
		expectedMount *mount.MountPoint
		expectErr     bool
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
			actions:       []mount.FakeAction{{Action: mount.FakeActionMount}},
			expectedMount: &mount.MountPoint{Device: stagingTargetPath, Path: testTargetPath, Type: "lustre", Opts: []string{"bind"}},
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
			expectedMount: &mount.MountPoint{Device: "/test-device", Path: testTargetPath},
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
			actions: []mount.FakeAction{{Action: mount.FakeActionMount}},

			expectedMount: &mount.MountPoint{Device: stagingTargetPath, Path: testTargetPath, Type: "lustre", Opts: []string{"bind", "foo", "bar"}},
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
			actions:       []mount.FakeAction{{Action: mount.FakeActionMount}},
			expectedMount: &mount.MountPoint{Device: stagingTargetPath, Path: testTargetPath, Type: "lustre", Opts: []string{"bind", "ro"}},
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

		_, err = testEnv.ns.NodePublishVolume(context.TODO(), test.req)
		if !test.expectErr && err != nil {
			t.Errorf("Test %q failed: %v", test.name, err)
		}
		if test.expectErr && err == nil {
			t.Errorf("Test %q failed: got success", test.name)
		}

		validateMountPoint(t, test.name, testEnv.fm, test.expectedMount)
	}
}

func TestNodeUnpublishVolume(t *testing.T) {
	t.Parallel()
	defaultPerm := os.FileMode(0o750) + os.ModeDir

	// Setup mount target path
	base, err := os.MkdirTemp("", "node-publish-")
	if err != nil {
		t.Fatalf("Failed to setup testdir: %v", err)
	}
	defer os.RemoveAll(base)
	testTargetPath := filepath.Join(base, "target")
	if err = os.MkdirAll(testTargetPath, defaultPerm); err != nil {
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

		_, err = testEnv.ns.NodeUnpublishVolume(context.TODO(), test.req)
		if !test.expectErr && err != nil {
			t.Errorf("Test %q failed: %v", test.name, err)
		}
		if test.expectErr && err == nil {
			t.Errorf("Test %q failed: got success", test.name)
		}

		validateMountPoint(t, test.name, testEnv.fm, test.expectedMount)
	}
}

func validateMountPoint(t *testing.T, name string, fm *mount.FakeMounter, e *mount.MountPoint) {
	t.Helper()
	if e == nil {
		if len(fm.MountPoints) != 0 {
			t.Errorf("Test %q failed: got mounts %+v, expected none", name, fm.MountPoints)
		}

		return
	}

	if mLen := len(fm.MountPoints); mLen != 1 {
		t.Errorf("Test %q failed: got %v mounts(%+v), expected %v", name, mLen, fm.MountPoints, 1)

		return
	}

	a := &fm.MountPoints[0]
	if a.Device != e.Device {
		t.Errorf("Test %q failed: got device %q, expected %q", name, a.Device, e.Device)
	}
	if a.Path != e.Path {
		t.Errorf("Test %q failed: got path %q, expected %q", name, a.Path, e.Path)
	}
	if a.Type != e.Type {
		t.Errorf("Test %q failed: got type %q, expected %q", name, a.Type, e.Type)
	}

	aLen := len(a.Opts)
	eLen := len(e.Opts)
	if aLen != eLen {
		t.Errorf("Test %q failed: got opts length %v, expected %v", name, aLen, eLen)
	}

	for i := range a.Opts {
		aOpt := a.Opts[i]
		eOpt := e.Opts[i]
		if aOpt != eOpt {
			t.Errorf("Test %q failed: got opt %q, expected %q", name, aOpt, eOpt)
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
	basePath, err := os.MkdirTemp("", "node-publish-")
	if err != nil {
		t.Fatalf("Failed to setup testdir: %v", err)
	}
	stagingTargetPath := filepath.Join(basePath, "staging")
	targetPath1 := filepath.Join(basePath, "target1")
	targetPath2 := filepath.Join(basePath, "target2")

	runRequest := func(req *nodeRequestConfig) <-chan error {
		resp := make(chan error)
		go func() {
			var err error
			switch {
			case req.nodePublishReq != nil:
				_, err = ns.ns.NodePublishVolume(context.TODO(), req.nodePublishReq)
			case req.nodeUnpublishReq != nil:
				_, err = ns.ns.NodeUnpublishVolume(context.TODO(), req.nodeUnpublishReq)
			case req.nodeStageReq != nil:
				_, err = ns.ns.NodeStageVolume(context.TODO(), req.nodeStageReq)
			case req.nodeUnstageReq != nil:
				_, err = ns.ns.NodeUnstageVolume(context.TODO(), req.nodeUnstageReq)
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

			defer os.RemoveAll(tmpDir)

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
