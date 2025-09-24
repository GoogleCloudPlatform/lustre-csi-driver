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
	"fmt"
	"strings"

	"cloud.google.com/go/compute/metadata"
	"github.com/GoogleCloudPlatform/lustre-csi-driver/pkg/cloud_provider/clientset"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/klog/v2"
)

type Service interface {
	GetZone() string
	GetProject() string
	GetIdentityPool() string
	GetIdentityProvider() string
}
type metadataServiceManager struct {
	// Current zone the driver is running in.
	zone             string
	project          string
	identityPool     string
	identityProvider string
}

var _ Service = &metadataServiceManager{}

func NewMetadataService(ctx context.Context, identityPool, identityProvider string, clientset clientset.Interface) (Service, error) {
	zone, err := metadata.ZoneWithContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get current zone: %w", err)
	}
	projectID, err := metadata.ProjectIDWithContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get project: %w", err)
	}
	if identityPool == "" {
		klog.Infof("got empty identityPool, constructing the identityPool using projectID")
		identityPool = projectID + ".svc.id.goog"
	}
	if identityProvider == "" {
		klog.Infof("got empty identityProvider, constructing the identityProvider using the gke-metadata-server flags")
		ds, err := clientset.GetDaemonSet(context.TODO(), "kube-system", "gke-metadata-server")
		if err != nil {
			return nil, fmt.Errorf("failed to get gke-metadata-server DaemonSet spec: %w", err)
		}

		identityProvider = getIdentityProvider(ds)
	}

	return &metadataServiceManager{
		project:          projectID,
		zone:             zone,
		identityPool:     identityPool,
		identityProvider: identityProvider,
	}, nil
}

func (manager *metadataServiceManager) GetZone() string {
	return manager.zone
}

func (manager *metadataServiceManager) GetProject() string {
	return manager.project
}

func (manager *metadataServiceManager) GetIdentityPool() string {
	return manager.identityPool
}

func (manager *metadataServiceManager) GetIdentityProvider() string {
	return manager.identityProvider
}

func getIdentityProvider(ds *appsv1.DaemonSet) string {
	for _, c := range ds.Spec.Template.Spec.Containers[0].Command {
		l := strings.Split(c, "=")
		if len(l) == 2 && l[0] == "--identity-provider" {
			return l[1]
		}
	}

	return ""
}
