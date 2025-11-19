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
	"fmt"
	"os/exec"

	"k8s.io/klog/v2"
)

const (
	// Networking constants.
	ipRangeName = "lustre-range"
)

func setupNetwork(project string) error {
	cmd := exec.Command("gcloud", "services", "enable", "servicenetworking.googleapis.com", "--project="+project)
	if err := runCommand("Enabling service networkiing API", cmd); err != nil {
		return fmt.Errorf("failed to enable service networking API: %w", err)
	}

	// Create network if it doesn't exist.
	cmd = exec.Command("gcloud", "compute", "networks", "describe", *clusterNewtwork, "--project="+project)
	if err := runCommand("Checking if VPC network exists", cmd); err != nil {
		klog.Infof("VPC network %q not found, creating it.", *clusterNewtwork)
		cmd = exec.Command("gcloud", "compute", "networks", "create", *clusterNewtwork, "--subnet-mode=auto", "--mtu=8896", "--project="+project)
		if err := runCommand("Creating VPC network", cmd); err != nil {
			return fmt.Errorf("failed to create VPC network: %w", err)
		}
	}

	// Create IP range if it doesn't exist.
	cmd = exec.Command("gcloud", "compute", "addresses", "describe", ipRangeName, "--global", "--project="+project)
	if err := runCommand("Checking if IP range exists", cmd); err != nil {
		klog.Infof("IP range %q not found, creating it.", ipRangeName)
		cmd = exec.Command("gcloud", "compute", "addresses", "create", ipRangeName, "--global", "--purpose=VPC_PEERING", "--prefix-length=16", "--description=Lustre VPC Peering", "--network="+*clusterNewtwork, "--project="+project)
		if err := runCommand("Creating IP range", cmd); err != nil {
			return fmt.Errorf("failed to reserve IP range: %w", err)
		}
	}

	// Connect VPC peering.
	// gcloud services vpc-peerings connect is idempotent. It will update the peering if it already exists.
	cmd = exec.Command("gcloud", "services", "vpc-peerings", "connect",
		"--network="+*clusterNewtwork,
		"--project="+project,
		"--ranges="+ipRangeName,
		"--service=servicenetworking.googleapis.com")
	if err := runCommand("Connecting VPC peering", cmd); err != nil {
		return fmt.Errorf("failed to reserve IP range: %w", err)
	}

	return nil
}
