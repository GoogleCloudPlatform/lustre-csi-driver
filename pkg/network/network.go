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

package network

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/GoogleCloudPlatform/lustre-csi-driver/pkg/cloud_provider/metadata"
	"github.com/GoogleCloudPlatform/lustre-csi-driver/pkg/k8sclient"
	"github.com/safchain/ethtool"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	v1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
)

const (
	multiNICLabel      = "lustre.csi.storage.gke.io/multi-nic"
	multiNICLabelAlias = "lustre.csi.storage.gke.io/multi-rail"
	RDMANodeLabel      = "lustre.csi.storage.gke.io/rdma"
	// max user specified table ID is 252. 253, 254, and 255 are reserved (default, main, local).
	maxTableID = 252
)

// netlinker is an interface to abstract the netlink package functions used by this package.
// This enables mocking/faking in tests.
type netlinker interface {
	LinkList() ([]netlink.Link, error)
	LinkByName(name string) (netlink.Link, error)
	AddrList(link netlink.Link, family int) ([]netlink.Addr, error)
	RouteList(link netlink.Link, family int) ([]netlink.Route, error)
	RouteReplace(route *netlink.Route) error
	RuleAdd(rule *netlink.Rule) error
	RuleDel(rule *netlink.Rule) error
	RuleList(family int) ([]netlink.Rule, error)
	RouteListFiltered(family int, filter *netlink.Route, mask uint64) ([]netlink.Route, error)
	GetStandardNICs() ([]string, error)
	GetIRDMANICs() ([]string, error)
}

type NodeClient interface {
	GetNodeWithRetry(ctx context.Context, nodeName string) (*v1.Node, error)
}

// MetadataClient abstracts the metadata service.
type MetadataClient interface {
	GetNetworkInterfaces() ([]metadata.NetworkInterface, error)
}

// RouteManager provides methods for network configuration using the netlinker interface.
type RouteManager struct {
	nl           netlinker
	nc           NodeClient
	mc           MetadataClient
	sysctlWriter func(path, val string) error
}

// realNetlink is the real implementation of netlinker,
// which calls the actual vishvananda/netlink functions.
type realNetlink struct {
	hasInfiniband func(name string) bool
	linkListFn    func() ([]netlink.Link, error)
	driverNameFn  func(name string) (string, error)
}

func defaultHasInfiniband(ifName string) bool {
	_, err := os.Stat(fmt.Sprintf("/sys/class/net/%s/device/infiniband", ifName))
	return err == nil
}

func (r *realNetlink) hasIB(name string) bool {
	if r.hasInfiniband != nil {
		return r.hasInfiniband(name)
	}
	return defaultHasInfiniband(name)
}

func (r *realNetlink) getLinks() ([]netlink.Link, error) {
	if r.linkListFn != nil {
		return r.linkListFn()
	}
	return netlink.LinkList()
}

func (r *realNetlink) getDriverName(eth *ethtool.Ethtool, name string) (string, error) {
	if r.driverNameFn != nil {
		return r.driverNameFn(name)
	}
	var driver string
	var driverErr error
	if eth != nil {
		driver, driverErr = eth.DriverName(name)
	}
	if eth == nil || driverErr != nil {
		driver = readDriverNameFromSysfs(name)
		if driver == "" {
			return "", driverErr
		}
	}
	return driver, nil
}

type k8sNodeClient struct{}

func (client *k8sNodeClient) GetNodeWithRetry(ctx context.Context, nodeName string) (*v1.Node, error) {
	return k8sclient.GetNodeWithRetry(ctx, nodeName)
}

func (r *realNetlink) LinkList() ([]netlink.Link, error)            { return netlink.LinkList() }
func (r *realNetlink) LinkByName(name string) (netlink.Link, error) { return netlink.LinkByName(name) }
func (r *realNetlink) AddrList(link netlink.Link, family int) ([]netlink.Addr, error) {
	return netlink.AddrList(link, family)
}

func (r *realNetlink) RouteList(link netlink.Link, family int) ([]netlink.Route, error) {
	return netlink.RouteList(link, family)
}
func (r *realNetlink) RouteReplace(route *netlink.Route) error { return netlink.RouteReplace(route) }
func (r *realNetlink) RuleAdd(rule *netlink.Rule) error        { return netlink.RuleAdd(rule) }
func (r *realNetlink) RuleDel(rule *netlink.Rule) error        { return netlink.RuleDel(rule) }
func (r *realNetlink) RuleList(family int) ([]netlink.Rule, error) {
	return netlink.RuleList(family)
}

func (r *realNetlink) RouteListFiltered(family int, filter *netlink.Route, mask uint64) ([]netlink.Route, error) {
	return netlink.RouteListFiltered(family, filter, mask)
}

func (r *realNetlink) GetStandardNICs() ([]string, error) {
	links, err := r.getLinks()
	if err != nil {
		return nil, fmt.Errorf("failed to list network interfaces: %w", err)
	}

	eth, err := ethtool.NewEthtool()
	if err != nil {
		return nil, fmt.Errorf("failed to create ethtool: %w", err)
	}
	defer eth.Close()

	var ethNICs []string
	for _, link := range links {
		name := link.Attrs().Name
		driver, err := r.getDriverName(eth, name)
		if err != nil {
			klog.Warningf("Failed to get driver for interface %s: %v", name, err)

			continue
		}

		// We only care about standard NICs (gVNIC or VirtIO-Net) for Lustre traffic.
		// RDMA NICs (like irdma, mlx5_core) should be excluded as they are handled separately
		// and might have names like ethN or gpu0rdma0.
		if driver == "gve" || driver == "virtio_net" || (driver == "idpf" && !r.hasIB(name)) {
			klog.V(4).Infof("Found valid NIC %s with driver %s", name, driver)
			ethNICs = append(ethNICs, name)
		}
	}

	klog.V(4).Infof("Found %d valid standard NICs: %v", len(ethNICs), ethNICs)

	return ethNICs, nil
}

func readDriverNameFromSysfs(ifName string) string {
	target, err := os.Readlink(fmt.Sprintf("/sys/class/net/%s/device/driver", ifName))
	if err != nil {
		return ""
	}
	return filepath.Base(target)
}

func (r *realNetlink) GetIRDMANICs() ([]string, error) {
	links, err := r.getLinks()
	if err != nil {
		return nil, fmt.Errorf("failed to list network interfaces: %w", err)
	}

	eth, err := ethtool.NewEthtool()
	if err != nil {
		return nil, fmt.Errorf("failed to create ethtool: %w", err)
	}
	defer eth.Close()

	var irdmaNICs []string
	for _, link := range links {
		name := link.Attrs().Name
		driver, err := r.getDriverName(eth, name)
		if err != nil {
			continue
		}

		if driver == "idpf" && r.hasIB(name) {
			klog.V(4).Infof("Found valid idpf RDMA NIC %s with driver %s", name, driver)
			irdmaNICs = append(irdmaNICs, name)
		}
	}

	klog.V(4).Infof("Found %d idpf RDMA NICs: %v", len(irdmaNICs), irdmaNICs)
	return irdmaNICs, nil
}

// NewNetlink creates an instance of netlinker.
func NewNetlink() netlinker {
	return &realNetlink{}
}

func NewK8sClient() NodeClient {
	return &k8sNodeClient{}
}

func Manager(nl netlinker, nc NodeClient, mc MetadataClient) *RouteManager {
	return &RouteManager{
		nl: nl,
		nc: nc,
		mc: mc,
		sysctlWriter: func(path, val string) error {
			return os.WriteFile(path, []byte(val), 0o644)
		},
	}
}

// GetStandardNICs gets all standard NICs on the node which are within the same VPC network
// as the GCE instance primary network.
func (rm *RouteManager) GetStandardNICs() ([]string, error) {
	nics, err := rm.nl.GetStandardNICs()
	if err != nil {
		return nil, err
	}

	// Sort the NICs to ensure deterministic order.
	// This is important for bare metal machines where NICs can be returned in random order. (b/470079386)
	// For example, we want to ensure eth0 comes before eth1.
	sort.Strings(nics)

	// If we have 0 or 1 NIC, no need to validate VPC as single NIC is always valid (primary).
	// Actually, if 1 NIC, it must be primary.
	if len(nics) <= 1 {
		return nics, nil
	}

	if rm.mc == nil {
		return nil, errors.New("metadata client is nil, cannot perform VPC validation for multi-NIC setup")
	}

	return rm.validateNICs(nics)
}

// validateNICs filters the provided NICs, returning only those that belong to the same VPC network
// as the primary interface (eth0). It uses cached metadata to perform the validation.
func (rm *RouteManager) validateNICs(nics []string) ([]string, error) {
	// Get network interfaces from metadata (cached).
	metaNICs, err := rm.mc.GetNetworkInterfaces()
	if err != nil {
		return nil, fmt.Errorf("failed to get network interfaces from metadata: %w", err)
	}

	if len(metaNICs) == 0 {
		return nil, errors.New("no network interfaces found in instance metadata; cannot perform VPC validation")
	}

	// The first network interface in the metadata list corresponds to the primary NIC (index 0).
	primaryNetwork := metaNICs[0].Network

	// Iterate over all discovered NICs and validate them against the metadata.
	// We match NICs by MAC address and ensure they belong to the same VPC network
	// as the primary interface.
	validNICs := make([]string, 0, len(nics))
	metaNICMap := make(map[string]metadata.NetworkInterface, len(metaNICs))
	for _, metaNIC := range metaNICs {
		metaNICMap[strings.ToLower(metaNIC.Mac)] = metaNIC
	}

	for _, nic := range nics {
		link, err := rm.nl.LinkByName(nic)
		if err != nil {
			klog.Warningf("Failed to get link for %s: %v", nic, err)

			continue
		}
		mac := strings.ToLower(link.Attrs().HardwareAddr.String())

		if metaNIC, ok := metaNICMap[mac]; ok {
			if metaNIC.Network == primaryNetwork {
				validNICs = append(validNICs, nic)
			} else {
				klog.Infof("Skipping nic %s because it is in a different VPC network: %s (primary: %s)", nic, metaNIC.Network, primaryNetwork)
			}
		} else {
			klog.Warningf("NIC %s with MAC %s not found in instance metadata, skipping", nic, mac)
		}
	}

	klog.Infof("Filtered NICs: %v (original: %v)", validNICs, nics)

	return validNICs, nil
}

// GetPrimaryNIC identifies the primary network interface name (e.g. eth0, ens4) by matching
// the kernel network interface MAC address with GCE metadata interface 0 MAC address.
func (rm *RouteManager) GetPrimaryNIC() (string, error) {
	if rm.mc == nil {
		return "", errors.New("metadata client is nil, cannot identify primary NIC")
	}

	metaNICs, err := rm.mc.GetNetworkInterfaces()
	if err != nil {
		return "", fmt.Errorf("failed to get network interfaces from metadata: %w", err)
	}

	if len(metaNICs) == 0 {
		return "", errors.New("no network interfaces found in instance metadata")
	}

	primaryMAC := strings.ToLower(metaNICs[0].Mac)

	nics, err := rm.nl.GetStandardNICs()
	if err != nil {
		return "", fmt.Errorf("failed to get standard NICs: %w", err)
	}

	for _, nic := range nics {
		link, err := rm.nl.LinkByName(nic)
		if err != nil {
			klog.Warningf("Failed to get link for %s: %v", nic, err)
			continue
		}
		mac := strings.ToLower(link.Attrs().HardwareAddr.String())
		if mac == primaryMAC {
			klog.V(4).Infof("Identified primary NIC %s with MAC %s", nic, mac)
			return nic, nil
		}
	}

	return "", fmt.Errorf("no standard NIC found matching primary metadata MAC %s", primaryMAC)
}

func (rm *RouteManager) configureRoutesForNIC(nicName string, nicIPAddr net.IP, instanceIPAddr string, tableID int) error {
	// Get the network interface handle
	link, err := rm.nl.LinkByName(nicName)
	if err != nil {
		return fmt.Errorf("failed to get interface %s: %w", nicName, err)
	}

	// Find the gateway for this interface
	routes, err := rm.nl.RouteList(link, netlink.FAMILY_V4)
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
	mask := net.CIDRMask(16, 32) // /16 subnet, 32 bits (IPv4)
	// This is the IP of the Lustre instance which is passed in through volumeContext.
	ip := net.ParseIP(instanceIPAddr)
	if ip == nil {
		return fmt.Errorf("invalid ip address format: %v", instanceIPAddr)
	}
	network := ip.Mask(mask)
	networkInstanceIP := network.String() + "/16"
	klog.Infof("Adding route for IP: %v", networkInstanceIP)
	_, dst, _ := net.ParseCIDR(networkInstanceIP)
	route := &netlink.Route{
		LinkIndex: link.Attrs().Index,
		Dst:       dst, // lustre ip
		Gw:        gateway,
		Table:     tableID,
	}
	if err := rm.nl.RouteReplace(route); err != nil {
		return fmt.Errorf("failed to replace/add route for %s: %w", nicName, err)
	}
	klog.Infof("Successfully added (or replace) route for %s", nicName)

	// Define rule
	rule := netlink.NewRule()
	rule.Table = tableID
	rule.Src = &net.IPNet{IP: nicIPAddr, Mask: net.CIDRMask(32, 32)} // /32 mask for a single IP

	// Check if rule already exist
	existingRules, err := rm.nl.RuleList(netlink.FAMILY_V4)
	if err != nil {
		return fmt.Errorf("failed to list rules: %w", err)
	}
	for _, r := range existingRules {
		// Check for an exact match on the source IP and destination table id.
		if r.Table == rule.Table && r.Src != nil && r.Src.String() == rule.Src.String() {
			klog.V(4).Infof("Rule matching Src %s and Table %d already exists, skipping addition.",
				r.Src.String(), r.Table)

			return nil
		}
	}

	// Define and add the rule if rule doesn't exist.
	if err := rm.nl.RuleAdd(rule); err != nil {
		if os.IsExist(err) {
			klog.V(4).Infof("Rule for %s already exists in table %d, skipping.", nicName, tableID)
		} else {
			return fmt.Errorf("failed to add rule for %s: %w", nicName, err)
		}
	}
	klog.Infof("Successfully added rule for %s", nicName)

	return nil
}

// ConfigureRoute configures route between Lustre Instance and NIC on an available Table ID.
func (rm *RouteManager) ConfigureRoute(nicName string, instanceIP string, tableID int) error {
	nicIPAddr, err := rm.GetNICIPAddr(nicName)
	if err != nil {
		return fmt.Errorf("failed to get IP address for %s: %w", nicName, err)
	}

	if err := rm.configureRoutesForNIC(nicName, nicIPAddr, instanceIP, tableID); err != nil {
		return fmt.Errorf("failed to configure routes for %s: %w", nicName, err)
	}

	return nil
}

// GetNICIPAddr gets primary IP Address of NIC.
func (rm *RouteManager) GetNICIPAddr(nicName string) (net.IP, error) {
	// Get the network interface handle
	link, err := rm.nl.LinkByName(nicName)
	if err != nil {
		return nil, fmt.Errorf("could not get link %s: %w", nicName, err)
	}

	addrs, err := rm.nl.AddrList(link, netlink.FAMILY_V4)
	if err != nil {
		return nil, fmt.Errorf("could not get address for %s: %w", nicName, err)
	}

	if len(addrs) == 0 {
		return nil, fmt.Errorf("could not get address for %s", nicName)
	}
	klog.Infof("NIC %v IP address are: %+v", nicName, addrs)

	// We capture primary IP address which is at index 0.
	return addrs[0].IP, nil
}

// CheckDisableMultiNIC checks for Multi-NIC feature via node label and cluster configuration.
// return true if disabled. return false if multi nic is enabled.
// if node label is specified, then it overrides cluster configuration for multi nic setup.
func (rm *RouteManager) CheckDisableMultiNIC(ctx context.Context, nodeID string, nics []string, disableMultiNIC bool) (bool, error) {
	klog.V(4).Infof("Checking if node label %s exists in %s", multiNICLabel, nodeID)
	if len(nics) <= 1 {
		klog.V(4).Infof("Node only has 1 nic or less available. Not suitable for Multi-NIC feature. current nics: %v", nics)

		return true, nil
	}
	node, err := rm.nc.GetNodeWithRetry(ctx, nodeID)
	if err != nil {
		return false, err
	}

	val, found := node.GetLabels()[multiNICLabel]
	labelName := multiNICLabel
	if !found {
		val, found = node.GetLabels()[multiNICLabelAlias]
		labelName = multiNICLabelAlias
	}

	if found {
		isMultiNICEnabled, err := strconv.ParseBool(val)
		if err != nil {
			return false, err
		}
		klog.Infof("Node: %v, is MultiNic Enabled: %v (via %s)", nodeID, isMultiNICEnabled, labelName)

		// Ex: If Multi-Nic is enabled, then we return false since we don't want to disable it.
		return !isMultiNICEnabled, nil
	}

	klog.Infof("Node %v, disable MultiNIC: %v", nodeID, disableMultiNIC)

	// if label not found, then default will be whatever is passed from cluster configuration.
	return disableMultiNIC, nil
}

func (rm *RouteManager) isTableFree(tableID int, targetIP net.IP) (bool, error) {
	// Check if any active Policy Rules point to this table
	rules, err := rm.nl.RuleList(netlink.FAMILY_V4)
	if err != nil {
		return false, fmt.Errorf("failed to list rules: %w", err)
	}
	for _, r := range rules {
		if r.Table == tableID {
			// Check if Table is already assigned to an existing NIC.
			if r.Src != nil && r.Src.IP.Equal(targetIP) {
				klog.V(4).Infof("Table %d is already assigned to IP %s. Reusing it.", tableID, targetIP.String())

				return true, nil
			}
			klog.V(5).Infof("Table %d is occupied by a rule for a different Src IP.", tableID)

			return false, nil
		}
	}

	// Check if any Routes exist in this table
	filter := &netlink.Route{Table: tableID}
	mask := netlink.RT_FILTER_TABLE
	routes, err := rm.nl.RouteListFiltered(netlink.FAMILY_V4, filter, mask)
	if err != nil {
		return false, fmt.Errorf("failed to list routes for table %d: %w", tableID, err)
	}

	if len(routes) > 0 {
		klog.V(5).Infof("Table %d is occupied by %d routes. Checking next table ID.", tableID, len(routes))

		return false, nil
	}

	return true, nil
}

// FindNextFreeTableID finds available Table ID for NIC confiugration.
func (rm *RouteManager) FindNextFreeTableID(startID int, nicIPAddr net.IP) (int, error) {
	for id := startID; id <= maxTableID; id++ {
		usable, err := rm.isTableFree(id, nicIPAddr)
		if err != nil {
			return 0, err
		}
		if usable {
			klog.Infof("Found free routing table ID: %d", id)

			return id, nil
		}
	}

	return 0, fmt.Errorf("no free routing tables found between %d and %d", startID, maxTableID)
}

func (rm *RouteManager) writeSysctl(path, val string) error {
	if rm.sysctlWriter != nil {
		return rm.sysctlWriter(path, val)
	}

	return os.WriteFile(path, []byte(val), 0o644)
}

// IsRDMASupportedNode checks if the node has the RDMA node label enabled and verifies that
// irdma/idpf RDMA network interfaces exist on the node.
func (rm *RouteManager) IsRDMASupportedNode(ctx context.Context, nodeID string) (bool, error) {
	node, err := rm.nc.GetNodeWithRetry(ctx, nodeID)
	if err != nil {
		return false, fmt.Errorf("failed to get node %s: %w", nodeID, err)
	}
	if node == nil {
		return false, fmt.Errorf("node %s is nil", nodeID)
	}
	labels := node.GetLabels()
	rdmaVal := strings.ToLower(strings.TrimSpace(labels[RDMANodeLabel]))
	if rdmaVal != "true" {
		klog.V(4).Infof("Node %s does not have RDMA label %s enabled (value: %q)", nodeID, RDMANodeLabel, rdmaVal)
		return false, nil
	}

	irdmaNICs, err := rm.nl.GetIRDMANICs()
	if err != nil {
		return false, fmt.Errorf("failed to discover irdma interfaces for node %s: %w", nodeID, err)
	}
	if len(irdmaNICs) == 0 {
		return false, fmt.Errorf("node %s has label %s=true, but no iRDMA interfaces were detected", nodeID, RDMANodeLabel)
	}

	klog.Infof("Node %s matched RDMA conditions (RDMA label: %s=%s, discovered %d irdma interfaces: %v)",
		nodeID, RDMANodeLabel, rdmaVal, len(irdmaNICs), irdmaNICs)
	return true, nil
}

// GetStandardAndIRDMANICs discovers standard NICs (e.g. eth0, eth1) and irdma/idpf RDMA NICs (e.g. rdma0..rdma7).
func (rm *RouteManager) GetStandardAndIRDMANICs() ([]string, []string, error) {
	// Discover irdma/idpf RDMA NICs (e.g. rdma0..rdma7)
	irdmaNICs, err := rm.nl.GetIRDMANICs()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get irdma NICs: %w", err)
	}
	sort.Strings(irdmaNICs)

	var stdNICs []string
	if rm.mc != nil {
		// When metadata client is available, rm.GetStandardNICs() validates against the primary VPC
		// so all standard NICs in the primary VPC (e.g. eth0, eth1) are returned.
		stdNICs, err = rm.GetStandardNICs()
		if err != nil {
			return nil, nil, fmt.Errorf("failed to get validated standard NICs: %w", err)
		}
	} else {
		// Fallback when metadata client is nil: query raw standard NICs directly
		stdNICs, err = rm.nl.GetStandardNICs()
		if err != nil {
			return nil, nil, fmt.Errorf("failed to get standard NICs: %w", err)
		}
		sort.Strings(stdNICs)
	}

	klog.Infof("Discovered network devices for symmetric routing (standard: %v, irdma/idpf: %v)",
		stdNICs, irdmaNICs)

	return stdNICs, irdmaNICs, nil
}

// ConfigureRDMASymmetricRouting configures symmetric policy routing and loose reverse-path filtering
// on RDMA supported nodes (A5X) for secondary RDMA interfaces.
func (rm *RouteManager) ConfigureRDMASymmetricRouting() error {
	gvnics, irdmaNICs, err := rm.GetStandardAndIRDMANICs()
	if err != nil {
		return fmt.Errorf("failed to discover network devices: %w", err)
	}
	allDevices := append(gvnics, irdmaNICs...)
	if len(allDevices) == 0 {
		klog.Warning("No network devices found for RDMA symmetric routing")
		return nil
	}

	// 1. Global and per-interface rp_filter settings (loose mode = 2)
	globalRPFilters := []string{
		"/proc/sys/net/ipv4/conf/all/rp_filter",
		"/proc/sys/net/ipv4/conf/default/rp_filter",
	}
	for _, path := range globalRPFilters {
		if err := rm.writeSysctl(path, "2\n"); err != nil {
			return fmt.Errorf("failed to set rp_filter on %s: %w", path, err)
		}
	}
	for _, dev := range allDevices {
		devRPFilter := fmt.Sprintf("/proc/sys/net/ipv4/conf/%s/rp_filter", dev)
		if err := rm.writeSysctl(devRPFilter, "2\n"); err != nil {
			return fmt.Errorf("failed to set rp_filter on interface %s: %w", dev, err)
		}
	}

	if len(irdmaNICs) == 0 {
		klog.Infof("No secondary RDMA interfaces found for dedicated routing tables")
		return nil
	}

	// 2. Configure dedicated routing tables and policy rules for secondary RDMA NICs
	var errs []error
	for i, dev := range irdmaNICs {
		link, err := rm.nl.LinkByName(dev)
		if err != nil {
			errs = append(errs, fmt.Errorf("failed to get link for %s: %w", dev, err))
			continue
		}

		addrs, err := rm.nl.AddrList(link, netlink.FAMILY_V4)
		if err != nil {
			errs = append(errs, fmt.Errorf("failed to list IPv4 addresses for %s: %w", dev, err))
			continue
		}
		if len(addrs) == 0 {
			errs = append(errs, fmt.Errorf("interface %s has no IPv4 address yet", dev))
			continue
		}

		if addrs[0].IPNet == nil || addrs[0].IP == nil {
			errs = append(errs, fmt.Errorf("interface %s has nil IPNet or IP", dev))
			continue
		}

		ipAddr := addrs[0].IP.To4()
		if ipAddr == nil {
			errs = append(errs, fmt.Errorf("interface %s has invalid IPv4 address %v", dev, addrs[0].IP))
			continue
		}

		table, err := rm.FindNextFreeTableID(101, ipAddr)
		if err != nil {
			errs = append(errs, fmt.Errorf("failed to find free routing table for %s: %w", dev, err))
			continue
		}

		// Per-device routes in dedicated table for this RDMA NIC
		routes, err := rm.nl.RouteList(link, netlink.FAMILY_V4)
		if err != nil {
			errs = append(errs, fmt.Errorf("failed to list routes for %s: %w", dev, err))
			continue
		}

		var gateway net.IP
		var subnetNet *net.IPNet
		for _, r := range routes {
			if r.Gw != nil && gateway == nil {
				gateway = r.Gw
			}
			if (r.Table == unix.RT_TABLE_MAIN || r.Table == 0) && r.Dst != nil && r.Dst.Mask != nil && subnetNet == nil {
				if r.Dst.Contains(ipAddr) {
					if ones, bits := r.Dst.Mask.Size(); ones > 0 && ones < bits {
						subnetNet = r.Dst
					}
				}
			}
		}
		if gateway == nil {
			errs = append(errs, fmt.Errorf("could not find gateway for %s", dev))
			continue
		}
		if subnetNet == nil {
			mask := addrs[0].Mask
			subnet := ipAddr.Mask(mask)
			subnetNet = &net.IPNet{IP: subnet, Mask: mask}
		}

		// Route local subnet via gateway onlink (prevents GCP ARP FAILED)
		subnetRoute := &netlink.Route{
			LinkIndex: link.Attrs().Index,
			Dst:       subnetNet,
			Gw:        gateway,
			Src:       ipAddr,
			Table:     table,
			Flags:     int(netlink.FLAG_ONLINK),
		}
		if err := rm.nl.RouteReplace(subnetRoute); err != nil {
			errs = append(errs, fmt.Errorf("failed to replace subnet route for %s in table %d: %w", dev, table, err))
			continue
		}

		// Default route out of this NIC's gateway for this table
		defaultRoute := &netlink.Route{
			LinkIndex: link.Attrs().Index,
			Dst:       nil,
			Gw:        gateway,
			Src:       ipAddr,
			Table:     table,
			Flags:     int(netlink.FLAG_ONLINK),
		}
		if err := rm.nl.RouteReplace(defaultRoute); err != nil {
			errs = append(errs, fmt.Errorf("failed to replace default route for %s in table %d: %w", dev, table, err))
			continue
		}

		// 3. Policy routing rules
		srcRule := netlink.NewRule()
		srcRule.Src = &net.IPNet{IP: ipAddr, Mask: net.CIDRMask(32, 32)}
		srcRule.Table = table
		srcRule.Priority = 1001 + i
		if err := rm.ensureRule(srcRule); err != nil {
			errs = append(errs, fmt.Errorf("failed to configure src rule for %s (table %d): %w", dev, table, err))
			continue
		}

		oifRule := netlink.NewRule()
		oifRule.OifName = dev
		oifRule.Table = table
		oifRule.Priority = 2001 + i
		if err := rm.ensureRule(oifRule); err != nil {
			errs = append(errs, fmt.Errorf("failed to configure oif rule for %s (table %d): %w", dev, table, err))
			continue
		}
	}

	return errors.Join(errs...)
}

func (rm *RouteManager) ensureRule(rule *netlink.Rule) error {
	existingRules, err := rm.nl.RuleList(netlink.FAMILY_V4)
	if err != nil {
		return fmt.Errorf("failed to list rules: %w", err)
	}

	for _, r := range existingRules {
		if r.Table == rule.Table && r.Priority == rule.Priority {
			if rule.Src != nil && r.Src != nil && r.Src.String() == rule.Src.String() {
				klog.V(4).Infof("Policy rule for table %d src %s already exists, skipping addition", rule.Table, rule.Src.String())
				return nil
			}
			if rule.OifName != "" && r.OifName == rule.OifName {
				klog.V(4).Infof("Policy rule for table %d oif %s already exists, skipping addition", rule.Table, rule.OifName)
				return nil
			}
			// Stale rule exists at this Table and Priority with a different selector; delete it.
			staleRule := r
			if err := rm.nl.RuleDel(&staleRule); err != nil && !os.IsNotExist(err) {
				klog.Warningf("Failed to delete stale policy rule for table %d priority %d: %v", r.Table, r.Priority, err)
			}
		}
	}

	if err := rm.nl.RuleAdd(rule); err != nil {
		if os.IsExist(err) {
			return nil
		}
		return fmt.Errorf("failed to add rule: %w", err)
	}
	return nil
}
