package main

import (
	"fmt"
	"net"
	"os/exec"
	"strings"

	"github.com/vishvananda/netlink"
	"k8s.io/klog/v2"
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

func configureRoutesForNic(nicName string, ipAddr net.IP, tableId int) error {
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
		Table:     tableId,
	}
	if err := netlink.RouteAdd(route); err != nil {
		return fmt.Errorf("failed to add route for %s: %w", nicName, err)
	}
	klog.Infof("Successfully added route for %s", nicName)

	// Define and add the rule
	rule := netlink.NewRule()
	rule.Table = tableId
	rule.Src = &net.IPNet{IP: ipAddr, Mask: net.CIDRMask(32, 32)} // /32 mask for a single IP
	if err := netlink.RuleAdd(rule); err != nil {
		return fmt.Errorf("failed to add rule for %s: %w", nicName, err)
	}
	klog.Infof("Successfully added rule for %s", nicName)

	return nil
}

func main() {
	nics, err := getGvnicNames()
	if err != nil {
		klog.Fatalf("Error getting NIC names: %v", err)
	}

	if len(nics) == 0 {
		klog.Infof("No NICs with 'eth' prefix found.")
	}

	lnetArg := fmt.Sprintf(`lnet.networks="tcp0(%s)"`, strings.Join(nics, ","))

	cmd := exec.Command("/usr/bin/cos-dkms", "install", "lustre-client-drivers",
		"--gcs-bucket=cos-default",
		"--latest",
		"-w", "0",
		"--kernelmodulestree=/host_modules",
		"--module-arg="+lnetArg,
		"--lsb-release-path=/host_etc/lsb-release",
		"--insert-on-install",
		"--logtostderr")

	klog.Infof("Executing command: %s", cmd.String())

	output, err := cmd.CombinedOutput()
	if err != nil {
		klog.Fatalf("Command execution failed: %v\nOutput:\n%s", err, string(output))
	}

	klog.Infof("Command output:\n%s\n", string(output))

	// Configure routes
	klog.Info("Configuring routes using netlink...")
	nicName := "eth1"
	tableId := 100
	link, err := netlink.LinkByName(nicName)
	if err != nil {
		klog.Fatalf("Could not get link for %s: %v", nicName, err)
	}
	addrs, err := netlink.AddrList(link, netlink.FAMILY_V4)
	if err != nil || len(addrs) == 0 {
		klog.Fatalf("Could not get address for %s: %v", nicName, err)
	}
	klog.Infof("IP address are: %+v", addrs)
	ipAddr := addrs[0].IP

	if err := configureRoutesForNic(nicName, ipAddr, tableId); err != nil {
		klog.Fatalf("Failed to configure routes for %s: %v", nicName, err)
	}
}
