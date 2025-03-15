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
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"text/template"
)

type driverConfig struct {
	StorageClassFile     string
	StorageClass         string
	Capabilities         []string
	SupportedFsType      []string
	MinimumVolumeSize    string
	NumAllowedTopologies int
	Timeouts             map[string]string
}

const (
	testConfigDir      = "test/k8s-integration/config"
	configTemplateFile = "test-config-template.in"
	configFile         = "test-config.yaml"

	// configurable timeouts for the k8s e2e testsuites.
	podStartTimeout       = "600s"
	claimProvisionTimeout = "3600s"
	pvDeleteTimeout       = "1800s"

	// These are keys for the configurable timeout map.
	podStartTimeoutKey       = "PodStart"
	claimProvisionTimeoutKey = "ClaimProvision"
	pvDeleteTimeoutKey       = "PVDelete"
)

// generateDriverConfigFile loads a testdriver config template and creates a file
// with the test-specific configuration.
func generateDriverConfigFile(testParams *testParameters, storageClassFile string) (string, error) {
	// Load template
	t, err := template.ParseFiles(filepath.Join(testParams.pkgDir, testConfigDir, configTemplateFile))
	if err != nil {
		return "", err
	}

	// Create destination
	configFilePath := filepath.Join(testParams.pkgDir, testConfigDir, configFile)
	f, err := os.Create(configFilePath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	w := bufio.NewWriter(f)
	defer w.Flush()

	// Fill in template parameters. Capabilities can be found here:
	// https://github.com/kubernetes/kubernetes/blob/b717be8269a4f381ab6c23e711e8924bc1f64c93/test/e2e/storage/testsuites/testdriver.go#L136

	caps := []string{
		"persistence",
		"exec",
		"RWX",
		"multipods",
	}

	// Lustre instance takes in the order of minutes to be provisioned, and with dynamic provisioning (WaitForFirstCustomer policy),
	// some e2e tests need a longer pod start timeout.
	timeouts := map[string]string{
		claimProvisionTimeoutKey: claimProvisionTimeout,
		podStartTimeoutKey:       podStartTimeout,
		pvDeleteTimeoutKey:       pvDeleteTimeout,
	}

	params := driverConfig{
		StorageClassFile:  filepath.Join(testParams.pkgDir, testConfigDir, storageClassFile),
		StorageClass:      storageClassFile[:strings.LastIndex(storageClassFile, ".")],
		Capabilities:      caps,
		MinimumVolumeSize: "1Ti",
		Timeouts:          timeouts,
	}

	// Write config file
	err = t.Execute(w, params)
	if err != nil {
		return "", err
	}

	return configFilePath, nil
}
