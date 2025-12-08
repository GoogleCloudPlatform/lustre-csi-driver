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

package metadata

import (
	"context"
	"encoding/json"
	"fmt"

	"cloud.google.com/go/compute/metadata"
)

type NetworkInterface struct {
	Network string `json:"network"`
	Mac     string `json:"mac"`
}

type Service interface {
	GetZone() string
	GetProject() string
	GetNetworkInterfaces() ([]NetworkInterface, error)
}

type metadataServiceManager struct {
	// Current zone the driver is running in.
	zone    string
	project string
	nics    []NetworkInterface
}

var _ Service = &metadataServiceManager{}

func NewMetadataService(ctx context.Context) (Service, error) {
	zone, err := metadata.ZoneWithContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get current zone: %w", err)
	}
	projectID, err := metadata.ProjectIDWithContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get project: %w", err)
	}

	// Fetch network interfaces recursively
	nicsJSON, err := metadata.GetWithContext(ctx, "instance/network-interfaces/?recursive=true")
	if err != nil {
		return nil, fmt.Errorf("failed to get network interfaces: %w", err)
	}

	var nics []NetworkInterface
	if err := json.Unmarshal([]byte(nicsJSON), &nics); err != nil {
		return nil, fmt.Errorf("failed to unmarshal network interfaces: %w", err)
	}

	return &metadataServiceManager{
		zone:    zone,
		project: projectID,
		nics:    nics,
	}, nil
}

func (manager *metadataServiceManager) GetZone() string {
	return manager.zone
}

func (manager *metadataServiceManager) GetProject() string {
	return manager.project
}

func (manager *metadataServiceManager) GetNetworkInterfaces() ([]NetworkInterface, error) {
	return manager.nics, nil
}
