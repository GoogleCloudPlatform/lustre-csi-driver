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
	"strings"

	"github.com/GoogleCloudPlatform/lustre-csi-driver/pkg/k8sclient"
	"github.com/vishvananda/netlink"
	"k8s.io/klog/v2"
)

const (
	multiRailLabel = "lustre.csi.storage.gke.io/multi-rail"
	// max user specified table ID is 252. 253, 254, and 255 are reserved (default, main, local).
	maxTableID = 252
)

// GetGvnicNames gets all available nics on the node.
func GetGvnicNames() ([]string, error) {
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

	klog.V(4).Infof("Got nics: %v", ethNics)

	return ethNics, nil
}

func configureRoutesForNic(nicName string, nicIPAddr net.IP, instanceIPAddr string, tableID int) error {
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
	// This is the IP of the Lustre instance which is passed in through volumeContext.
	instanceIPAddr += "/32"
	_, dst, _ := net.ParseCIDR(instanceIPAddr)
	route := &netlink.Route{
		LinkIndex: link.Attrs().Index,
		Dst:       dst, // lustre ip
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
	rule.Src = &net.IPNet{IP: nicIPAddr, Mask: net.CIDRMask(32, 32)} // /32 mask for a single IP
	if err := netlink.RuleAdd(rule); err != nil {
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
func ConfigureRoute(nicName string, instanceIP string, tableID int) error {
	nicIPAddr, err := GetNicIPAddr(nicName)
	if err != nil {
		return err
	}

	if err := configureRoutesForNic(nicName, nicIPAddr, instanceIP, tableID); err != nil {
		return fmt.Errorf("failed to configure routes for %s: %w", nicName, err)
	}

	return nil
}

// GetNicIPAddr gets primary IP Address of NIC.
func GetNicIPAddr(nicName string) (net.IP, error) {
	link, err := netlink.LinkByName(nicName)
	if err != nil {
		return nil, fmt.Errorf("could not get link for %s: %w", nicName, err)
	}
	addrs, err := netlink.AddrList(link, netlink.FAMILY_V4)
	if err != nil || len(addrs) == 0 {
		return nil, fmt.Errorf("could not get address for %s: %w", nicName, err)
	}
	klog.Infof("Nic %v IP address are: %+v", nicName, addrs)

	// We capture primary IP address which is at index 0.
	return addrs[0].IP, nil
}

// IsMultiRailEnabled checks for Multi Nic feature via node label.
func IsMultiRailEnabled(ctx context.Context, nodeID string, nics []string) (bool, error) {
	klog.V(4).Infof("Checking if node label %s exists in %s", multiRailLabel, nodeID)
	if len(nics) <= 1 {
		klog.V(4).Infof("Node only has 1 nic or less available. Not suitable for Multi-Nic feature. current nics: %v", nics)

		return false, nil
	}
	node, err := k8sclient.GetNodeWithRetry(ctx, nodeID)
	if err != nil {
		return false, err
	}

	if val, found := node.GetLabels()[multiRailLabel]; found {
		isMultiNicEnabled, err := strconv.ParseBool(val)
		if err != nil {
			return false, err
		}
		klog.Infof("Node: %v, MultiNic Enabled: %v", nodeID, isMultiNicEnabled)

		return isMultiNicEnabled, nil
	}

	return false, nil
}

func isTableFree(tableID int, targetIP net.IP) (bool, error) {
	// Check if any active Policy Rules point to this table
	rules, err := netlink.RuleList(netlink.FAMILY_V4)
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
	routes, err := netlink.RouteListFiltered(netlink.FAMILY_V4, filter, mask)
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
func FindNextFreeTableID(startID int, nicIPAddr net.IP) (int, error) {
	for id := startID; id <= maxTableID; id++ {
		usable, err := isTableFree(id, nicIPAddr)
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
