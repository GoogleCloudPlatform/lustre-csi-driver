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
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/GoogleCloudPlatform/lustre-csi-driver/test/k8s-integration/podlogs"
)

const (
	driverBinary = "lustre-csi-driver"
)

func installDriver(pkgDir, overlay string) error {
	var deployEnv []string
	if *saFile != "" {
		// Setup service account file for secret creation.
		tmpSaFile := filepath.Join(generateUniqueTmpDir(), "lustre_csi_driver_sa.json")
		defer removeDir(filepath.Dir(tmpSaFile))

		// Move the SA file to a temp path.
		out, err := exec.Command("cp", *saFile, tmpSaFile).CombinedOutput()
		if err != nil {
			return fmt.Errorf("error copying service account key: %s, err: %w", out, err)
		}
		defer shredFile(tmpSaFile)

		deployEnv = append(deployEnv, fmt.Sprintf("GSA_FILE=%s", tmpSaFile))
	}

	cmd := exec.Command("make", "-C", pkgDir, "install")
	deployEnv = append(deployEnv, fmt.Sprintf("OVERLAY=%s", overlay))
	deployEnv = append(deployEnv, fmt.Sprintf("LUSTRE_ENDPOINT=%s", *lustreEndpoint))
	deployEnv = append(os.Environ(), deployEnv...)
	cmd.Env = deployEnv

	if err := runCommand("Installing non-managed CSI driver", cmd); err != nil {
		return fmt.Errorf("failed to run install non-managed CSI driver: %w", err)
	}

	return nil
}

func deleteDriver(pkgDir, overlay string) error {
	cmd := exec.Command("make", "-C", pkgDir, "uninstall", "OVERLAY="+overlay)
	if err := runCommand("Uninstalling non-managed CSI driver", cmd); err != nil {
		return fmt.Errorf("failed run uninstall non-managed CSI driver: %w", err)
	}

	return nil
}

func pushImage(pkgDir, stagingVersion string) error {
	err := os.Setenv("STAGINGVERSION", stagingVersion)
	if err != nil {
		return err
	}

	// Authenticate to AR.
	cmd := exec.Command("gcloud", "auth", "configure-docker", "us-central1-docker.pkg.dev")
	if err := runCommand("Authenticating to AR", cmd); err != nil {
		return fmt.Errorf("failed to auth AR: %w", err)
	}

	cmd = exec.Command("make", "-C", pkgDir, "build-driver-image-and-push",
		fmt.Sprintf("STAGINGVERSION=%s", stagingVersion))
	err = runCommand("Pushing GCP Container for Linux", cmd)
	if err != nil {
		return fmt.Errorf("failed to run make command for linux: err: %w", err)
	}

	return nil
}

func deleteImage(stagingVersion string) error {
	project, err := getCurrProject()
	if err != nil {
		return err
	}
	driverImage := fmt.Sprintf("gcr.io/%s/%s", project, driverBinary)
	// Delete driver image.
	cmd := exec.Command("gcloud", "container", "images", "delete", fmt.Sprintf("%s:%s", driverImage, stagingVersion), "--quiet")
	err = runCommand("Deleting GCR Container", cmd)
	if err != nil {
		return fmt.Errorf("failed to delete container image %s:%s: %w", driverImage, stagingVersion, err)
	}

	return nil
}

// dumpDriverLogs will watch all pods in the driver namespace
// and copy its logs to the test artifacts directory, if set.
// It returns a context.CancelFunc that needs to be invoked when
// the test is finished.
func dumpDriverLogs() (context.CancelFunc, error) {
	// Dump all driver logs to the test artifacts
	artifactsDir, ok := os.LookupEnv("ARTIFACTS")
	if ok {
		client, err := getKubeClient()
		if err != nil {
			return nil, fmt.Errorf("failed to get kubeclient: %w", err)
		}
		out := podlogs.LogOutput{
			StatusWriter:  os.Stdout,
			LogPathPrefix: filepath.Join(artifactsDir, "lustre-csi-driver") + "/",
		}
		ctx, cancel := context.WithCancel(context.Background())
		if err = podlogs.CopyAllLogs(ctx, client, getDriverNamespace(), out); err != nil {
			return cancel, fmt.Errorf("failed to start pod logger: %w", err)
		}

		return cancel, nil
	}

	return nil, nil
}

// mergeArtifacts merges the results of doing multiple gingko runs, taking all junit files
// in the specified subdirectories of the artifacts directory and merging into a single
// file at the artifacts root.  If artifacts are not saved (ie, ARTIFACTS is not set),
// this is a no-op. See kubernetes-csi/csi-release-tools/prow.sh for the inspiration.
func mergeArtifacts(subdirectories []string) error {
	artifactsDir, ok := os.LookupEnv("ARTIFACTS")
	if !ok {
		// No artifacts, nothing to merge.
		return nil
	}
	sourceDirs := make([]string, 0, len(subdirectories))
	for _, subdir := range subdirectories {
		sourceDirs = append(sourceDirs, filepath.Join(artifactsDir, subdir))
	}

	return MergeJUnit("External Storage", sourceDirs, filepath.Join(artifactsDir, "junit_fscsi.xml"))
}

func getDriverNamespace() string {
	return externalDriverNamespace
}
