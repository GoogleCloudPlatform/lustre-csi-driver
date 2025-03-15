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

package metrics

import (
	"fmt"
	"net/http"
	"os"

	"k8s.io/component-base/metrics"
	"k8s.io/klog/v2"
)

const (
	// envGKELustreCSIVersion is an environment variable set in the Lustre CSI driver controller manifest
	// with the current version of the GKE component.
	envGKELustreCSIVersion = "GKE_LUSTRECSI_VERSION"
)

// This metric is exposed only from the controller driver component when GKE_LUSTRECSI_VERSION env variable is set.
var gkeComponentVersion = metrics.NewGaugeVec(&metrics.GaugeOpts{
	Name: "component_version",
	Help: "Metric to expose the version of the LustreCSI GKE component.",
}, []string{"component_version"})

type Manager struct {
	registry metrics.KubeRegistry
}

func NewMetricsManager() Manager {
	mm := Manager{
		registry: metrics.NewKubeRegistry(),
	}

	return mm
}

func (mm *Manager) GetRegistry() metrics.KubeRegistry {
	return mm.registry
}

func (mm *Manager) registerComponentVersionMetric() {
	mm.registry.MustRegister(gkeComponentVersion)
}

func (mm *Manager) recordComponentVersionMetric() error {
	v := getEnvVar(envGKELustreCSIVersion)
	if v == "" {
		klog.V(2).Info("Skip emitting component version metric")

		return fmt.Errorf("failed to register GKE component version metric, env variable %v not defined", envGKELustreCSIVersion)
	}

	gkeComponentVersion.WithLabelValues(v).Set(1.0)
	klog.Infof("Recorded GKE component version: %v", v)

	return nil
}

func (mm *Manager) EmitGKEComponentVersion() error {
	mm.registerComponentVersionMetric()
	if err := mm.recordComponentVersionMetric(); err != nil {
		return err
	}

	return nil
}

// Server represents any type that could serve HTTP requests for the metrics
// endpoint.
type Server interface {
	Handle(pattern string, handler http.Handler)
}

// RegisterToServer registers an HTTP handler for this metrics manager to the
// given server at the specified address/path.
func (mm *Manager) registerToServer(s Server, metricsPath string) {
	s.Handle(metricsPath, metrics.HandlerFor(
		mm.GetRegistry(),
		metrics.HandlerOpts{
			ErrorHandling: metrics.ContinueOnError,
		}))
}

// InitializeHTTPHandler sets up a server and creates a handler for metrics.
func (mm *Manager) InitializeHTTPHandler(address, path string) {
	mux := http.NewServeMux()
	mm.registerToServer(mux, path)
	go func() {
		klog.Infof("Metric server listening at %q", address)
		if err := http.ListenAndServe(address, mux); err != nil {
			klog.Fatalf("Failed to start metric server at specified address (%q) and path (%q): %s", address, path, err.Error())
		}
	}()
}

func getEnvVar(envVarName string) string {
	v, ok := os.LookupEnv(envVarName)
	if !ok {
		klog.Warningf("env %q not set", envVarName)

		return ""
	}

	return v
}

func IsGKEComponentVersionAvailable() bool {
	return getEnvVar(envGKELustreCSIVersion) != ""
}
