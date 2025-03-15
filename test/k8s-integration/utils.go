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
	"time"

	"golang.org/x/oauth2/google"
	cloudresourcemanager "google.golang.org/api/cloudresourcemanager/v1"
	"google.golang.org/api/option"
	"k8s.io/klog/v2"
	boskosclient "sigs.k8s.io/boskos/client"
)

var boskos, _ = boskosclient.NewClient(os.Getenv("JOB_NAME"), "http://boskos", "", "")

func runCommand(action string, cmd *exec.Cmd) error {
	cmd.Stdout = os.Stdout
	cmd.Stdin = os.Stdin
	cmd.Stderr = os.Stderr

	klog.Infof("%s", action)
	klog.Infof("cmd env=%v", cmd.Env)
	klog.Infof("cmd path=%v", cmd.Path)
	klog.Infof("cmd args=%s", cmd.Args)

	if err := cmd.Start(); err != nil {
		return err
	}

	if err := cmd.Wait(); err != nil {
		return err
	}

	return nil
}

func generateUniqueTmpDir() string {
	dir, err := os.MkdirTemp("", "lustre-csi-driver-tmp")
	if err != nil {
		klog.Fatalf("Failed to creating temp dir: %v", err)
	}

	return dir
}

func removeDir(dir string) {
	err := os.RemoveAll(dir)
	if err != nil {
		klog.Fatalf("Failed to remove temp dir: %v", err)
	}
}

func ensureVariable(v *string, set bool, msgOnError string) {
	if set && len(*v) == 0 {
		klog.Fatal(msgOnError)
	} else if !set && len(*v) != 0 {
		klog.Fatal(msgOnError)
	}
}

func ensureFlag(v *bool, setTo bool, msgOnError string) {
	if *v != setTo {
		klog.Fatal(msgOnError)
	}
}

func ensureExactlyOneVariableSet(vars []*string, msgOnError string) {
	var count int
	for _, v := range vars {
		if len(*v) != 0 {
			count++
		}
	}

	if count != 1 {
		klog.Fatal(msgOnError)
	}
}

func shredFile(filePath string) {
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		klog.V(4).Infof("File %v was not found, skipping shredding", filePath)

		return
	}
	klog.V(4).Infof("Shredding file %v", filePath)
	out, err := exec.Command("shred", "--remove", filePath).CombinedOutput()
	if err != nil {
		klog.V(4).Infof("Failed to shred file %v: %v\nOutput:%v", filePath, err, out)
	}
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		klog.V(4).Infof("File %v successfully shredded", filePath)

		return
	}

	// Shred failed Try to remove the file for good meausure
	err = os.Remove(filePath)
	if err != nil {
		klog.V(4).Infof("Failed to remove service account file %s: %v", filePath, err)
	}
}

func ensureVariableVal(v *string, val string, msgOnError string) {
	if *v != val {
		klog.Fatal(msgOnError)
	}
}

func ensureVariableNotVal(v *string, val string, msgOnError string) {
	if *v == val {
		klog.Fatal(msgOnError)
	}
}

func isVariableSet(v *string) bool {
	return len(*v) != 0
}

func setupProwConfig(resourceType string) (project, serviceAccount string) {
	// Try to get a Boskos project
	klog.V(4).Infof("Running in PROW")
	klog.V(4).Infof("Fetching a Boskos loaned project")

	p, err := boskos.Acquire(resourceType, "free", "busy")
	if err != nil {
		klog.Fatalf("Boskos failed to acquire project: %v", err)
	}

	if p == nil {
		klog.Fatalf("Boskos does not have a free gce-project at the moment")
	}

	project = p.Name

	go func(c *boskosclient.Client, _ string) {
		for range time.Tick(time.Minute * 5) {
			if err := c.UpdateOne(p.Name, "busy", nil); err != nil {
				klog.Warningf("[Boskos] Update %v failed with %v", p, err)
			}
		}
	}(boskos, p.Name)

	// If we're on CI overwrite the service account
	klog.V(4).Infof("Fetching the default compute service account")

	c, err := google.DefaultClient(context.TODO(), cloudresourcemanager.CloudPlatformScope)
	if err != nil {
		klog.Fatalf("Failed to get Google Default Client: %v", err)
	}

	cloudresourcemanagerService, err := cloudresourcemanager.NewService(context.TODO(), option.WithHTTPClient(c))
	if err != nil {
		klog.Fatalf("Failed to create new cloudresourcemanager: %v", err)
	}

	resp, err := cloudresourcemanagerService.Projects.Get(project).Do()
	if err != nil {
		klog.Fatalf("Failed to get project %v from Cloud Resource Manager: %v", project, err)
	}

	// Default Compute Engine service account
	// [PROJECT_NUMBER]-compute@developer.gserviceaccount.com
	serviceAccount = fmt.Sprintf("%v-compute@developer.gserviceaccount.com", resp.ProjectNumber)
	klog.Infof("Prow config utilizing:\n- project %q\n- project number %d\n- service account %q", project, resp.ProjectNumber, serviceAccount)

	return project, serviceAccount
}
