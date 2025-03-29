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
	"testing"

	csi "github.com/container-storage-interface/spec/lib/go/csi"
)

const (
	testDriver  = "test-driver"
	testVersion = "test-version"
)

func initTestIdentityServer(t *testing.T) csi.IdentityServer {
	t.Helper()

	return newIdentityServer(initTestDriver(t))
}

func TestGetPluginInfo(t *testing.T) {
	t.Parallel()
	s := initTestIdentityServer(t)

	resp, err := s.GetPluginInfo(t.Context(), nil)
	if err != nil {
		t.Fatalf("GetPluginInfo failed: %v", err)
	}

	if resp == nil {
		t.Fatalf("GetPluginInfo resp is nil")
	}

	if resp.GetName() != testDriver {
		t.Errorf("Got driver name %v, want %v", resp.GetName(), testDriver)
	}

	if resp.GetVendorVersion() != testVersion {
		t.Errorf("Got driver version %v, want %v", resp.GetName(), testVersion)
	}
}

func TestGetPluginCapabilities(t *testing.T) {
	t.Parallel()
	s := initTestIdentityServer(t)

	resp, err := s.GetPluginCapabilities(t.Context(), nil)
	if err != nil {
		t.Fatalf("GetPluginCapabilities failed: %v", err)
	}

	if resp == nil {
		t.Fatalf("GetPluginCapabilities resp is nil")
	}

	if len(resp.GetCapabilities()) != 2 {
		t.Fatalf("Got %d capabilities, want %d", len(resp.GetCapabilities()), 2)
	}

	if resp.Capabilities[0].Type == nil {
		t.Fatalf("Got nil capability type")
	}

	service := resp.GetCapabilities()[0].GetService()
	if service == nil {
		t.Fatalf("Got nil capability service")
	}

	if serviceType := service.GetType(); serviceType != csi.PluginCapability_Service_CONTROLLER_SERVICE {
		t.Fatalf("Got %v capability service, want %v", serviceType, csi.PluginCapability_Service_CONTROLLER_SERVICE)
	}
	service = resp.GetCapabilities()[1].GetService()
	if service == nil {
		t.Fatalf("Got nil capability service")
	}

	if serviceType := service.GetType(); serviceType != csi.PluginCapability_Service_VOLUME_ACCESSIBILITY_CONSTRAINTS {
		t.Fatalf("Got %v capability service, want %v", serviceType, csi.PluginCapability_Service_VOLUME_ACCESSIBILITY_CONSTRAINTS)
	}
}

func TestProbe(t *testing.T) {
	t.Parallel()
	s := initTestIdentityServer(t)

	resp, err := s.Probe(t.Context(), nil)
	if err != nil {
		t.Fatalf("Probe failed: %v", err)
	}

	if resp == nil {
		t.Fatalf("Probe resp is nil")
	}
}
