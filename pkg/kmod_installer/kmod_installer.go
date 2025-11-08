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

package kmodinstaller

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/lustre-csi-driver/pkg/network"
	"k8s.io/klog/v2"
)

const (
	lnetAcceptPortFile       = "/sys/module/lnet/parameters/accept_port"
	lnetNetworkParameterFile = "/sys/module/lnet/parameters/networks"
	efiPartition             = "/dev/disk/by-label/EFI-SYSTEM"
	grubCfgSuffix            = "/efi/boot/grub.cfg"
	legacyLNetPort           = 6988
	defaultLNetPort          = 988
	cmdTimeout               = 15 * time.Minute
)

func lnetPort(enableLegacyLustrePort bool) int {
	if enableLegacyLustrePort {
		return legacyLNetPort
	}

	return defaultLNetPort
}

func checkLnetPort(expectedPort int) error {
	file, err := os.ReadFile(lnetAcceptPortFile)
	if err != nil {
		if os.IsNotExist(err) {
			klog.V(4).Infof("LNET port file %v not found. Skipping check", lnetAcceptPortFile)

			return nil
		}

		return fmt.Errorf("error reading file %s: %w", lnetAcceptPortFile, err)
	}
	currPort := strings.TrimSpace(string(file))
	expectedPortStr := strconv.Itoa(expectedPort)
	if currPort != expectedPortStr {
		klog.V(3).Infof("LNET port mismatched, Got: %s, Expected: %s", currPort, expectedPortStr)

		return errors.New("node already has lustre kernel modules installed with an outdated lnet.accept_port configuration. please upgrade your node pool to apply the correct settings")
	}

	klog.V(4).Info("LNET_PORT matches configuration. Check passed.")

	return nil
}

func checkLnetNetwork(expectedNics string) error {
	file, err := os.ReadFile(lnetNetworkParameterFile)
	if err != nil {
		if os.IsNotExist(err) {
			klog.V(4).Infof("LNET network parameter file %v not found. Skipping check", lnetNetworkParameterFile)

			return nil
		}

		return fmt.Errorf("error reading file %s: %w", lnetAcceptPortFile, err)
	}
	currNetworkNics := strings.TrimSpace(string(file))
	if currNetworkNics != expectedNics {
		klog.V(3).Infof("LNET network parameter mismatched, Got: %s, Expected: %s", currNetworkNics, expectedNics)

		return errors.New("node already has lustre kernel modules installed with an outdated lnet.networks configuration. please upgrade your node pool to apply the correct settings")
	}

	klog.V(4).Info("Network NICs matches configuration. Check passed.")

	return nil
}

func InstallLustreKmod(ctx context.Context, nodeID string, enableLegacyPort bool, customDkmsArgs []string) error {
	lnetPort := lnetPort(enableLegacyPort)
	if err := checkLnetPort(lnetPort); err != nil {
		return err
	}
	nics, err := network.GetGvnicNames()
	if err != nil {
		return fmt.Errorf("error getting nic names: %w", err)
	}
	if len(nics) == 0 {
		return errors.New("no nics with eth prefix found")
	}
	isMultiNic, err := network.IsMultiRailEnabled(nodeID)
	if err != nil {
		return err
	}

	moduleArg := fmt.Sprintf(`lnet.networks="tcp0(%s)"`, nics[0])
	if isMultiNic {
		if err = checkLnetNetwork(fmt.Sprintf("tcp0(%s)", strings.Join(nics, ","))); err != nil {
			return err
		}
		moduleArg = fmt.Sprintf(`lnet.networks="tcp0(%s)"`, strings.Join(nics, ","))
	}
	cmdCtx, cancel := context.WithTimeout(ctx, cmdTimeout)
	defer cancel()

	// --gcs-bucket: Specifies the GCS bucket containing the driver packages ('cos-default').
	// --latest: Installs the latest available driver version that is compatible with the kernel version running on the current node.
	//           We can’t pin to a specific driver version because GKE nodes can skew from the control plane version.
	//           If we pin to a fixed version and the control plane is ahead of the node, it could result in a kmod installer failure due to the node's kernel being too old for the specified driver version.
	// --kernelmodulestree: Sets the path to the kernel modules directory on the host ('/host_modules').
	// --lsb-release-path: Specifies the path to the lsb-release file on the host ('/host_etc/lsb-release').
	// --insert-on-install: Inserts the module into the kernel after installation.
	// --module-arg lnet.accept_port=${LNET_PORT}: This is crucial for setting the LNET port.
	//                                     Lustre uses LNET for network communication, and this
	//                                     parameter configures the port LNET will use. This is
	//                                     essential for proper communication between Lustre clients
	//                                     and servers. The default value is 988.
	// -w Set the number of parallel downloads (`0` downloads all files in parallel).
	args := []string{"install", "lustre-client-drivers"}
	if len(customDkmsArgs) > 0 {
		args = append(args, customDkmsArgs...)
	} else {
		defaultArgs := []string{
			"--gcs-bucket=cos-default",
			"--latest",
			"-w", "0",
			"--kernelmodulestree=/host_modules",
			"--module-arg=" + moduleArg,
			"--module-arg=lnet.accept_port=" + strconv.Itoa(lnetPort),
			"--lsb-release-path=/host_etc/lsb-release",
			"--insert-on-install",
			"--logtostderr",
		}
		args = append(args, defaultArgs...)
	}
	cmd := exec.CommandContext(cmdCtx, "/usr/bin/cos-dkms", args...)
	// TODO(samhalim): Add latency/success rate metrics for kmod cos-dkms install.

	klog.Infof("Installing Lustre kernel modules by executing command: %s", cmd.String())
	output, err := cmd.CombinedOutput()
	if err != nil {
		if cmdCtx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("command timed out after %s: %w\noutput:\n%s", cmdTimeout, err, string(output))
		}

		return fmt.Errorf("command execution failed: %w\noutput:\n%s", err, string(output))
	}

	klog.Infof("Cos-dkms output:\n%s\n", string(output))

	return nil
}

func DisableLoadPin(ctx context.Context) error {
	// initial validation.
	cmdline, err := os.ReadFile("/proc/cmdline")
	if err != nil {
		return fmt.Errorf("failed to read /proc/cmdline: %w", err)
	}
	if strings.Contains(string(cmdline), "loadpin") {
		klog.V(4).Info("LoadPin is already disabled. Moving to Kmod installation.")
		return nil
	}
	klog.V(4).Info("LoadPin is not disabled. Sleeping 60s until the node is ready...")
	time.Sleep(60 * time.Second)
	klog.Info("Disabling LoadPin now.")

	// mount disk
	mountPoint := "/mnt/disks"
	if err := os.MkdirAll(mountPoint, 0755); err != nil {
		return fmt.Errorf("failed to mkdir %s: %w", mountPoint, err)
	}
	cmdCtx, cancel := context.WithTimeout(ctx, cmdTimeout)
	defer cancel()

	mountCmd := exec.CommandContext(cmdCtx, "mount", efiPartition, mountPoint)
	output, err := mountCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to mount EFI partition: %w, output: %s", err, string(output))
	}

	// Parse through grub cfg file and replace/add disable loadpin line.
	grubPath := mountPoint + grubCfgSuffix
	grubBytes, err := os.ReadFile(grubPath)
	if err != nil {
		return fmt.Errorf("failed to read grub.cfg: %w", err)
	}
	grubContent := string(grubBytes)
	klog.V(4).Infof("samhalim this is before grub change: %v", grubContent)
	if !strings.Contains(grubContent, "loadpin.enforce=0") {
		disableLoadpinCfg := strings.ReplaceAll(grubContent, "module.sig_enforce=0", "module.sig_enforce=0 loadpin.enforce=0")

		if err := os.WriteFile(grubPath, []byte(disableLoadpinCfg), 0644); err != nil {
			return fmt.Errorf("failed to write disable loadpin content to %v: %w", grubPath, err)
		}
		klog.V(4).Info("Successfully disabled LoadPin.")
	}

	// unmount disk
	unmountCmd := exec.CommandContext(cmdCtx, "unmount", mountPoint)
	output, err = unmountCmd.CombinedOutput()
	if err != nil {
		klog.Warningf("failed explicit unmount on %v. output: %v, error: %v", mountPoint, string(output), err)
	}

	// bash: echo 1 > /proc/sys/kernel/sysrq
	if err := os.WriteFile("/proc/sys/kernel/sysrq", []byte("1\n"), 0644); err != nil {
		return fmt.Errorf("failed to enable sysrq: %w", err)
	}

	// bash: echo b > /proc/sysrq-trigger
	// This should trigger node reboot
	if err := os.WriteFile("/proc/sysrq-trigger", []byte("b\n"), 0200); err != nil {
		return fmt.Errorf("failed to trigger reboot: %w", err)
	}

	// Sleep for 10 seconds to account for any delay on node reboot.
	time.Sleep(10 * time.Second)
	return nil
}
