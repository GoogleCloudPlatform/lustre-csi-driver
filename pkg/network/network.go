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
	"fmt"
	"strings"

	"github.com/vishvananda/netlink"
	"k8s.io/klog/v2"
)

const (
	multiRailLabel = "lustre.csi.storage.gke.io/multi-rail"
)

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

func ConfigureRoute() error {
	// TODO(halimsam): Add Route configuration logic for Nics.
	return nil
}

func IsMultiRailEnabled(nodeID string) (bool, error) {
	klog.V(4).Infof("Checking if node label %s exists in %s", multiRailLabel, nodeID)
	// TODO(halimsam): Add Multi Rail Logic check here.
	return false, nil
}
