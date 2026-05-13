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

	"github.com/GoogleCloudPlatform/lustre-csi-driver/pkg/network"
	"k8s.io/klog/v2"
)

const (
	legacyLNetPort     = 6988
	defaultLNetPort    = 988
	cmdTimeout         = 15 * time.Minute
	DefaultLnetNetwork = "tcp0(eth0)"
	osNodeLabel        = "cloud.google.com/gke-os-distribution"
)

var (
	lnetAcceptPortFile       = "/sys/module/lnet/parameters/accept_port"
	lnetNetworkParameterFile = "/sys/module/lnet/parameters/networks"
	lustreModuleDir          = "/sys/module/lustre"
)

func lnetPort(enableLegacyLustrePort bool) int {
	if enableLegacyLustrePort {
		return legacyLNetPort
	}

	return defaultLNetPort
}

// IsLustreKmodInstalled checks if the Lustre kernel modules are installed.
// It checks for the existence of the lnet accept port file and the lustre module directory.
// If the files exist, it validates that the configured port matches the expected port.
// Returns (true, nil) if installed and valid.
// Returns (true, error) if installed but configuration mismatch (e.g. wrong port).
// Returns (false, nil) if not installed.
// Returns (false, error) if unexpected filesystem error checking the files.
func IsLustreKmodInstalled(enableLegacyLustrePort bool) (bool, error) {
	return isLustreKmodInstalled(enableLegacyLustrePort, lnetAcceptPortFile, lustreModuleDir)
}

func isLustreKmodInstalled(enableLegacyLustrePort bool, acceptPortFile, lustreDir string) (bool, error) {
	// Check if the kernel module is loaded by checking the existence of the parameter file
	file, err := os.ReadFile(acceptPortFile)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}

		return false, fmt.Errorf("failed to read lnet accept port file %s: %w", acceptPortFile, err)
	}

	// Also verify that the lustre module itself is loaded, not just lnet
	if _, err := os.Stat(lustreDir); err != nil {
		if os.IsNotExist(err) {
			klog.Warningf("LNet module is loaded but lustre module directory %s is missing. Considering kmod installation incomplete.", lustreDir)
			return false, nil
		}
		return false, fmt.Errorf("failed to check lustre module directory %s: %w", lustreDir, err)
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

// InstallLustreKmodOnCos installs the Lustre kernel modules on the node using cos-dkms on COS nodes.
// It proceeds with the installation using the provided NICs.
func InstallLustreKmodOnCos(ctx context.Context, enableLegacyPort bool, customModuleArgs []string, nics []string, disableMultiNIC bool) error {
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

// HostOSFromNodeLabel returns the OS identifier from node label.
func HostOSFromNodeLabel(ctx context.Context, nodeID string, nc network.NodeClient) (string, error) {
	node, err := nc.GetNodeWithRetry(ctx, nodeID)
	if err != nil {
		return "", err
	}
	val, found := node.GetLabels()[osNodeLabel]
	if !found {
		klog.Warningf("Label %v could not be found on the node", osNodeLabel)
		return "unknown", nil
	}
	return val, nil
}

// InstallLustreKmodOnUbuntu installs the Lustre kernel modules on the node via the Driver Container approach.
// It fetches the dynamic kernel module package matching the node's kernel version, extracts it locally,
// configures LNet parameters, runs depmod, and loads the module using modprobe.
func InstallLustreKmodOnUbuntu(ctx context.Context, enableLegacyPort bool, customModuleArgs []string, nics []string, disableMultiNIC bool) error {
	lnetPort := lnetPort(enableLegacyPort)
	primaryNIC := "ens4"
	if len(nics) > 0 {
		primaryNIC = nics[0]
	}
	expectedNetwork := fmt.Sprintf("tcp0(%s)", primaryNIC)
	if !disableMultiNIC && len(nics) > 0 {
		expectedNetwork = fmt.Sprintf("tcp0(%s)", strings.Join(nics, ","))
	}

	cmdCtx, cancel := context.WithTimeout(ctx, cmdTimeout)
	defer cancel()

	// 1. Determine running kernel version
	unameCmd := exec.CommandContext(cmdCtx, "uname", "-r")
	unameOut, err := unameCmd.Output()
	if err != nil {
		return fmt.Errorf("failed to determine kernel version via uname -r: %w", err)
	}
	kernelVersion := strings.TrimSpace(string(unameOut))
	klog.Infof("Detected target Ubuntu kernel version: %s", kernelVersion)

	// 2. Run apt-get update inside the container
	klog.Info("Updating apt repositories to locate dynamic kernel modules")
	updateCmd := exec.CommandContext(cmdCtx, "apt-get", "update")
	if out, err := updateCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("apt-get update failed: %w\noutput:\n%s", err, string(out))
	}

	// 3. Download the package for the current kernel version
	pkgName := fmt.Sprintf("lustre-client-modules-%s", kernelVersion)
	tmpDir, err := os.MkdirTemp("", "lustre-kmod-*")
	if err != nil {
		return fmt.Errorf("failed to create temp directory: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	klog.Infof("Downloading package %s via apt-get download", pkgName)
	downloadCmd := exec.CommandContext(cmdCtx, "apt-get", "download", pkgName)
	downloadCmd.Dir = tmpDir
	if out, err := downloadCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to download package %s: %w\noutput:\n%s", pkgName, err, string(out))
	}

	// Find the downloaded .deb file
	files, err := os.ReadDir(tmpDir)
	if err != nil {
		return fmt.Errorf("failed to read download directory: %w", err)
	}
	var debFile string
	for _, f := range files {
		if strings.HasSuffix(f.Name(), ".deb") {
			debFile = f.Name()
			break
		}
	}
	if debFile == "" {
		return fmt.Errorf("no .deb file found after successful apt-get download in %s", tmpDir)
	}

	// 4. Extract the package locally
	extractDir := "/tmp/extracted-kmod"
	_ = os.RemoveAll(extractDir)
	if err := os.MkdirAll(extractDir, 0755); err != nil {
		return fmt.Errorf("failed to create extract directory: %w", err)
	}
	defer os.RemoveAll(extractDir)

	klog.Infof("Extracting package %s to %s", debFile, extractDir)
	extractCmd := exec.CommandContext(cmdCtx, "dpkg", "-x", tmpDir+"/"+debFile, extractDir)
	if out, err := extractCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("dpkg -x failed: %w\noutput:\n%s", err, string(out))
	}

	// 5. Write module options to /etc/modprobe.d/lustre.conf inside the container
	/*
	if err := os.MkdirAll("/etc/modprobe.d", 0755); err != nil {
		return fmt.Errorf("failed to create /etc/modprobe.d directory: %w", err)
	}

	optionsMap := map[string][]string{
		"lnet": {
			fmt.Sprintf("accept_port=%d", lnetPort),
			fmt.Sprintf("networks=\"%s\"", expectedNetwork),
		},
	}

	for _, arg := range customModuleArgs {
		parts := strings.SplitN(arg, ".", 2)
		if len(parts) == 2 {
			modName := parts[0]
			param := parts[1]
			optionsMap[modName] = append(optionsMap[modName], param)
		} else {
			klog.Warningf("Invalid custom module arg format (expected module.param=value): %s", arg)
		}
	}

	var confLines []string
	for modName, opts := range optionsMap {
		confLines = append(confLines, fmt.Sprintf("options %s %s", modName, strings.Join(opts, " ")))
	}
	confContent := strings.Join(confLines, "\n") + "\n"
	klog.Infof("Writing module configuration to /etc/modprobe.d/lustre.conf:\n%s", confContent)
	if err := os.WriteFile("/etc/modprobe.d/lustre.conf", []byte(confContent), 0644); err != nil {
		return fmt.Errorf("failed to write /etc/modprobe.d/lustre.conf: %w", err)
	}
	*/

	// 6. Run depmod on the extracted directory
	klog.Infof("Running depmod for extracted modules in basedir %s", extractDir)
	depmodCmd := exec.CommandContext(cmdCtx, "depmod", "-b", extractDir, kernelVersion)
	if out, err := depmodCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("depmod failed: %w\noutput:\n%s", err, string(out))
	}

	// 6.5 Unload any stale modules left over from previous failed attempts using rmmod directly to bypass path mapping issues
	klog.Info("Unloading any stale lustre/lnet modules using rmmod")
	if out, err := exec.CommandContext(cmdCtx, "rmmod", "lustre").CombinedOutput(); err != nil {
		klog.V(5).Infof("rmmod lustre output (harmless if not loaded): %s", string(out))
	}
	if out, err := exec.CommandContext(cmdCtx, "rmmod", "ksocklnd").CombinedOutput(); err != nil {
		klog.V(5).Infof("rmmod ksocklnd output (harmless if not loaded): %s", string(out))
	}
	if out, err := exec.CommandContext(cmdCtx, "rmmod", "lnet").CombinedOutput(); err != nil {
		klog.V(5).Infof("rmmod lnet output (harmless if not loaded): %s", string(out))
	}
	if out, err := exec.CommandContext(cmdCtx, "rmmod", "libcfs").CombinedOutput(); err != nil {
		klog.V(5).Infof("rmmod libcfs output (harmless if not loaded): %s", string(out))
	}

	// 7. Load lnet explicitly with discovered primary NIC binding arguments on the command line
	lnetArgs := []string{"-d", extractDir, "lnet", fmt.Sprintf("accept_port=%d", lnetPort), fmt.Sprintf("networks=%s", expectedNetwork)}
	for _, arg := range customModuleArgs {
		if strings.HasPrefix(arg, "lnet.") {
			lnetArgs = append(lnetArgs, strings.TrimPrefix(arg, "lnet."))
		}
	}
	klog.Infof("Loading lnet module explicitly via modprobe with args: %v", lnetArgs)
	lnetCmd := exec.CommandContext(cmdCtx, "modprobe", lnetArgs...)
	if out, err := lnetCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("modprobe lnet failed: %w\noutput:\n%s", err, string(out))
	}

	// 8. Explicitly load ksocklnd (LNet TCP Network Driver) to prevent internal host request_module failures
	klog.Info("Loading ksocklnd module explicitly via modprobe")
	ksockCmd := exec.CommandContext(cmdCtx, "modprobe", "-d", extractDir, "ksocklnd")
	if out, err := ksockCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("modprobe ksocklnd failed: %w\noutput:\n%s", err, string(out))
	}

	// 9. Load the lustre module using modprobe
	klog.Info("Loading lustre module via modprobe")
	modprobeCmd := exec.CommandContext(cmdCtx, "modprobe", "-d", extractDir, "lustre")
	if out, err := modprobeCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("modprobe lustre failed: %w\noutput:\n%s", err, string(out))
	}

	klog.Info("Successfully installed Lustre kernel modules for Ubuntu")
	return nil
}
