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
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/runtime"
	apimachineryversion "k8s.io/apimachinery/pkg/version"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
)

func gkeLocationArgs(gceZone, gceRegion string) (locationArg, locationVal string, err error) {
	switch {
	case len(gceZone) > 0:
		locationArg = "--zone"
		locationVal = gceZone
	case len(gceRegion) > 0:
		locationArg = "--region"
		locationVal = gceRegion
	default:
		return "", "", errors.New("zone and region unspecified")
	}

	return
}

func getKubeClusterVersion() (string, error) {
	out, err := exec.Command("kubectl", "version", "-o=json").Output()
	klog.Infof("Output of kubectl version is: \n %s", string(out))
	if err != nil {
		return "", fmt.Errorf("failed to obtain cluster version, error: %w", err)
	}
	type version struct {
		ClientVersion *apimachineryversion.Info `json:"clientVersion,omitempty" yaml:"clientVersion,omitempty"`
		ServerVersion *apimachineryversion.Info `json:"serverVersion,omitempty" yaml:"serverVersion,omitempty"`
	}

	var v version
	err = json.Unmarshal(out, &v)
	if err != nil {
		return "", fmt.Errorf("failed to parse kubectl version output, error: %w", err)
	}

	return v.ServerVersion.GitVersion, nil
}

func mustGetKubeClusterVersion() string {
	ver, err := getKubeClusterVersion()
	if err != nil {
		klog.Fatalf("Error: %v", err)
	}

	return ver
}

// getKubeConfig returns the full path to the
// kubeconfig file set in $KUBECONFIG env.
// If unset, then it defaults to $HOME/.kube/config.
func getKubeConfig() (string, error) {
	config, ok := os.LookupEnv("KUBECONFIG")
	if ok {
		return config, nil
	}
	homeDir, ok := os.LookupEnv("HOME")
	if !ok {
		return "", errors.New("HOME env not set")
	}

	return filepath.Join(homeDir, ".kube/config"), nil
}

// getKubeClient returns a Kubernetes client interface
// for the test cluster.
func getKubeClient() (kubernetes.Interface, error) {
	kubeConfig, err := getKubeConfig()
	if err != nil {
		return nil, err
	}
	config, err := clientcmd.BuildConfigFromFlags("", kubeConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create config: %w", err)
	}
	config.ContentType = runtime.ContentTypeProtobuf
	kubeClient, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create client: %w", err)
	}

	return kubeClient, nil
}

func clusterDownGKE(gceZone, gceRegion string) error {
	locationArg, locationVal, err := gkeLocationArgs(gceZone, gceRegion)
	if err != nil {
		return err
	}

	klog.Infof("Bringing down GKE cluster %v, location arg %v, location val %v", *gkeTestClusterName, locationArg, locationVal)
	out, err := exec.Command("gcloud", "container", "clusters", "delete", *gkeTestClusterName,
		locationArg, locationVal, "--quiet").CombinedOutput()

	if err != nil && !isNotFoundError(string(out)) {
		return fmt.Errorf("failed to bring down kubernetes e2e cluster on gke: %w", err)
	}

	return nil
}

func clusterUpGKE(project, gceZone, gceRegion, imageType string, numNodes int, useManagedDriver, enableLegacyLustrePort bool) error {
	locationArg, locationVal, err := gkeLocationArgs(gceZone, gceRegion)
	if err != nil {
		return err
	}

	out, err := exec.Command("gcloud", "container", "clusters", "list",
		locationArg, locationVal, "--verbosity", "none",
		fmt.Sprintf("--filter=name=%s", *gkeTestClusterName)).CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to check for previous test cluster: %w %s", err, out)
	}

	if len(out) > 0 {
		klog.Infof("Detected previous cluster %s. Deleting so a new one can be created...", *gkeTestClusterName)
		err = clusterDownGKE(gceZone, gceRegion)
		if err != nil {
			return err
		}
	}

	if useManagedDriver {
		// Update gcloud to the latest version to support the managed csi driver.
		cmd := exec.Command("gcloud", "components", "update")
		if err := runCommand("Updating gcloud to the latest version", cmd); err != nil {
			return fmt.Errorf("failed to update gcloud to latest version: %w", err)
		}
	}

	var cmd *exec.Cmd
	cmdParams := []string{
		"container", "clusters", "create", *gkeTestClusterName,
		locationArg, locationVal, "--num-nodes", strconv.Itoa(numNodes),
		"--quiet", "--machine-type", "n1-standard-2", "--image-type", imageType, "--network", *clusterNewtwork,
		"--workload-pool", project + ".svc.id.goog",
	}

	if isVariableSet(gkeClusterVersion) {
		cmdParams = append(cmdParams, "--cluster-version", *gkeClusterVersion)
		cmdParams = append(cmdParams, "--release-channel", "rapid")
	}

	if isVariableSet(gkeNodeVersion) {
		// The minimum supported node version for the managed csi driver is 1.33.2-gke.1072000.
		cmdParams = append(cmdParams, "--node-version", *gkeNodeVersion)
	}

	if useManagedDriver {
		cmdParams = append(cmdParams, "--addons", "LustreCsiDriver")
		if enableLegacyLustrePort {
			cmdParams = append(cmdParams, "--enable-legacy-lustre-port")
		}
	}

	cmd = exec.Command("gcloud")
	cmd.Args = append(cmd.Args, cmdParams...)
	err = runCommand("Starting E2E Cluster on GKE", cmd)
	if err != nil {
		return fmt.Errorf("failed to bring up kubernetes e2e cluster on gke: %w", err)
	}

	return nil
}

func getGKEKubeTestArgs(gceZone, gceRegion, project string) []string {
	var locationArg, locationVal string
	switch {
	case len(gceZone) > 0:
		locationArg = "--zone"
		locationVal = gceZone
	case len(gceRegion) > 0:
		locationArg = "--region"
		locationVal = gceRegion
	}

	var gkeEnv string
	switch gkeURL := os.Getenv("CLOUDSDK_API_ENDPOINT_OVERRIDES_CONTAINER"); gkeURL {
	case "https://staging-container.sandbox.googleapis.com/":
		gkeEnv = "staging"
	case "https://test-container.sandbox.googleapis.com/":
		gkeEnv = "test"
	case "":
		gkeEnv = "prod"
	default:
		// if the URL does not match to an option, assume it is a custom GKE backend
		// URL and pass that to kubetest
		gkeEnv = gkeURL
	}

	return []string{
		"--up=false",
		"--down=false",
		fmt.Sprintf("--cluster-name=%s", *gkeTestClusterName),
		fmt.Sprintf("--environment=%s", gkeEnv),
		fmt.Sprintf("%s=%s", locationArg, locationVal),
		fmt.Sprintf("--project=%s", project),
	}
}

func isNotFoundError(errstr string) bool {
	return strings.Contains(strings.ToLower(errstr), "code=404")
}

func getCurrProject() (string, error) {
	project, err := exec.Command("gcloud", "config", "get-value", "project").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to get gcloud project: %s, err: %w", project, err)
	}

	// The `gcloud config get-value project` command may print extra
	// informational lines before the project ID. We must parse the
	// output to get only the last line, which is the project ID.
	//
	// Example output from the command:
	// $ gcloud config get-value project
	// Your active configuration is: [yangspirit-joonix]
	// yangspirit-joonix
	trimmedOutput := strings.TrimSpace(string(project))
	lines := strings.Split(trimmedOutput, "\n")
	projectID := strings.TrimSpace(lines[len(lines)-1])
	if projectID == "" {
		return "", fmt.Errorf("parsed empty project ID from gcloud output: %q", string(project))
	}

	return projectID, nil
}
