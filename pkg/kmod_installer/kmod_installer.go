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
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/lustre-csi-driver/pkg/network"
	v1 "k8s.io/api/core/v1"
	utilversion "k8s.io/apimachinery/pkg/util/version"
	"k8s.io/klog/v2"
)

const (
	legacyLNetPort     = 6988
	defaultLNetPort    = 988
	cmdTimeout         = 15 * time.Minute
	OSNodeLabel        = "cloud.google.com/gke-os-distribution"
	PreviewClientLabel = "lustre.csi.storage.gke.io/preview-client-version"

	ubuntuProdAptSuite    = "lustre-client-ubuntu-noble"
	ubuntuPreviewAptSuite = "lustre-client-ubuntu-noble-preview"
)

// ubuntuPinnedClientVersion keeps GKE Ubuntu nodes older than beforeGKEVersion on the
// lustreVersion minor series when they have not opted in to a preview client version.
type ubuntuPinnedClientVersion struct {
	beforeGKEVersion *utilversion.Version
	lustreVersion    string
}

var (
	lnetAcceptPortFile       = "/sys/module/lnet/parameters/accept_port"
	lnetNetworkParameterFile = "/sys/module/lnet/parameters/networks"
	lustreModuleDir          = "/sys/module/lustre"

	previewVersionRegex = regexp.MustCompile(`^[0-9]+\.[0-9]+$`)

	// ubuntuPinnedClientVersions, ordered oldest boundary first, pins the default Lustre
	// series for GKE Ubuntu nodes that predate a GKE version. Nodes at or above the last
	// boundary install the newest package in the production repository, the same way
	// cos-dkms --latest does on COS. The pin exists so that promoting a newer series into
	// the production repository does not change node pools that were created when an older
	// series was the default; such a node pool only moves to the newer series when it is
	// upgraded across the boundary. Add a boundary when a newer series is promoted and the
	// fleet that predates it should stay put.
	ubuntuPinnedClientVersions = []ubuntuPinnedClientVersion{
		{beforeGKEVersion: utilversion.MustParseGeneric("1.38.0"), lustreVersion: "2.14"},
	}
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
	return isLustreKmodInstalled(enableLegacyLustrePort, lnetAcceptPortFile, lustreModuleDir)
}

func isLustreKmodInstalled(enableLegacyLustrePort bool, acceptPortFile, lustreModuleDir string) (bool, error) {
	// Check if the kernel module is loaded by checking the existence of the parameter file
	file, err := os.ReadFile(acceptPortFile)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}

		return false, fmt.Errorf("failed to read lnet accept port file %s: %w", acceptPortFile, err)
	}

	if _, err := os.Stat(lustreModuleDir); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("failed to check lustre module sysfs: %w", err)
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
// If the file is missing but modules are installed, it returns a default network based on defaultNic.
func GetLnetNetwork(expectedNics, defaultNic string) ([]string, error) {
	return getLnetNetwork(expectedNics, lnetNetworkParameterFile, defaultNic)
}

func getLnetNetwork(expectedNics, networkFile, defaultNic string) ([]string, error) {
	networkStr, err := readLnetConfig(networkFile, defaultNic)
	if err != nil {
		if os.IsNotExist(err) {
			// If the LNET parameter file is missing but modules are supposedly installed,
			// falling back to a default network using defaultNic.
			networkStr = fmt.Sprintf("tcp0(%s)", defaultNic)
		} else {
			return nil, err
		}
	}

	if expectedNics != "" && networkStr != expectedNics {
		klog.Warningf("LNET network parameter mismatched, Got: %s, Expected: %s. Please upgrade your node pool to apply the correct settings.", networkStr, expectedNics)
	}

	return parseLnetNetwork(networkStr), nil
}

func readLnetConfig(path, defaultNic string) (string, error) {
	file, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	currNetworkNics := strings.TrimSpace(string(file))
	if currNetworkNics == "" {
		klog.V(4).Infof("LNET network parameter file %v is empty.", path)
		// An empty lnetNetworkParameterFile implies either the kernel modules are not installed yet,
		// or they are installed, but just without any parameters.
		// If that file is empty, but kernel modules are already installed, defaultNic should be used.
		return fmt.Sprintf("tcp0(%s)", defaultNic), nil
	}

	return currNetworkNics, nil
}

// InstallLustreKmodOnCos installs the Lustre kernel modules on the node using cos-dkms on COS nodes.
// It proceeds with the installation using the provided NICs.
func InstallLustreKmodOnCos(ctx context.Context, enableLegacyPort bool, customModuleArgs []string, nics []string, disableMultiNIC bool, primaryNic, previewClientVersion string) error {
	lnetPort := lnetPort(enableLegacyPort)
	expectedNetwork := BuildLnetNetworkString(nics, primaryNic, disableMultiNIC)

	cmdCtx, cancel := context.WithTimeout(ctx, cmdTimeout)
	defer cancel()

	args := buildCosDKMSArgs(previewClientVersion, lnetPort, expectedNetwork, customModuleArgs)
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

// InstallLustreKmodOnUbuntu installs the Lustre kernel modules on Ubuntu nodes.
// previewClientVersion is the opt-in label value, if any; defaultClientVersion is the
// minor series to install when the node has not opted in (see DefaultUbuntuClientVersion).
func InstallLustreKmodOnUbuntu(ctx context.Context, enableLegacyPort bool, customModuleArgs []string, nics []string, disableMultiNIC bool, primaryNic, previewClientVersion, defaultClientVersion string) error {
	lnetPort := lnetPort(enableLegacyPort)
	expectedNetwork := BuildLnetNetworkString(nics, primaryNic, disableMultiNIC)

	cmdCtx, cancel := context.WithTimeout(ctx, cmdTimeout)
	defer cancel()

	uname := exec.CommandContext(cmdCtx, "uname", "-r")
	unameOut, err := uname.Output()
	if err != nil {
		return fmt.Errorf("failed to determine kernel version via uname -r: %w", err)
	}
	kernelVersion := strings.TrimSpace(string(unameOut))
	klog.Infof("Detected target Ubuntu kernel version: %s", kernelVersion)

	updateCmd := exec.CommandContext(cmdCtx, "apt-get", "update")
	if out, err := updateCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to update apt repositories: %w\noutput:\n%s", err, string(out))
	}

	// download lustre client packages
	downloadArgs := buildUbuntuAptDownloadArgs(kernelVersion, previewClientVersion, defaultClientVersion)
	tmpDir, err := os.MkdirTemp("", "lustre-kmod-*")
	if err != nil {
		return fmt.Errorf("failed to create temp directory: %w", err)
	}
	defer func() {
		if err := os.RemoveAll(tmpDir); err != nil {
			klog.Warningf("Failed to remove temp directory %s: %v", tmpDir, err)
		}
	}()

	klog.Infof("Downloading lustre client modules with args: %v into: %s", downloadArgs, tmpDir)
	downloadCmd := exec.CommandContext(cmdCtx, "apt-get", downloadArgs...)
	downloadCmd.Dir = tmpDir
	if out, err := downloadCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to download lustre client modules: %w\noutput:\n%s", err, string(out))
	}

	// confirm .deb file is present
	files, err := os.ReadDir(tmpDir)
	if err != nil {
		return fmt.Errorf("failed to read download directory: %w", err)
	}
	var debFile string
	for _, f := range files {
		if !f.IsDir() && strings.HasSuffix(f.Name(), ".deb") {
			debFile = f.Name()
			break
		}
	}
	if debFile == "" {
		return fmt.Errorf("no .deb file found in %v", tmpDir)
	}
	klog.Infof("Found lustre client modules deb file: %v", debFile)

	// extract deb file package locally
	extractDir, err := os.MkdirTemp("", "extracted-kmod-*")
	if err != nil {
		return fmt.Errorf("failed to create temp extract directory: %w", err)
	}
	defer func() {
		if err := os.RemoveAll(extractDir); err != nil {
			klog.Warningf("Failed to remove temp extract directory %s: %v", extractDir, err)
		}
	}()

	extractCmd := exec.CommandContext(cmdCtx, "dpkg", "-x", tmpDir+"/"+debFile, extractDir)
	if out, err := extractCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("dpkg -x failed: %w\noutput:\n%s", err, string(out))
	}

	klog.Infof("Running depmod for extracted modules in basedir %s", extractDir)
	depmodCmd := exec.CommandContext(cmdCtx, "depmod", "-b", extractDir, kernelVersion)
	if out, err := depmodCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("depmod failed: %w\noutput:\n%s", err, string(out))
	}
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

	// Load the underlying TCP network driver (ksocklnd), core lustre module,
	// and required client subsystem modules (mgc: management, lmv/mdc: metadata, lov/osc: object storage)
	// via modprobe to ensure they are resident in kernel memory before the ephemeral module files are removed.
	clientModules := []string{"ksocklnd", "lustre", "mgc", "lmv", "lov", "mdc", "osc"}
	for _, mod := range clientModules {
		modArgs := []string{"-d", extractDir, mod}
		prefix := mod + "."
		for _, arg := range customModuleArgs {
			if strings.HasPrefix(arg, prefix) {
				modArgs = append(modArgs, strings.TrimPrefix(arg, prefix))
			}
		}
		klog.Infof("Loading %s module via modprobe with args: %v", mod, modArgs)
		cmd := exec.CommandContext(cmdCtx, "modprobe", modArgs...)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("modprobe %s failed: %w\noutput:\n%s", mod, err, string(out))
		}
	}

	klog.Infof("Ubuntu Lustre installation completed successfully.")

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

// GetNode fetches the Node object once so callers can read several fields from it
// (labels, kubelet version) without repeating the API call.
func GetNode(ctx context.Context, nodeID string, nc network.NodeClient) (*v1.Node, error) {
	node, err := nc.GetNodeWithRetry(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	if node == nil {
		return nil, fmt.Errorf("node object %s is empty", nodeID)
	}
	return node, nil
}

// NodeLabelValue returns the trimmed value of labelKey on node, or "" if it is not set.
func NodeLabelValue(node *v1.Node, labelKey string) string {
	return strings.TrimSpace(node.GetLabels()[labelKey])
}

// GetNodeLabelValue returns the value of the specified label from the node.
func GetNodeLabelValue(ctx context.Context, nodeID string, labelKey string, nc network.NodeClient) (string, error) {
	node, err := GetNode(ctx, nodeID, nc)
	if err != nil {
		return "", err
	}
	return NodeLabelValue(node, labelKey), nil
}

// BuildLnetNetworkString constructs the LNet network configuration string.
func BuildLnetNetworkString(nics []string, primaryNic string, disableMultiNIC bool) string {
	if disableMultiNIC {
		return fmt.Sprintf("tcp0(%s)", primaryNic)
	}
	orderedNics := []string{primaryNic}
	for _, nic := range nics {
		if nic != primaryNic {
			orderedNics = append(orderedNics, nic)
		}
	}
	return fmt.Sprintf("tcp0(%s)", strings.Join(orderedNics, ","))
}

// isPreviewVersion returns true if the version string represents a preview minor release stream (e.g. "2.16"),
// for which the latest or max compatible build should be installed.
func isPreviewVersion(version string) bool {
	return previewVersionRegex.MatchString(strings.TrimSpace(version))
}

// buildCosDKMSArgs constructs the cos-dkms install command arguments.
func buildCosDKMSArgs(previewClientVersion string, lnetPort int, expectedNetwork string, customModuleArgs []string) []string {
	lustreClientDriverPackage := "lustre-client-drivers"
	versionArgs := []string{
		"--latest",
	}
	if previewClientVersion != "" {
		lustreClientDriverPackage += "-pre"
		if isPreviewVersion(previewClientVersion) {
			versionArgs = append(versionArgs, []string{
				"--min-version=" + previewClientVersion + ".0",
				"--max-version=" + previewClientVersion + ".9999",
			}...)
		} else {
			// If it doesn't match preview regex, we treat it as an exact driver build version.
			// If it's not a valid version string, cos-dkms will reject it.
			versionArgs = []string{
				"--package-version=" + previewClientVersion,
			}
		}
		klog.Infof("Preview client version requested: %q. Using driver package %q with args: %v",
			previewClientVersion, lustreClientDriverPackage, versionArgs)
	}

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
	args := []string{"install", lustreClientDriverPackage}
	args = append(args, versionArgs...)
	args = append(args,
		"--gcs-bucket=cos-default",
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

	return args
}

// buildUbuntuAptDownloadArgs constructs the apt-get download arguments for Lustre client modules on Ubuntu.
//
// pinnedClientVersion is the minor series an older fleet stays on (see
// DefaultUbuntuClientVersion); when it is empty, the newest package in the production
// repository is installed. apt resolves "<package>=<prefix>*" to the newest available
// version whose version string starts with <prefix>, so "=2.14.*" selects the latest 2.14
// patch release and never crosses into another minor series. Rolling back a bad patch
// build is handled on the repository side (fix forward or remove the build), after which
// the previous build becomes the newest match again.
func buildUbuntuAptDownloadArgs(kernelVersion, previewClientVersion, pinnedClientVersion string) []string {
	pkgTarget := fmt.Sprintf("lustre-client-modules-%s", kernelVersion)
	if previewClientVersion == "" {
		if pinnedClientVersion == "" {
			return []string{"download", fmt.Sprintf("%s/%s", pkgTarget, ubuntuProdAptSuite)}
		}
		return []string{"download", "-t", ubuntuProdAptSuite, fmt.Sprintf("%s=%s.*", pkgTarget, pinnedClientVersion)}
	}

	if isPreviewVersion(previewClientVersion) {
		// Trailing "." keeps "2.1" from matching "2.16".
		pkgTarget = fmt.Sprintf("%s=%s.*", pkgTarget, previewClientVersion)
	} else {
		pkgTarget = fmt.Sprintf("%s=%s", pkgTarget, previewClientVersion)
	}

	return []string{"download", "-t", ubuntuPreviewAptSuite, pkgTarget}
}

// DefaultUbuntuClientVersion returns the Lustre minor series an Ubuntu node that has not
// opted in to a preview client version is pinned to, or an empty string when the node should
// install the newest package in the production repository. The choice is keyed on the node's
// GKE version, which is reported as the kubelet version (for example "v1.37.2-gke.1000").
func DefaultUbuntuClientVersion(node *v1.Node) string {
	kubeletVersion := node.Status.NodeInfo.KubeletVersion
	series := defaultUbuntuClientVersionFor(kubeletVersion, ubuntuPinnedClientVersions)
	if series == "" {
		klog.Infof("Node %s runs GKE version %q; installing the newest Lustre client in the production repository", node.GetName(), kubeletVersion)
	} else {
		klog.Infof("Node %s runs GKE version %q; default Lustre client is pinned to the %s series", node.GetName(), kubeletVersion, series)
	}
	return series
}

func defaultUbuntuClientVersionFor(kubeletVersion string, pins []ubuntuPinnedClientVersion) string {
	if len(pins) == 0 {
		return ""
	}
	v, err := utilversion.ParseGeneric(kubeletVersion)
	if err != nil {
		// An unknown node version is treated like the oldest fleet so it is never moved
		// to a newer series by accident.
		klog.Warningf("Could not parse node version %q, pinning Lustre client to the %s series: %v", kubeletVersion, pins[0].lustreVersion, err)
		return pins[0].lustreVersion
	}
	for _, p := range pins {
		if v.LessThan(p.beforeGKEVersion) {
			return p.lustreVersion
		}
	}
	return ""
}
