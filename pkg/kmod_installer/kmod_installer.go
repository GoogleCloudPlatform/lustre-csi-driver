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
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/lustre-csi-driver/pkg/network"
	"github.com/GoogleCloudPlatform/lustre-csi-driver/pkg/util"
	"k8s.io/klog/v2"
)

const (
	legacyLNetPort  = 6988
	defaultLNetPort = 988

	// lnetNumaRange is set to a high value (1000000) to treat all local interfaces
	// equally and enable multi-rail load balancing across NUMA nodes when Multi-NIC is enabled.
	lnetNumaRange = 1000000

	// minCPUsPerPartition ensures each Lustre CPT partition has enough CPUs
	// so ko2iblnd scheduler threads do not starve user workloads.
	minCPUsPerPartition = 8

	// maxPartitionsPerRDMANIC caps partitions per RDMA NIC to avoid ring/QP lock contention.
	maxPartitionsPerRDMANIC = 4

	cmdTimeout         = 15 * time.Minute
	OSNodeLabel        = "cloud.google.com/gke-os-distribution"
	PreviewClientLabel = "lustre.csi.storage.gke.io/preview-client-version"
)

var (
	lnetAcceptPortFile       = "/sys/module/lnet/parameters/accept_port"
	lnetNetworkParameterFile = "/sys/module/lnet/parameters/networks"
	lustreModuleDir          = "/sys/module/lustre"

	previewVersionRegex = regexp.MustCompile(`^[0-9]+\.[0-9]+$`)
	tcp0NetworkRegex    = regexp.MustCompile(`tcp0\(([^)]*)\)`)

	// defaultRDMAModuleArgs are the default ko2iblnd tuning parameters for iRDMA-enabled nodes.
	defaultRDMAModuleArgs = []string{
		"ko2iblnd.credits=8192",
		"ko2iblnd.ntx=8192",
		"ko2iblnd.peer_credits=15",
		"ko2iblnd.peer_credits_hiw=7",
		"ko2iblnd.nscheds=16",
		"ko2iblnd.conns_per_peer=8",
		"ko2iblnd.fmr_pool_size=2048",
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
// It proceeds with the installation using the provided standard and optional RDMA NICs.
func InstallLustreKmodOnCos(ctx context.Context, enableLegacyPort bool, customModuleArgs []string, nics []string, rdmaNICs []string, disableMultiNIC bool, primaryNic, previewClientVersion string) error {
	return installLustreKmodOnCos(ctx, enableLegacyPort, customModuleArgs, nics, rdmaNICs, disableMultiNIC, primaryNic, previewClientVersion, runCosDKMS)
}

func runCosDKMS(ctx context.Context, args []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "/usr/bin/cos-dkms", args...)
	klog.Infof("Installing Lustre kernel modules by executing command: %s", cmd.String())
	return cmd.CombinedOutput()
}

func installLustreKmodOnCos(
	ctx context.Context,
	enableLegacyPort bool,
	customModuleArgs []string,
	nics []string,
	rdmaNICs []string,
	disableMultiNIC bool,
	primaryNic string,
	previewClientVersion string,
	execCosDKMS func(ctx context.Context, args []string) ([]byte, error),
) error {
	lnetPort := lnetPort(enableLegacyPort)
	isRDMA := len(rdmaNICs) > 0
	var expectedNetwork string
	var cpuPartitions int
	if isRDMA {
		cpuPartitions = CalculateCPUPartitions(runtime.NumCPU(), len(rdmaNICs))
		expectedNetwork = BuildRDMALnetNetworkString(rdmaNICs, primaryNic, cpuPartitions)
	} else {
		expectedNetwork = BuildLnetNetworkString(nics, primaryNic, disableMultiNIC)
	}

	cmdCtx, cancel := context.WithTimeout(ctx, cmdTimeout)
	defer cancel()

	// TODO(samhalim): Add latency/success rate metrics for kmod cos-dkms install.
	candidates := cosDKMSPackages(previewClientVersion)
	for i, pkg := range candidates {
		args := buildCosDKMSArgs(pkg, previewClientVersion, lnetPort, expectedNetwork, customModuleArgs, disableMultiNIC, isRDMA, cpuPartitions)
		output, err := execCosDKMS(cmdCtx, args)
		if err == nil {
			klog.Infof("COS-DKMS output:\n%s\n", string(output))
			return nil
		}

		outputStr := string(output)
		lowerOutput := strings.ToLower(outputStr)
		if strings.Contains(lowerOutput, "operation not permitted") && strings.Contains(lowerOutput, "insmod") {
			msg := "lustre kernel module installation failed due to permission issue. Please follow https://docs.cloud.google.com/managed-lustre/docs/lustre-csi-driver-new-volume#node-upgrade-required to upgrade node pool to allow GKE managed Lustre CSI driver installation to complete, or disabling loadpin if you are installing the Lustre CSI driver from open source"

			return fmt.Errorf("command execution failed: %w. %s", err, msg)
		}

		if cmdCtx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("command timed out after %s: %w\noutput:\n%s", cmdTimeout, err, outputStr)
		}

		if i < len(candidates)-1 {
			klog.Infof("COS-DKMS install failed for package %q (preview version %q): %v; trying next candidate", pkg, previewClientVersion, err)
			continue
		}

		return fmt.Errorf("command execution failed: %w\noutput:\n%s", err, outputStr)
	}

	return nil
}

// InstallLustreKmodOnUbuntu installs the Lustre kernel modules on Ubuntu nodes.
func InstallLustreKmodOnUbuntu(ctx context.Context, enableLegacyPort bool, customModuleArgs []string, nics []string, disableMultiNIC bool, primaryNic, previewClientVersion string) error {
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
	downloadArgs := buildUbuntuAptDownloadArgs(kernelVersion, previewClientVersion)
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
	if !disableMultiNIC {
		lnetArgs = append(lnetArgs, fmt.Sprintf("lnet_numa_range=%d", lnetNumaRange))
	}
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

	modinfoCmd := exec.CommandContext(cmdCtx, "modinfo", "-b", extractDir, "-k", kernelVersion, "-F", "version", "lustre")
	modVerOut, err := modinfoCmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
			return fmt.Errorf("modinfo lustre failed: %w\noutput:\n%s", err, string(exitErr.Stderr))
		}
		return fmt.Errorf("modinfo lustre failed: %w", err)
	}

	// Load the underlying TCP network driver (ksocklnd), RPC security module (ptlrpc_gss for 2.16+ packages),
	// core lustre module, and required client subsystem modules (mgc: management, lmv/mdc: metadata,
	// lov/osc: object storage) via modprobe to ensure they are resident in kernel memory before
	// the ephemeral module files are removed.
	clientModules, err := ubuntuClientModules(string(modVerOut))
	if err != nil {
		return err
	}
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

// ubuntuClientModules returns the ordered list of Lustre client kernel modules to load via modprobe
// based on the extracted Lustre module version. For Lustre 2.16+, ptlrpc_gss is loaded right after
// ksocklnd and before the lustre filesystem and client OBD modules so the RPC security policy is
// registered in ptlrpc before higher-level client modules initialize.
func ubuntuClientModules(verOut string) ([]string, error) {
	ok, verStr, err := util.LustreSupportsGSSIAM(verOut)
	if err != nil {
		return nil, fmt.Errorf("parsing lustre module version %q: %w", verStr, err)
	}
	if ok {
		klog.Infof("Lustre module version %s >= %s; including ptlrpc_gss module", verStr, util.MinGSSIAMVersion)
		return []string{"ksocklnd", "ptlrpc_gss", "lustre", "mgc", "lmv", "lov", "mdc", "osc"}, nil
	}
	klog.Infof("Skipping ptlrpc_gss module: Lustre module version %s < %s", verStr, util.MinGSSIAMVersion)
	return []string{"ksocklnd", "lustre", "mgc", "lmv", "lov", "mdc", "osc"}, nil
}

func parseLnetNetwork(networkStr string) []string {
	// Extracts standard TCP NICs from formats such as:
	// - "tcp0(eth0,eth1)"
	// - "o2ib(rdma0[0,1,2,3],rdma1[4,5,6,7]),tcp0(eth0)"
	if matches := tcp0NetworkRegex.FindStringSubmatch(networkStr); len(matches) == 2 {
		if matches[1] == "" {
			return []string{}
		}
		return strings.Split(matches[1], ",")
	}
	return []string{}
}

// GetNodeLabelValue returns the value of the specified label from the node.
func GetNodeLabelValue(ctx context.Context, nodeID string, labelKey string, nc network.NodeClient) (string, error) {
	node, err := nc.GetNodeWithRetry(ctx, nodeID)
	if err != nil {
		return "", err
	}
	if node == nil {
		return "", fmt.Errorf("node object %s is empty", nodeID)
	}
	return strings.TrimSpace(node.GetLabels()[labelKey]), nil
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

// CalculateCPUPartitions computes libcfs.cpu_npartitions so partitions are evenly
// divisible across rdmaNICs without exceeding the host's CPU budget.
func CalculateCPUPartitions(numCPUs, numRDMANICs int) int {
	if numRDMANICs <= 0 || numCPUs <= 0 {
		return 1
	}

	maxPartitionsByCPU := numCPUs / minCPUsPerPartition
	// Try 4, 2, then 1 partitions per NIC.
	for _, partsPerNIC := range []int{maxPartitionsPerRDMANIC, 2, 1} {
		total := numRDMANICs * partsPerNIC
		if total <= maxPartitionsByCPU {
			return total
		}
	}

	// Fallback: 1 partition per NIC, never exceeding actual host CPUs.
	if numRDMANICs > numCPUs {
		return numCPUs
	}
	return numRDMANICs
}

// BuildRDMALnetNetworkString constructs the LNet network string for RDMA nodes,
// spreading cpuPartitions evenly across sorted rdmaNICs, e.g.:
// "o2ib(rdma0[0,1,2,3],rdma1[4,5,6,7]),tcp0(eth0)".
func BuildRDMALnetNetworkString(rdmaNICs []string, primaryNic string, cpuPartitions int) string {
	if len(rdmaNICs) == 0 {
		return fmt.Sprintf("tcp0(%s)", primaryNic)
	}

	sortedRDMA := slices.Clone(rdmaNICs)
	slices.Sort(sortedRDMA)

	// If fewer partitions than NICs (low-CPU edge case), omit explicit CPT lists
	// so LNet binds NICs across available CPTs automatically.
	if cpuPartitions < len(sortedRDMA) {
		return fmt.Sprintf("o2ib(%s),tcp0(%s)", strings.Join(sortedRDMA, ","), primaryNic)
	}

	baseParts := cpuPartitions / len(sortedRDMA)
	remainder := cpuPartitions % len(sortedRDMA)

	nicEntries := make([]string, 0, len(sortedRDMA))
	cptIdx := 0
	for i, nic := range sortedRDMA {
		count := baseParts
		if i < remainder {
			count++
		}
		parts := make([]string, 0, count)
		for j := 0; j < count; j++ {
			parts = append(parts, strconv.Itoa(cptIdx))
			cptIdx++
		}
		nicEntries = append(nicEntries, fmt.Sprintf("%s[%s]", nic, strings.Join(parts, ",")))
	}

	return fmt.Sprintf("o2ib(%s),tcp0(%s)", strings.Join(nicEntries, ","), primaryNic)
}

// isPreviewVersion returns true if the version string represents a preview minor release stream (e.g. "2.16"),
// for which the latest or max compatible build should be installed.
func isPreviewVersion(version string) bool {
	return previewVersionRegex.MatchString(strings.TrimSpace(version))
}

// cosDKMSPackages returns the ordered candidate COS-DKMS package names to try for previewClientVersion.
func cosDKMSPackages(previewClientVersion string) []string {
	trimmed := strings.TrimSpace(previewClientVersion)
	if trimmed == "" {
		return []string{"lustre-client-drivers"}
	}
	if trimmed == "2.16" || strings.HasPrefix(trimmed, "2.16.") || strings.HasPrefix(trimmed, "2.16_") || strings.HasPrefix(trimmed, "2.16-") {
		return []string{"lustre-client-drivers-2_16", "lustre-client-drivers", "lustre-client-drivers-pre"}
	}
	return []string{"lustre-client-drivers", "lustre-client-drivers-pre"}
}

// buildCosDKMSArgs constructs the cos-dkms install command arguments.
func buildCosDKMSArgs(lustreClientDriverPackage, previewClientVersion string, lnetPort int, expectedNetwork string, customModuleArgs []string, disableMultiNIC, isRDMA bool, cpuPartitions int) []string {
	versionArgs := []string{
		"--latest",
	}
	if previewClientVersion != "" {
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
	if !disableMultiNIC || isRDMA {
		args = append(args, fmt.Sprintf("--module-arg=lnet.lnet_numa_range=%d", lnetNumaRange))
	}
	args = append(args, fmt.Sprintf(`--module-arg=lnet.networks="%s"`, expectedNetwork))

	if isRDMA {
		// Track keys specified by customModuleArgs so user overrides win cleanly.
		customKeys := make(map[string]bool, len(customModuleArgs))
		for _, arg := range customModuleArgs {
			if k, _, ok := strings.Cut(arg, "="); ok {
				customKeys[k] = true
			}
		}

		rdmaArgs := make([]string, 0, len(defaultRDMAModuleArgs)+1)
		if cpuPartitions > 0 {
			rdmaArgs = append(rdmaArgs, fmt.Sprintf("libcfs.cpu_npartitions=%d", cpuPartitions))
		}
		rdmaArgs = append(rdmaArgs, defaultRDMAModuleArgs...)

		for _, arg := range rdmaArgs {
			if k, _, ok := strings.Cut(arg, "="); ok && customKeys[k] {
				continue
			}
			args = append(args, "--module-arg="+arg)
		}
	}

	for _, arg := range customModuleArgs {
		args = append(args, "--module-arg="+arg)
	}

	return args
}

// buildUbuntuAptDownloadArgs constructs the apt-get download arguments for Lustre client modules on Ubuntu.
func buildUbuntuAptDownloadArgs(kernelVersion, previewClientVersion string) []string {
	if previewClientVersion == "" {
		return []string{"download", fmt.Sprintf("lustre-client-modules-%s/lustre-client-ubuntu-noble", kernelVersion)}
	}

	pkgTarget := fmt.Sprintf("lustre-client-modules-%s", kernelVersion)
	if isPreviewVersion(previewClientVersion) {
		pkgTarget = fmt.Sprintf("%s=%s*", pkgTarget, previewClientVersion)
	} else {
		pkgTarget = fmt.Sprintf("%s=%s", pkgTarget, previewClientVersion)
	}

	return []string{"download", "-t", "lustre-client-ubuntu-noble-preview", pkgTarget}
}
