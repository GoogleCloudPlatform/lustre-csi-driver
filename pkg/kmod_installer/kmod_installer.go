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

	"k8s.io/klog/v2"
)

const (
	legacyLNetPort     = 6988
	defaultLNetPort    = 988
	cmdTimeout         = 15 * time.Minute
	DefaultLnetNetwork = "tcp0(eth0)"
)

var (
	lnetAcceptPortFile       = "/sys/module/lnet/parameters/accept_port"
	lnetNetworkParameterFile = "/sys/module/lnet/parameters/networks"
)

func lnetPort(enableLegacyLustrePort bool) int {
	if enableLegacyLustrePort {
		return legacyLNetPort
	}

	return defaultLNetPort
}

// IsLustreKmodInstalled checks if the Lustre kernel modules are installed.
// It checks for the existence of the lnet accept port file.
// If the file exists, it validates that the configured port matches the expected port.
// Returns (true, nil) if installed and valid.
// Returns (true, error) if installed but configuration mismatch (e.g. wrong port).
// Returns (false, nil) if not installed.
// Returns (false, error) if unexpected filesystem error checking the file.
func IsLustreKmodInstalled(enableLegacyLustrePort bool) (bool, error) {
	return isLustreKmodInstalled(enableLegacyLustrePort, lnetAcceptPortFile)
}

func isLustreKmodInstalled(enableLegacyLustrePort bool, acceptPortFile string) (bool, error) {
	// Check if the kernel module is loaded by checking the existence of the parameter file
	file, err := os.ReadFile(acceptPortFile)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}

		return false, fmt.Errorf("failed to read lnet accept port file %s: %w", acceptPortFile, err)
	}

	currPort := strings.TrimSpace(string(file))
	expectedPort := lnetPort(enableLegacyLustrePort)
	expectedPortStr := strconv.Itoa(expectedPort)
	if currPort != expectedPortStr {
		klog.Errorf("LNET port mismatched, Got: %s, Expected: %s", currPort, expectedPortStr)

		return true, fmt.Errorf("node already has lustre kernel modules installed with an outdated lnet.accept_port configuration (Got: %s, Expected: %s). please upgrade your node pool to apply the correct settings", currPort, expectedPortStr)
	}

	return true, nil
}

// GetLnetNetwork retrieves the currently configured LNet network interfaces.
// It reads the "networks" parameter from the LNet module parameters.
// If expectedNics is provided, it validates the current config matches expectation and warns on mismatch.
// If the file is missing but modules are installed, it returns a default "eth0".
func GetLnetNetwork(expectedNics string) ([]string, error) {
	return getLnetNetwork(expectedNics, lnetNetworkParameterFile)
}

func getLnetNetwork(expectedNics, networkFile string) ([]string, error) {
	networkStr, err := readLnetConfig(networkFile)
	if err != nil {
		if os.IsNotExist(err) {
			// If the LNET parameter file is missing but modules are supposedly installed,
			// falling back to a (tcp0(eth0)) as the default.
			networkStr = DefaultLnetNetwork
		} else {
			return nil, err
		}
	}

	if expectedNics != "" && networkStr != expectedNics {
		klog.Warningf("LNET network parameter mismatched, Got: %s, Expected: %s. Please upgrade your node pool to apply the correct settings.", networkStr, expectedNics)
	}

	return parseLnetNetwork(networkStr), nil
}

func readLnetConfig(path string) (string, error) {
	file, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	currNetworkNics := strings.TrimSpace(string(file))
	if currNetworkNics == "" {
		klog.V(4).Infof("LNET network parameter file %v is empty.", path)
		// An empty lnetNetworkParameterFile implies either the kernel modules are not installed yet,
		// or they are installed, but just without any parameters.
		// If that file is empty, but kernel modules are already installed, eth0 should be used.
		return DefaultLnetNetwork, nil
	}

	return currNetworkNics, nil
}

// InstallLustreKmod installs the Lustre kernel modules on the node using cos-dkms.
// It proceeds with the installation using the provided NICs.
func InstallLustreKmod(ctx context.Context, enableLegacyPort bool, customModuleArgs []string, nics []string, disableMultiNIC bool) error {
	lnetPort := lnetPort(enableLegacyPort)
	expectedNetwork := DefaultLnetNetwork
	if !disableMultiNIC {
		expectedNetwork = fmt.Sprintf("tcp0(%s)", strings.Join(nics, ","))
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

	args = append(args, fmt.Sprintf(`--module-arg=lnet.networks="%s"`, expectedNetwork))

	for _, arg := range customModuleArgs {
		args = append(args, "--module-arg="+arg)
	}
	cmd := exec.CommandContext(cmdCtx, "/usr/bin/cos-dkms", args...)
	// TODO(samhalim): Add latency/success rate metrics for kmod cos-dkms install.

	klog.Infof("Installing Lustre kernel modules by executing command: %s", cmd.String())
	output, err := cmd.CombinedOutput()
	if err != nil {
		outputStr := string(output)
		lowerOutput := strings.ToLower(outputStr)
		if strings.Contains(lowerOutput, "operation not permitted") && strings.Contains(lowerOutput, "insmod") {
			msg := "lustre kernel module installation failed due to permission issue. Please follow https://docs.cloud.google.com/managed-lustre/docs/lustre-csi-driver-new-volume#node-upgrade-required to upgrade node pool to allow GKE managed Lustre CSI driver installation to complete, or disabling loadpin if you are installing the Lustre CSI driver from open source"

			return fmt.Errorf("command execution failed: %w. %s", err, msg)
		}

		if cmdCtx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("command timed out after %s: %w\noutput:\n%s", cmdTimeout, err, outputStr)
		}

		return fmt.Errorf("command execution failed: %w\noutput:\n%s", err, outputStr)
	}

	klog.Infof("COS-DKMS output:\n%s\n", string(output))

	return nil
}

func parseLnetNetwork(networkStr string) []string {
	// networkStr format: tcp0(eth0,eth1)
	networkStr = strings.TrimPrefix(networkStr, "tcp0(")
	networkStr = strings.TrimSuffix(networkStr, ")")
	if networkStr == "" {
		return []string{}
	}

	return strings.Split(networkStr, ",")
}
