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
	"os"
	"os/exec"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog/v2"
	mount "k8s.io/mount-utils"
)

const (
	// DefaultUnmountTimeout is the maximum duration to wait for a standard umount.
	DefaultUnmountTimeout = 15 * time.Second
	// DefaultForceUnmountTimeout is the maximum duration to wait for a force umount (-f).
	DefaultForceUnmountTimeout = 10 * time.Second
	// DefaultLazyUnmountTimeout is the maximum duration to wait for a lazy umount (-l).
	DefaultLazyUnmountTimeout = 5 * time.Second

	errNotMounted = "not mounted"
)

type unmountExecFunc func(ctx context.Context, args ...string) ([]byte, error)

// execUmount executes umount with given arguments and context.
func (s *nodeServer) execUmount(ctx context.Context, args ...string) ([]byte, error) {
	if s.unmountExec != nil {
		return s.unmountExec(ctx, args...)
	}

	// In test environments using FakeMounter, delegate to FakeMounter.Unmount if no custom unmountExec is set.
	if fm, ok := s.mounter.(*mount.FakeMounter); ok {
		target := args[len(args)-1]
		err := fm.Unmount(target)
		return nil, err
	}

	cmd := exec.CommandContext(ctx, "umount", args...)
	return cmd.CombinedOutput()
}

// unmountPath unmounts the target path using a cascading fallback strategy:
// 1. Standard umount (DefaultUnmountTimeout)
// 2. Force umount -f (DefaultForceUnmountTimeout) if standard umount times out or fails
// 3. Lazy umount -l (DefaultLazyUnmountTimeout) if force umount times out or fails
// After successfully detaching the mount, the directory is deleted.
func (s *nodeServer) unmountPath(ctx context.Context, target string) error {
	notMnt, err := s.mounter.IsLikelyNotMountPoint(target)
	if err != nil && !os.IsNotExist(err) {
		if isCorruptedMnt(err) {
			klog.V(4).Infof("unmountPath: target %q is corrupted mount, proceeding with unmount", target)
			notMnt = false
		} else {
			klog.Warningf("unmountPath: error checking if %q is a mount point: %v", target, err)
		}
	}

	if notMnt {
		klog.V(5).Infof("unmountPath: target path %s is not mounted, removing directory", target)
		return removeDir(target)
	}

	// Phase 1: Standard unmount with bounded timeout
	klog.V(4).Infof("unmountPath: attempting standard unmount on %s", target)
	ctxStandard, cancelStandard := context.WithTimeout(ctx, DefaultUnmountTimeout)
	out, err := s.execUmount(ctxStandard, target)
	cancelStandard()

	if err == nil || isNotMounted(err, out) {
		klog.V(4).Infof("unmountPath: standard unmount succeeded on %s", target)
		return removeDir(target)
	}

	// Phase 2: Force unmount (-f) with bounded timeout
	klog.Warningf("unmountPath: standard unmount failed or timed out on %s (err: %v, out: %s). Attempting force unmount (umount -f)...", target, err, string(out))
	ctxForce, cancelForce := context.WithTimeout(ctx, DefaultForceUnmountTimeout)
	out, err = s.execUmount(ctxForce, "-f", target)
	cancelForce()

	if err == nil || isNotMounted(err, out) {
		klog.V(4).Infof("unmountPath: force unmount succeeded on %s", target)
		return removeDir(target)
	}

	// Phase 3: Lazy unmount (-l / MNT_DETACH) as ultimate fallback to avoid wedging the node
	klog.Warningf("unmountPath: force unmount failed or timed out on %s (err: %v, out: %s). Falling back to lazy unmount (umount -l)...", target, err, string(out))
	ctxLazy, cancelLazy := context.WithTimeout(ctx, DefaultLazyUnmountTimeout)
	out, err = s.execUmount(ctxLazy, "-l", target)
	cancelLazy()

	if err == nil || isNotMounted(err, out) {
		klog.V(4).Infof("unmountPath: lazy unmount succeeded on %s", target)
		return removeDir(target)
	}

	return status.Errorf(codes.Internal, "failed to unmount target %q after standard, force, and lazy attempts: %v (output: %s)", target, err, string(out))
}

func isNotMounted(err error, output []byte) bool {
	if err != nil && strings.Contains(err.Error(), errNotMounted) {
		return true
	}
	if strings.Contains(string(output), errNotMounted) {
		return true
	}
	return false
}

func removeDir(target string) error {
	if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
		return status.Errorf(codes.Internal, "failed to remove mount directory %q: %v", target, err)
	}
	return nil
}
