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
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/klog/v2"
)

var (
	pkgDir = flag.String("pkg-dir", "", "the package directory")

	// Cluster configs.
	doNetworkSetup = flag.Bool("do-network-setup", true, "whether to setup and then cleanup a VPC network during the test")
	bringupCluster = flag.Bool("bringup-cluster", true, "bringup a GKE cluster before the e2e test")
	testVersion    = flag.String("test-version", "", "version of k8s binary to download and use for the e2e test")
	numNodes       = flag.Int("num-nodes", -1, "the number of nodes in the test cluster")
	imageType      = flag.String("image-type", "cos_containerd", "the node image type to use for the cluster")

	// Test infrastructure flags.
	boskosResourceType = flag.String("boskos-resource-type", "gke-internal-project", "name of the boskos resource type to reserve")
	storageClassFiles  = flag.String("storageclass-files", "storage-class.yaml", "name of storageclass yaml file to use for test relative to test/k8s-integration/config")
	inProw             = flag.Bool("run-in-prow", false, "whether the test is running in PROW")

	// Driver flags.
	deployOverlayName = flag.String("deploy-overlay-name", "dev", "which kustomize overlay to deploy the driver with")
	doDriverBuild     = flag.Bool("do-driver-build", false, "build the driver from source, install the driver and uninstall it after the test")
	useManagedDriver  = flag.Bool("use-gke-driver", true, "use GKE managed Lustre CSI driver for the tests")
	saFile            = flag.String("service-account-file", "", "path of service account file")
	lustreEndpoint    = flag.String("lustre-endpoint", "staging", "Lustre API endpoint for unmanged csi driver")

	// Test flags.
	testFocus = flag.String("test-focus", "External.Storage", "test focus for k8s external e2e tests")
	parallel  = flag.Int("parallel", 8, "the number of parallel tests setting for ginkgo parallelism")

	// GKE specific flags.
	gkeClusterVersion  = flag.String("gke-cluster-version", "", "version of k8s master and node for GKE cluster")
	gkeNodeVersion     = flag.String("gke-node-version", "", "GKE cluster worker node version")
	gkeTestClusterName = flag.String("gke-cluster-name", "", "GKE cluster name")
	gceZone            = flag.String("gce-zone", "", "zone that the gke zonal cluster is created/found in")
	gceRegion          = flag.String("gce-region", "", "region that the gke regional cluster should be created in")
	clusterNewtwork    = flag.String("cluster-network", "lustre-network", "the VPC network to be used by the GKE cluster")
)

const (
	externalDriverNamespace = "lustre-csi-driver"
	gkeTestClusterPrefix    = "lustre-csi"
)

type testParameters struct {
	stagingVersion    string
	pkgDir            string
	testDir           string
	testFocus         string
	testSkip          string
	clusterVersion    string
	cloudProviderArgs []string
	imageType         string
	nodeVersion       string
	parallel          int
}

func main() {
	klog.InitFlags(nil)
	if err := flag.Set("logtostderr", "true"); err != nil {
		klog.Fatalf("Failed to set logtostderr: %v", err)
	}
	flag.Parse()

	if *inProw {
		*doNetworkSetup = true
		*bringupCluster = true
	}

	if *useManagedDriver {
		ensureFlag(doDriverBuild, false, "'do-driver-build' must be false when using GKE managed driver")
	}

	if !*useManagedDriver {
		ensureVariable(deployOverlayName, true, "deploy-overlay-name is a required flag")
		if *inProw {
			ensureVariable(saFile, true, "service-account-file must be set in prow test for unmanaged driver")
		}
	}

	ensureVariable(testFocus, true, "test-focus is a required flag")
	ensureVariable(pkgDir, true, "pkg-dir is a required flag")

	if len(*gceRegion) != 0 {
		ensureVariable(gceZone, false, "gce-zone and gce-region cannot both be set")
	} else {
		ensureVariable(gceZone, true, "One of gce-zone or gce-region must be set")
	}

	ensureVariable(testVersion, true, "test-version is a required flag.")

	if !*bringupCluster && len(*gkeTestClusterName) == 0 {
		klog.Fatalf("gke-cluster-name must be set when using a pre-existing cluster")
	}

	if len(*gkeTestClusterName) == 0 {
		randSuffix := string(uuid.NewUUID())[0:4]
		*gkeTestClusterName = gkeTestClusterPrefix + "-" + randSuffix
	}

	if *numNodes == -1 && *bringupCluster {
		klog.Fatalf("num-nodes must be set to number of nodes in cluster")
	}

	err := handle()
	if err != nil {
		klog.Fatalf("Failed to run integration test: %v", err)
	}
}

func handle() error {
	oldmask := syscall.Umask(0o000)
	defer syscall.Umask(oldmask)

	testParams := &testParameters{
		testFocus:      *testFocus,
		stagingVersion: string(uuid.NewUUID()),
		imageType:      *imageType,
		parallel:       *parallel,
		pkgDir:         *pkgDir,
	}
	// If running in Prow, then acquire and set up a project through Boskos
	if *inProw {
		oldProject, err := getCurrProject()
		if err != nil {
			return err
		}

		newproject, _ := setupProwConfig(*boskosResourceType)
		err = setEnvProject(newproject)
		if err != nil {
			return fmt.Errorf("failed to set project environment to %s: %w", newproject, err)
		}

		defer func() {
			err = setEnvProject(oldProject)
			if err != nil {
				klog.Errorf("Failed to set project environment to %s: %v", oldProject, err)
			}
		}()

		if _, ok := os.LookupEnv("USER"); !ok {
			err = os.Setenv("USER", "prow")
			if err != nil {
				return fmt.Errorf("failed to set user in prow to prow: %w", err)
			}
		}
	}

	project, err := getCurrProject()
	if err != nil {
		return err
	}

	if *doDriverBuild {
		err := pushImage(testParams.pkgDir, testParams.stagingVersion)
		if err != nil {
			return fmt.Errorf("failed pushing image: %w", err)
		}
		defer func() {
			err := deleteImage(testParams.stagingVersion)
			if err != nil {
				klog.Errorf("Failed to delete image: %v", err)
			}
		}()
	}

	// Create temporary directories for kubernetes builds
	k8sParentDir := generateUniqueTmpDir()
	testParams.testDir = filepath.Join(k8sParentDir, "kubernetes")
	defer removeDir(k8sParentDir)

	if *doNetworkSetup {
		err := setupNetwork(project)
		defer cleanupNetwork(project)
		if err != nil {
			return fmt.Errorf("failed to setup VPC network: %w", err)
		}
	}

	if *bringupCluster {
		if err := clusterUpGKE(*gceZone, *gceRegion, testParams.imageType, *numNodes, *useManagedDriver); err != nil {
			return fmt.Errorf("failed to cluster up: %w", err)
		}
		defer func() {
			if err := clusterDownGKE(*gceZone, *gceRegion); err != nil {
				klog.Errorf("Failed to cluster down: %v", err)
			}
		}()
	}

	if !*useManagedDriver && *doDriverBuild {
		err := installDriver(testParams.pkgDir, *deployOverlayName)
		defer func() {
			if teardownErr := deleteDriver(testParams.pkgDir, *deployOverlayName); teardownErr != nil {
				klog.Errorf("Failed to delete driver: %v", teardownErr.Error())
			}
		}()
		if err != nil {
			return fmt.Errorf("failed to install CSI Driver: %w", err)
		}
	}

	cancel, err := dumpDriverLogs()
	if err != nil {
		return fmt.Errorf("failed to start driver logging: %w", err)
	}
	defer func() {
		if cancel != nil {
			cancel()
		}
	}()

	testParams.cloudProviderArgs = getGKEKubeTestArgs(*gceZone, *gceRegion, project)

	testParams.clusterVersion = mustGetKubeClusterVersion()
	klog.Infof("Kubernetes cluster server version: %s", testParams.clusterVersion)
	testParams.nodeVersion = *gkeNodeVersion
	testParams.testSkip = generateGKETestSkip(testParams)

	// Run the tests using the testDir kubernetes
	if len(*storageClassFiles) != 0 {
		applicableStorageClassFiles := []string{}
		for _, rawScFile := range strings.Split(*storageClassFiles, ",") {
			scFile := strings.TrimSpace(rawScFile)
			if len(scFile) == 0 {
				continue
			}
			applicableStorageClassFiles = append(applicableStorageClassFiles, scFile)
		}
		if len(applicableStorageClassFiles) == 0 {
			return errors.New("no applicable storage classes found")
		}
		var ginkgoErrors []string
		var testOutputDirs []string
		for _, scFile := range applicableStorageClassFiles {
			outputDir := strings.TrimSuffix(scFile, ".yaml")
			testOutputDirs = append(testOutputDirs, outputDir)
			if err = runCSITests(testParams, scFile, outputDir); err != nil {
				ginkgoErrors = append(ginkgoErrors, err.Error())
			}
		}
		if err = mergeArtifacts(testOutputDirs); err != nil {
			return fmt.Errorf("artifact merging failed: %w", err)
		}
		if ginkgoErrors != nil {
			return fmt.Errorf("runCSITests failed: %v", strings.Join(ginkgoErrors, " "))
		}
	} else {
		return errors.New("did not run either CSI test")
	}

	return nil
}

func generateGKETestSkip(_ *testParameters) string {
	skipString := "\\[Disruptive\\]|\\[Serial\\]"

	// Lustre CSI driver does not support ephemeral volumes.
	skipString += "|External.*Storage.*ephemeral"

	return skipString
}

func runCSITests(testParams *testParameters, storageClassFile, reportPrefix string) error {
	testDriverConfigFile, err := generateDriverConfigFile(testParams, storageClassFile)
	if err != nil {
		return err
	}
	testConfigArg := fmt.Sprintf("--storage.testdriver=%s", testDriverConfigFile)

	return runTestsWithConfig(testParams, testConfigArg, reportPrefix)
}

func runTestsWithConfig(testParams *testParameters, testConfigArg, reportPrefix string) error {
	kubeconfig, err := getKubeConfig()
	if err != nil {
		return err
	}
	os.Setenv("KUBECONFIG", kubeconfig)

	artifactsDir, ok := os.LookupEnv("ARTIFACTS")
	kubetestDumpDir := ""
	if ok {
		if len(reportPrefix) > 0 {
			kubetestDumpDir = filepath.Join(artifactsDir, reportPrefix)
			if err := os.MkdirAll(kubetestDumpDir, 0o755); err != nil {
				return err
			}
		} else {
			kubetestDumpDir = artifactsDir
		}
	}

	focus := testParams.testFocus
	skip := testParams.testSkip

	// kubetest2 flags
	var runID string
	if uid, exists := os.LookupEnv("PROW_JOB_ID"); exists && uid != "" {
		// reuse uid for CI use cases
		runID = uid
	} else {
		runID = string(uuid.NewUUID())
	}

	// Usage: kubetest2 <deployer> [Flags] [DeployerFlags] -- [TesterArgs]
	// [Flags]
	kubeTest2Args := []string{
		"gke",
		fmt.Sprintf("--run-id=%s", runID),
		"--test=ginkgo",
	}

	// [DeployerFlags]
	kubeTest2Args = append(kubeTest2Args, testParams.cloudProviderArgs...)
	if kubetestDumpDir != "" {
		kubeTest2Args = append(kubeTest2Args, fmt.Sprintf("--artifacts=%s", kubetestDumpDir))
	}

	kubeTest2Args = append(kubeTest2Args, "--")

	// [TesterArgs]
	kubeTest2Args = append(kubeTest2Args, fmt.Sprintf("--test-package-marker=latest-%s.txt", *testVersion))
	kubeTest2Args = append(kubeTest2Args, fmt.Sprintf("--focus-regex=%s", focus))
	kubeTest2Args = append(kubeTest2Args, fmt.Sprintf("--skip-regex=%s", skip))
	kubeTest2Args = append(kubeTest2Args, fmt.Sprintf("--parallel=%d", testParams.parallel))
	kubeTest2Args = append(kubeTest2Args, fmt.Sprintf("--test-args=%s", testConfigArg))
	// Default timeout has been reduced from 24 hours to 1 hours
	// in k/k repo because Ginkgo v1 is deprecated
	// since https://github.com/kubernetes/kubernetes/pull/109111.
	kubeTest2Args = append(kubeTest2Args, "--ginkgo-args=--timeout=24h")

	err = runCommand("Running Tests", exec.Command("kubetest2", kubeTest2Args...))
	if err != nil {
		return fmt.Errorf("failed to run tests on e2e cluster: %w", err)
	}

	return nil
}

func setEnvProject(project string) error {
	out, err := exec.Command("gcloud", "config", "set", "project", project).CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to set gcloud project to %s: %s, err: %w", project, out, err)
	}

	err = os.Setenv("PROJECT", project)
	if err != nil {
		return err
	}

	return nil
}
