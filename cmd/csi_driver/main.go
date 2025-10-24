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

package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/GoogleCloudPlatform/lustre-csi-driver/pkg/cloud_provider/lustre"
	"github.com/GoogleCloudPlatform/lustre-csi-driver/pkg/cloud_provider/metadata"
	driver "github.com/GoogleCloudPlatform/lustre-csi-driver/pkg/csi_driver"
	"github.com/GoogleCloudPlatform/lustre-csi-driver/pkg/metrics"
	"github.com/vishvananda/netlink"
	"k8s.io/klog/v2"
	"k8s.io/mount-utils"
)

const (
	multiRailLabel = "lustre.csi.storage.gke.io/multi-rail"
	lnetPortFile   = "/sys/module/lnet/parameters/accept_port"
)

var (
	endpoint               = flag.String("endpoint", "unix:/tmp/csi.sock", "CSI endpoint")
	nodeID                 = flag.String("nodeid", "", "node id")
	runController          = flag.Bool("controller", false, "run controller service")
	runNode                = flag.Bool("node", false, "run node service")
	httpEndpoint           = flag.String("http-endpoint", "", "The TCP network address where the prometheus metrics endpoint will listen (example: `:8080`). The default is empty string, which means metrics endpoint is disabled.")
	metricsPath            = flag.String("metrics-path", "/metrics", "The HTTP path where prometheus metrics will be exposed. Default is `/metrics`.")
	lustreAPIEndpoint      = flag.String("lustre-endpoint", "", "Lustre API service endpoint, supported values are autopush, staging and prod.")
	cloudConfigFilePath    = flag.String("cloud-config", "", "Path to GCE cloud provider config")
	enableLegacyLustrePort = flag.Bool("enable-legacy-lustre-port", false, "If set to true, the CSI driver controller will provision Lustre instance with the gkeSupportEnabled flag")
	// These are set at compile time.
	version = "unknown"
)

func getGvnicNames() ([]string, error) {
	links, err := netlink.LinkList()
	if err != nil {
		return nil, fmt.Errorf("failed to list network interfaces: %w", err)
	}

	var ethNics []string
	for _, link := range links {
		if strings.HasPrefix(link.Attrs().Name, "eth") {
			ethNics = append(ethNics, link.Attrs().Name)
		}
	}

	return ethNics, nil
}

func configureRoutesForNic(nicName string, ipAddr net.IP, tableID int) error {
	// Get the network interface handle
	link, err := netlink.LinkByName(nicName)
	if err != nil {
		return fmt.Errorf("failed to get interface %s: %w", nicName, err)
	}

	// Find the gateway for this interface
	routes, err := netlink.RouteList(link, netlink.FAMILY_V4)
	if err != nil {
		return fmt.Errorf("failed to list routes for %s: %w", nicName, err)
	}

	var gateway net.IP
	for _, r := range routes {
		if r.Gw != nil {
			gateway = r.Gw

			break
		}
	}

	if gateway == nil {
		return fmt.Errorf("could not find gateway for %s", nicName)
	}
	klog.Infof("Found gateway %s for %s", gateway.String(), nicName)

	// Define and add the route
	_, dst, _ := net.ParseCIDR("10.170.0.0/16") // MGS IP
	route := &netlink.Route{
		LinkIndex: link.Attrs().Index,
		Dst:       dst,
		Gw:        gateway,
		Table:     tableID,
	}
	if err := netlink.RouteReplace(route); err != nil {
		return fmt.Errorf("failed to replace/add route for %s: %w", nicName, err)
	}
	klog.Infof("Successfully added (or replace) route for %s", nicName)

	// Define and add the rule
	rule := netlink.NewRule()
	rule.Table = tableID
	rule.Src = &net.IPNet{IP: ipAddr, Mask: net.CIDRMask(32, 32)} // /32 mask for a single IP
	if err := netlink.RuleAdd(rule); err != nil {
		return fmt.Errorf("failed to add rule for %s: %w", nicName, err)
	}
	klog.Infof("Successfully added rule for %s", nicName)

	return nil
}

func isMultiRailEnabled() (bool, error) {
	// TODO(halimsam): Add Multi Rail Logic here.
	return false, nil
}

func getLnetPort() int {
	legacyPortEnabled := os.Getenv("ENABLE_LEGACY_LUSTRE_PORT")
	if legacyPortEnabled == "true" {
		return 6988
	}

	return 988
}

func checkLnetPort(expectedPort int) error {
	file, err := os.ReadFile(lnetPortFile)
	if err != nil {
		if os.IsNotExist(err) {
			klog.V(4).Infof("LNET port file %v not found. Skipping check", lnetPortFile)

			return nil
		}

		return fmt.Errorf("error reading file %s: %w", lnetPortFile, err)
	}
	currPort := strings.TrimSpace(string(file))
	expectedPortStr := strconv.Itoa(expectedPort)
	if currPort != expectedPortStr {
		klog.V(3).Infof("Your node already has Lustre kernel modules installed with an outdated configuration. Please upgrade your node pool to apply the correct settings.")

		return fmt.Errorf("lnet port mismatched, expected %s, but found: %s", expectedPortStr, currPort)
	}

	log.Println("LNET_PORT matches configuration. Check passed.")

	return nil
}

func configureRoute(nicName string, tableID int) error {
	link, err := netlink.LinkByName(nicName)
	if err != nil {
		return fmt.Errorf("could not get link for %s: %w", nicName, err)
	}
	addrs, err := netlink.AddrList(link, netlink.FAMILY_V4)
	if err != nil || len(addrs) == 0 {
		return fmt.Errorf("could not get address for %s: %w", nicName, err)
	}
	klog.Infof("IP address are: %+v", addrs)
	ipAddr := addrs[0].IP
	if err := configureRoutesForNic(nicName, ipAddr, tableID); err != nil {
		return fmt.Errorf("failed to configure routes for %s: %w", nicName, err)
	}

	return nil
}

func kmodInstaller() error {
	if err := checkLnetPort(getLnetPort()); err != nil {
		return err
	}
	nics, err := getGvnicNames()
	if err != nil {
		return fmt.Errorf("error getting nic names: %w", err)
	}
	if len(nics) == 0 {
		return fmt.Errorf("no nics with eth prefix found")
	}
	isMultiNic, err := isMultiRailEnabled()
	if err != nil {
		return err
	}
	klog.V(4).Infof("Is using LustreCSI Multi-Rail feature: %v", isMultiNic)

	lnetArg := fmt.Sprintf(`lnet.networks="tcp0(%s)"`, nics[0])
	if isMultiNic {
		lnetArg = fmt.Sprintf(`lnet.networks="tcp0(%s)"`, strings.Join(nics, ","))
	}

	cmd := exec.Command("/usr/bin/cos-dkms", "install", "lustre-client-drivers",
		"--gcs-bucket=cos-default",
		"--latest",
		"-w", "0",
		"--kernelmodulestree=/host_modules",
		"--module-arg="+lnetArg,
		"--module-arg=lnet.accept_port="+strconv.Itoa(getLnetPort()),
		"--lsb-release-path=/host_etc/lsb-release",
		"--insert-on-install",
		"--logtostderr")

	klog.Infof("Executing command: %s", cmd.String())
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("command execution failed: %w\noutput:\n%s", err, string(output))
	}

	klog.Infof("Command output:\n%s\n", string(output))
	// 100 is a placeholder table id.
	tableID := 100
	if isMultiNic {
		// Configure route for all NICS
		for _, nicName := range nics {
			err = configureRoute(nicName, tableID)
			if err != nil {
				return err
			}
			tableID++
		}
	} else {
		// Configure route for one NIC.
		err = configureRoute(nics[0], tableID)
		if err != nil {
			return err
		}
	}

	return nil
}

func main() {
	klog.InitFlags(nil)
	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := kmodInstaller(); err != nil {
		klog.Fatalf("kmod install failure: %v", err)
	}

	config := &driver.LustreDriverConfig{
		Name:                   driver.DefaultName,
		Version:                version,
		RunController:          *runController,
		RunNode:                *runNode,
		EnableLegacyLustrePort: *enableLegacyLustrePort,
	}

	if *runNode {
		if *nodeID == "" {
			klog.Fatalf("NodeID cannot be empty for node service")
		}
		config.NodeID = *nodeID

		meta, err := metadata.NewMetadataService(ctx)
		if err != nil {
			klog.Fatalf("Failed to set up metadata service: %v", err)
		}
		klog.Infof("Metadata service setup: %+v", meta)
		config.MetadataService = meta

		config.Mounter = mount.New("")
	}

	if *runController {
		if *httpEndpoint != "" && metrics.IsGKEComponentVersionAvailable() {
			mm := metrics.NewMetricsManager()
			mm.InitializeHTTPHandler(*httpEndpoint, *metricsPath)
			if err := mm.EmitGKEComponentVersion(); err != nil {
				klog.Fatalf("Failed to emit GKE component version: %v", err)
			}
		}

		if *lustreAPIEndpoint == "" {
			*lustreAPIEndpoint = "prod"
		}
		cloudProvider, err := lustre.NewCloud(ctx, *cloudConfigFilePath, version, *lustreAPIEndpoint)
		if err != nil {
			klog.Fatalf("Failed to initialize cloud provider: %v", err)
		}
		config.Cloud = cloudProvider
	}

	lustreDriver, err := driver.NewLustreDriver(config)
	if err != nil {
		klog.Fatalf("Failed to initialize Lustre CSI Driver: %v", err)
	}

	klog.Infof("Running Lustre CSI driver version %v", version)
	lustreDriver.Run(*endpoint)
}
