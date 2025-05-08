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

package lustre

import (
	"context"

	"cloud.google.com/go/longrunning/autogen/longrunningpb"
	"google.golang.org/api/googleapi"
	"k8s.io/klog/v2"
)

const (
	activeState   = "ACTIVE"
	creatingState = "CREATING"
	unknownState  = "STATE_UNSPECIFIED"

	defaultIP = "127.0.0.1"
	project   = "test-project"
	zone      = "us-central1-a"
	network   = "projects/test-project/global/networks/default"
	minCapGiB = 18000
)

type fakeServiceManager struct {
	createdInstances map[string]*ServiceInstance
}

var _ Service = &fakeServiceManager{}

func (sm *fakeServiceManager) CreateInstance(_ context.Context, obj *ServiceInstance) (*ServiceInstance, error) {
	instance := &ServiceInstance{
		Project:                  obj.Project,
		Location:                 obj.Location,
		Name:                     obj.Name,
		Filesystem:               obj.Filesystem,
		Network:                  obj.Network,
		IP:                       defaultIP,
		Description:              obj.Description,
		State:                    activeState,
		Labels:                   obj.Labels,
		CapacityGib:              obj.CapacityGib,
		PerUnitStorageThroughput: obj.PerUnitStorageThroughput,
	}
	sm.createdInstances[obj.Name] = instance

	return instance, nil
}

func (sm *fakeServiceManager) DeleteInstance(_ context.Context, _ *ServiceInstance) error {
	return nil
}

func (sm *fakeServiceManager) GetInstance(_ context.Context, obj *ServiceInstance) (*ServiceInstance, error) {
	instance, exists := sm.createdInstances[obj.Name]
	if exists {
		klog.Infof("%+v", instance)

		return instance, nil
	}

	return nil, &googleapi.Error{Code: 404}
}

func (sm *fakeServiceManager) ListInstance(_ context.Context, _ *ListFilter) ([]*ServiceInstance, error) {
	instances := make([]*ServiceInstance, 0, len(sm.createdInstances))
	for _, v := range sm.createdInstances {
		instances = append(instances, v)
	}

	return instances, nil
}

func (sm *fakeServiceManager) GetCreateInstanceOp(_ context.Context, _ *ServiceInstance) (*longrunningpb.Operation, error) {
	return nil, nil
}

func (sm *fakeServiceManager) ListLocations(_ context.Context, _ *ListFilter) ([]string, error) {
	return nil, nil
}

func NewFakeCloud() (*Cloud, error) {
	existingInstances := map[string]*ServiceInstance{
		"existing-instance": {
			Project:                  project,
			Location:                 zone,
			Name:                     "existing-instance",
			Filesystem:               "existing",
			Network:                  network,
			IP:                       "192.168.1.1",
			State:                    activeState,
			CapacityGib:              minCapGiB,
			PerUnitStorageThroughput: "1000",
		},
		"creating-instance": {
			Project:                  project,
			Location:                 zone,
			Name:                     "creating-instance",
			Filesystem:               "creating",
			Network:                  network,
			IP:                       "192.168.1.2",
			State:                    creatingState,
			CapacityGib:              minCapGiB,
			PerUnitStorageThroughput: "1000",
		},
		"unknown-instance": {
			Project:                  project,
			Location:                 zone,
			Name:                     "unknown-instance",
			Filesystem:               "unknown",
			Network:                  network,
			IP:                       "192.168.1.3",
			State:                    unknownState,
			CapacityGib:              minCapGiB,
			PerUnitStorageThroughput: "1000",
		},
	}

	return &Cloud{
		LustreService: &fakeServiceManager{createdInstances: existingInstances},
		Project:       project,
		Zone:          zone,
	}, nil
}
