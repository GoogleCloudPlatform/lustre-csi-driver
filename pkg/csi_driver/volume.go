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

package driver

import (
	"fmt"

	"github.com/GoogleCloudPlatform/lustre-csi-driver/pkg/cloud_provider/lustre"
	"github.com/GoogleCloudPlatform/lustre-csi-driver/pkg/util"
)

func volumeIDToInstance(volumeID string) (*lustre.ServiceInstance, error) {
	projectID, location, instanceName, err := util.ParseVolumeID(volumeID)
	if err != nil {
		return nil, err
	}

	return &lustre.ServiceInstance{
		Name:     instanceName,
		Location: location,
		Project:  projectID,
	}, nil
}

func instanceToVolumeID(instance *lustre.ServiceInstance) string {
	return fmt.Sprintf("%s/%s/%s",
		instance.Project,
		instance.Location,
		instance.Name)
}
