/*
Copyright 2026 Google LLC

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
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	csi "github.com/container-storage-interface/spec/lib/go/csi"
	mount "k8s.io/mount-utils"
)

func TestUnmountPath_CascadingFallback(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		mounts        []mount.MountPoint
		execResponses map[string]struct {
			out []byte
			err error
		}
		expectedCalls    []string
		expectErr        bool
		dirExistsBefore  bool
		expectDirRemoved bool
	}{
		{
			name: "Standard unmount succeeds on first attempt",
			mounts: []mount.MountPoint{
				{Device: testDevice, Path: "/test/staging"},
			},
			execResponses: map[string]struct {
				out []byte
				err error
			}{
				"umount /test/staging": {out: nil, err: nil},
			},
			expectedCalls:    []string{"umount /test/staging"},
			expectErr:        false,
			dirExistsBefore:  true,
			expectDirRemoved: true,
		},
		{
			name: "Standard unmount fails, force unmount (-f) succeeds",
			mounts: []mount.MountPoint{
				{Device: testDevice, Path: "/test/staging"},
			},
			execResponses: map[string]struct {
				out []byte
				err error
			}{
				"umount /test/staging":    {out: []byte("device busy"), err: errors.New("exit status 32")},
				"umount -f /test/staging": {out: nil, err: nil},
			},
			expectedCalls: []string{
				"umount /test/staging",
				"umount -f /test/staging",
			},
			expectErr:        false,
			dirExistsBefore:  true,
			expectDirRemoved: true,
		},
		{
			name: "Standard and force unmount fail, lazy unmount (-l) succeeds",
			mounts: []mount.MountPoint{
				{Device: testDevice, Path: "/test/staging"},
			},
			execResponses: map[string]struct {
				out []byte
				err error
			}{
				"umount /test/staging":    {out: []byte("target is busy"), err: errors.New("exit status 32")},
				"umount -f /test/staging": {out: []byte("operation not supported"), err: errors.New("exit status 1")},
				"umount -l /test/staging": {out: nil, err: nil},
			},
			expectedCalls: []string{
				"umount /test/staging",
				"umount -f /test/staging",
				"umount -l /test/staging",
			},
			expectErr:        false,
			dirExistsBefore:  true,
			expectDirRemoved: true,
		},
		{
			name: "All unmount attempts fail, returns error",
			mounts: []mount.MountPoint{
				{Device: testDevice, Path: "/test/staging"},
			},
			execResponses: map[string]struct {
				out []byte
				err error
			}{
				"umount /test/staging":    {out: []byte("error 1"), err: errors.New("exit status 1")},
				"umount -f /test/staging": {out: []byte("error 2"), err: errors.New("exit status 2")},
				"umount -l /test/staging": {out: []byte("fatal error"), err: errors.New("exit status 3")},
			},
			expectedCalls: []string{
				"umount /test/staging",
				"umount -f /test/staging",
				"umount -l /test/staging",
			},
			expectErr:        true,
			dirExistsBefore:  true,
			expectDirRemoved: false,
		},
		{
			name:   "Path is not a mount point, cleans up directory directly",
			mounts: []mount.MountPoint{},
			execResponses: map[string]struct {
				out []byte
				err error
			}{},
			expectedCalls:    nil,
			expectErr:        false,
			dirExistsBefore:  true,
			expectDirRemoved: true,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tmpDir := t.TempDir()
			targetPath := filepath.Join(tmpDir, "staging")

			if tc.dirExistsBefore {
				if err := os.MkdirAll(targetPath, 0750); err != nil {
					t.Fatalf("Failed to create test directory: %v", err)
				}
			}

			// Adjust mount paths to match targetPath
			var mountPoints []mount.MountPoint
			for _, m := range tc.mounts {
				mountPoints = append(mountPoints, mount.MountPoint{
					Device: m.Device,
					Path:   targetPath,
				})
			}

			fm := &mount.FakeMounter{MountPoints: mountPoints}
			testEnv := initTestNodeServer(t)
			ns := testEnv.ns.(*nodeServer)
			ns.mounter = fm

			var mu sync.Mutex
			var recordedCalls []string

			ns.unmountExec = func(ctx context.Context, args ...string) ([]byte, error) {
				mu.Lock()
				defer mu.Unlock()

				callKey := "umount"
				for _, arg := range args {
					if arg == targetPath {
						callKey += " /test/staging"
					} else {
						callKey += " " + arg
					}
				}
				recordedCalls = append(recordedCalls, callKey)

				resp, exists := tc.execResponses[callKey]
				if !exists {
					return nil, errors.New("unexpected command call: " + callKey)
				}
				return resp.out, resp.err
			}

			err := ns.unmountPath(targetPath)
			if (err != nil) != tc.expectErr {
				t.Fatalf("unmountPath() error = %v, expectErr = %v", err, tc.expectErr)
			}

			mu.Lock()
			if len(recordedCalls) != len(tc.expectedCalls) {
				t.Errorf("Recorded calls = %v, expected = %v", recordedCalls, tc.expectedCalls)
			} else {
				for i := range recordedCalls {
					if recordedCalls[i] != tc.expectedCalls[i] {
						t.Errorf("Call %d = %q, expected %q", i, recordedCalls[i], tc.expectedCalls[i])
					}
				}
			}
			mu.Unlock()

			_, statErr := os.Stat(targetPath)
			dirExists := !os.IsNotExist(statErr)
			if tc.expectDirRemoved && dirExists {
				t.Errorf("Expected directory %s to be removed, but it still exists", targetPath)
			}
			if !tc.expectDirRemoved && tc.dirExistsBefore && !dirExists {
				t.Errorf("Expected directory %s to remain, but it was removed", targetPath)
			}
		})
	}
}

func TestUnmountPath_ContextTimeoutAndLockRelease(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	targetPath := filepath.Join(tmpDir, "staging")
	if err := os.MkdirAll(targetPath, 0750); err != nil {
		t.Fatalf("Failed to create test directory: %v", err)
	}

	fm := &mount.FakeMounter{
		MountPoints: []mount.MountPoint{
			{Device: testDevice, Path: targetPath},
		},
	}
	testEnv := initTestNodeServer(t)
	ns := testEnv.ns.(*nodeServer)
	ns.mounter = fm

	// Simulate hanging commands that simulate timeout / deadline exceeded
	ns.unmountExec = func(ctx context.Context, args ...string) ([]byte, error) {
		return nil, context.DeadlineExceeded
	}

	req := &csi.NodeUnstageVolumeRequest{
		VolumeId:          testVolumeID,
		StagingTargetPath: targetPath,
	}

	start := time.Now()
	_, err := ns.NodeUnstageVolume(context.Background(), req)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("Expected NodeUnstageVolume to fail due to timeout, got success")
	}

	// Verify that the call returned promptly without blocking forever
	if elapsed > 2*time.Second {
		t.Errorf("NodeUnstageVolume took %v, expected return within ~1s", elapsed)
	}

	// CRITICAL TEST: Verify that the volume lock was released so that a subsequent call can acquire it
	acquired := ns.volumeLocks.TryAcquire(targetPath)
	if !acquired {
		t.Errorf("Volume lock for %s was not released after NodeUnstageVolume failure!", targetPath)
	} else {
		ns.volumeLocks.Release(targetPath)
	}
}
