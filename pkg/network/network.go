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
	"fmt"
	"net"
	"os"
	"strconv"

	"github.com/GoogleCloudPlatform/lustre-csi-driver/pkg/k8sclient"
	"github.com/safchain/ethtool"
	"github.com/vishvananda/netlink"
	v1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
)

const (
	multiRailLabel = "lustre.csi.storage.gke.io/multi-rail"
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
	RuleList(family int) ([]netlink.Rule, error)
	RouteListFiltered(family int, filter *netlink.Route, mask uint64) ([]netlink.Route, error)
	GetGvnicNames() ([]string, error)
}

type nodeClient interface {
	GetNodeWithRetry(ctx context.Context, nodeName string) (*v1.Node, error)
}

// RouteManager provides methods for network configuration using the netlinker interface.
type RouteManager struct {
	nl netlinker
	nc nodeClient
}

// realNetlink is the real implementation of netlinker,
// which calls the actual vishvananda/netlink functions.
type realNetlink struct{}

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
func (r *realNetlink) RuleList(family int) ([]netlink.Rule, error) {
	return netlink.RuleList(family)
}

func (r *realNetlink) RouteListFiltered(family int, filter *netlink.Route, mask uint64) ([]netlink.Route, error) {
	return netlink.RouteListFiltered(family, filter, mask)
}

func (r *realNetlink) GetGvnicNames() ([]string, error) {
	links, err := netlink.LinkList()
	if err != nil {
		return nil, fmt.Errorf("failed to list network interfaces: %w", err)
	}

	eth, err := ethtool.NewEthtool()
	if err != nil {
		return nil, fmt.Errorf("failed to create ethtool: %w", err)
	}
	defer eth.Close()

	var ethNics []string
	for _, link := range links {
		name := link.Attrs().Name
		driver, err := eth.DriverName(name)
		if err != nil {
			klog.Warningf("Failed to get driver for interface %s: %v", name, err)

			continue
		}

		if driver == "gve" || driver == "virtio_net" {
			klog.V(4).Infof("Found valid NIC %s with driver %s", name, driver)
			ethNics = append(ethNics, name)
		} else {
			// We only care about standard NICs (gVNIC or VirtIO-Net) for Lustre traffic.
			// RDMA NICs (like irdma, mlx5_core) should be excluded as they are handled separately
			// and might have names like ethN or gpu0rdma0.
			klog.Infof("Skipping interface %s with driver %s", name, driver)
		}
	}

	klog.V(4).Infof("Found %d valid standard NICs: %v", len(ethNics), ethNics)

	return ethNics, nil
}

// NewNetlink creates an instance of netlinker.
func NewNetlink() netlinker {
	return &realNetlink{}
}

func NewK8sClient() nodeClient {
	return &k8sNodeClient{}
}

func Manager(nl netlinker, nc nodeClient) *RouteManager {
	return &RouteManager{nl: nl, nc: nc}
}

// GetGvnicNames gets all available nics on the node.
func (rm *RouteManager) GetGvnicNames() ([]string, error) {
	return rm.nl.GetGvnicNames()
}

func (rm *RouteManager) configureRoutesForNic(nicName string, nicIPAddr net.IP, instanceIPAddr string, tableID int) error {
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
	// This is the IP of the Lustre instance which is passed in through volumeContext.
	instanceIPAddr += "/32"
	_, dst, _ := net.ParseCIDR(instanceIPAddr)
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

	// Define and add the rule
	rule := netlink.NewRule()
	rule.Table = tableID
	rule.Src = &net.IPNet{IP: nicIPAddr, Mask: net.CIDRMask(32, 32)} // /32 mask for a single IP
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
	nicIPAddr, err := rm.GetNicIPAddr(nicName)
	if err != nil {
		return err
	}

	if err := rm.configureRoutesForNic(nicName, nicIPAddr, instanceIP, tableID); err != nil {
		return fmt.Errorf("failed to configure routes for %s: %w", nicName, err)
	}

	return nil
}

// GetNicIPAddr gets primary IP Address of NIC.
func (rm *RouteManager) GetNicIPAddr(nicName string) (net.IP, error) {
	link, err := rm.nl.LinkByName(nicName)
	if err != nil {
		return nil, fmt.Errorf("could not get link for %s: %w", nicName, err)
	}
	addrs, err := rm.nl.AddrList(link, netlink.FAMILY_V4)
	if err != nil || len(addrs) == 0 {
		return nil, fmt.Errorf("could not get address for %s: %w", nicName, err)
	}
	klog.Infof("Nic %v IP address are: %+v", nicName, addrs)

	// We capture primary IP address which is at index 0.
	return addrs[0].IP, nil
}

// CheckDisableMultiNic checks for Multi Nic feature via node label and cluster configuration.
// return true if disabled. return false if multi nic is enabled.
// if node label is specified, then it overrides cluster configuration for multi nic setup.
func (rm *RouteManager) CheckDisableMultiNic(ctx context.Context, nodeID string, nics []string, disableMultiNic bool) (bool, error) {
	klog.V(4).Infof("Checking if node label %s exists in %s", multiRailLabel, nodeID)
	if len(nics) <= 1 {
		klog.V(4).Infof("Node only has 1 nic or less available. Not suitable for Multi-Nic feature. current nics: %v", nics)

		return true, nil
	}
	node, err := rm.nc.GetNodeWithRetry(ctx, nodeID)
	if err != nil {
		return false, err
	}

	if val, found := node.GetLabels()[multiRailLabel]; found {
		isMultiNicEnabled, err := strconv.ParseBool(val)
		if err != nil {
			return false, err
		}
		klog.Infof("Node: %v, Disable MultiNic: %v", nodeID, isMultiNicEnabled)

		// we look for inverse since node label specifies if Multi nic is enabled or not.
		// Ex: If Multi-Nic is enabled, then we return false since we don't want to disable it.
		return !isMultiNicEnabled, nil
	}

	// if label not found, then default will be whatever is passed from cluster configuration.
	return disableMultiNic, nil
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
