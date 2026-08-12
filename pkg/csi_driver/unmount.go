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
	"bytes"
	"context"
	"os"
	"os/exec"
	"reflect"
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

// execUmount executes umount with given arguments and context without blocking on D-state tasks.
func (s *nodeServer) execUmount(ctx context.Context, args ...string) ([]byte, error) {
	if s.unmountExec != nil {
		return s.unmountExec(ctx, args...)
	}

	// In test environments using FakeMounter, delegate to FakeMounter.Unmount if no custom unmountExec is set.
	if fm := extractFakeMounter(s.mounter); fm != nil {
		var target string
		if len(args) > 0 {
			target = args[len(args)-1]
		}
		err := fm.Unmount(target)
		return nil, err
	}

	cmd := exec.Command("umount", args...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	select {
	case err := <-done:
		return buf.Bytes(), err
	case <-ctx.Done():
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		return nil, ctx.Err()
	}
}

// unmountPath unmounts the target path using a cascading fallback strategy:
// 1. Standard umount (DefaultUnmountTimeout = 15s)
// 2. Force umount -f (DefaultForceUnmountTimeout = 10s) if standard umount times out or fails
// 3. Lazy umount -l (DefaultLazyUnmountTimeout = 5s) if force umount times out or fails
// After successfully detaching the mount, the directory is deleted.
//
// NOTE on Context Handling:
// unmountPath intentionally does NOT accept or derive from the caller's gRPC context.
// Instead, it manages its own internal timeouts using context.Background().
// Rationale: If a caller's request context was cancelled or timed out (e.g. Kubelet 2-minute deadline
// or client disconnect), deriving from that cancelled context would cause all three unmount phases
// to abort immediately without actually executing umount -f or umount -l.
// Because unmounting is a critical cleanup operation, it must run to completion to prevent leaked
// mount points and wedged kernel/lock states. The total execution time is strictly bounded by
// DefaultUnmountTimeout + DefaultForceUnmountTimeout + DefaultLazyUnmountTimeout (<= 30s), which is
// well within Kubelet's retry timeout.
func (s *nodeServer) unmountPath(target string) error {
	notMnt, err := s.mounter.IsLikelyNotMountPoint(target)
	if err != nil && !os.IsNotExist(err) {
		if isCorruptedMnt(err) {
			klog.V(4).Infof("unmountPath: target %q is corrupted mount, proceeding with unmount", target)
		} else {
			klog.Warningf("unmountPath: error checking if %q is a mount point: %v, proceeding with unmount", target, err)
		}
		notMnt = false
	}

	if notMnt {
		klog.V(5).Infof("unmountPath: target path %s is not mounted, removing directory", target)
		return removeDir(target)
	}

	// Phase 1: Standard unmount with bounded timeout
	klog.V(4).Infof("unmountPath: attempting standard unmount on %s", target)
	ctxStandard, cancelStandard := context.WithTimeout(context.Background(), DefaultUnmountTimeout)
	out, err := s.execUmount(ctxStandard, target)
	cancelStandard()

	if err == nil || isNotMounted(err, out) {
		klog.V(4).Infof("unmountPath: standard unmount succeeded on %s", target)
		return removeDir(target)
	}

	// Phase 2: Force unmount (-f) with bounded timeout
	klog.Warningf("unmountPath: standard unmount failed or timed out on %s (err: %v, out: %s). Attempting force unmount (umount -f)...", target, err, string(out))
	ctxForce, cancelForce := context.WithTimeout(context.Background(), DefaultForceUnmountTimeout)
	out, err = s.execUmount(ctxForce, "-f", target)
	cancelForce()

	if err == nil || isNotMounted(err, out) {
		klog.V(4).Infof("unmountPath: force unmount succeeded on %s", target)
		return removeDir(target)
	}

	// Phase 3: Lazy unmount (-l / MNT_DETACH) as ultimate fallback to avoid wedging the node
	klog.Warningf("unmountPath: force unmount failed or timed out on %s (err: %v, out: %s). Falling back to lazy unmount (umount -l)...", target, err, string(out))
	ctxLazy, cancelLazy := context.WithTimeout(context.Background(), DefaultLazyUnmountTimeout)
	out, err = s.execUmount(ctxLazy, "-l", target)
	cancelLazy()

	if err == nil || isNotMounted(err, out) {
		klog.V(4).Infof("unmountPath: lazy unmount succeeded on %s", target)
		return removeDir(target)
	}

	return status.Errorf(codes.Internal, "failed to unmount target %q after standard, force, and lazy attempts: %v (output: %s)", target, err, string(out))
}

func isNotMounted(err error, output []byte) bool {
	if err != nil && strings.Contains(strings.ToLower(err.Error()), errNotMounted) {
		return true
	}
	if strings.Contains(strings.ToLower(string(output)), errNotMounted) {
		return true
	}
	return false
}

func extractFakeMounter(m mount.Interface) *mount.FakeMounter {
	if m == nil {
		return nil
	}
	if fm, ok := m.(*mount.FakeMounter); ok {
		return fm
	}
	// Check if the mounter embeds *mount.FakeMounter (e.g. fakeMounter in tests)
	val := reflect.ValueOf(m)
	if val.Kind() == reflect.Ptr && !val.IsNil() {
		val = val.Elem()
	}
	if val.Kind() == reflect.Struct {
		field := val.FieldByName("FakeMounter")
		if field.IsValid() {
			if fm, ok := field.Interface().(*mount.FakeMounter); ok {
				return fm
			}
		}
	}
	return nil
}

func removeDir(target string) error {
	if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
		return status.Errorf(codes.Internal, "failed to remove mount directory %q: %v", target, err)
	}
	return nil
}
