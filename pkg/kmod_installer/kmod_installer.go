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
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog/v2"
)

const (
	lnetAcceptPortFile       = "/sys/module/lnet/parameters/accept_port"
	lnetNetworkParameterFile = "/sys/module/lnet/parameters/networks"
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
		klog.Warningf("LNET port mismatched, Got: %s, Expected: %s", currPort, expectedPortStr)

		return status.Error(codes.FailedPrecondition, "node already has lustre kernel modules installed with an outdated lnet.accept_port configuration. please upgrade your node pool to apply the correct settings")
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
	if currNetworkNics == "" {
		klog.V(4).Infof("LNET network parameter file %v is empty. Skipping check", lnetNetworkParameterFile)

		return nil
	}
	if currNetworkNics != expectedNics {
		klog.Warningf("LNET network parameter mismatched, Got: %s, Expected: %s", currNetworkNics, expectedNics)

		return status.Error(codes.FailedPrecondition, "node already has lustre kernel modules installed with an outdated lnet.networks configuration. please upgrade your node pool to apply the correct settings")
	}

	klog.V(4).Info("Network NICs matches configuration. Check passed.")

	return nil
}

// InstallLustreKmod installs kmod (cos-dkms) on the node.
func InstallLustreKmod(ctx context.Context, enableLegacyPort bool, customModuleArgs []string, nics []string, disableMultiNIC bool) error {
	lnetPort := lnetPort(enableLegacyPort)
	if err := checkLnetPort(lnetPort); err != nil {
		return err
	}

	expectedNetwork := fmt.Sprintf("tcp0(%s)", nics[0])
	if !disableMultiNIC {
		expectedNetwork = fmt.Sprintf("tcp0(%s)", strings.Join(nics, ","))
	}

	if err := checkLnetNetwork(expectedNetwork); err != nil {
		return err
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
	args = append(args,
		"--gcs-bucket=cos-default",
		"--latest",
		"-w", "0",
		"--kernelmodulestree=/host_modules",
		"--lsb-release-path=/host_etc/lsb-release",
		"--insert-on-install",
		"--logtostderr",
		"--module-arg=lnet.accept_port="+strconv.Itoa(lnetPort),
	)

	if !disableMultiNIC {
		args = append(args, fmt.Sprintf(`--module-arg=lnet.networks="%s"`, expectedNetwork))
	}

	for _, arg := range customModuleArgs {
		args = append(args, "--module-arg="+arg)
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
