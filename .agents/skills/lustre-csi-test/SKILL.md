<!--
Copyright 2026 Google LLC

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    https://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
-->

---
name: lustre-csi-test
description: >-
  Guides local build, unit/sanity testing, and live GKE integration testing (static or dynamic provisioning)
  of the Lustre CSI Driver on Google Kubernetes Engine (GKE) nodes running Ubuntu or Container-Optimized OS (COS).
  Helps developers perform local validation, verify kernel module installation, test volume mounts, and execute simple I/O to ensure local changes conform to minimum quality standards.
---

# Lustre CSI Driver Testing Workflow

You are an expert Kubernetes and Storage engineer helping test local changes to the Lustre CSI Driver on Google Kubernetes Engine (GKE) nodes running **Ubuntu** or **Container-Optimized OS (COS)**.

## Core Architecture & Debugging Rules
### Split Architecture (Critical Knowledge)
- **Host OS Support:** Nodes may run **Ubuntu** or **Container-Optimized OS (COS)**. The driver architecture is identical on both.
- **Lustre Kernel Module (`kmod`):** Loaded on the host node (managed via `cos-dkms` or Secure Kernel Module Loading).
- **Lustre Client Utilities (`lfs`, `lctl`, etc.):** **NOT installed on the host node.** They reside exclusively inside the CSI driver container image.
- **Running Lustre Commands:** If you need to execute `lctl`, `lfs`, or other Lustre client tools for diagnostics, execute them inside the CSI node pod container:
  ```bash
  kubectl exec -it <lustre-csi-node-pod> -n lustre-csi-driver -c lustre-csi-driver -- /bin/sh
  ```

- **Lustre API Endpoint Overrides:** Managed Lustre supports non-prod API endpoints via `CLOUDSDK_API_ENDPOINT_OVERRIDES_LUSTRE`:
  - **Staging:** `CLOUDSDK_API_ENDPOINT_OVERRIDES_LUSTRE=https://staging-lustre.mtls.sandbox.googleapis.com/`
  - **Autopush:** `CLOUDSDK_API_ENDPOINT_OVERRIDES_LUSTRE=https://autopush-lustre.mtls.sandbox.googleapis.com/`
  - **Default:** Omit variable to use local environment default.

- **VPC Co-Location Requirement:** For Static Provisioning, the GKE cluster and the Managed Lustre instance MUST reside in the same VPC network. Otherwise, mounting will fail. Always verify matching networks before deploying test workloads.
- **Node Kernel Module Verification:** Whenever SSHing into a host node (`gcloud compute ssh`), ALWAYS run `lsmod | grep lustre` first to verify whether the Lustre kernel module is loaded before inspecting kernel logs with `dmesg`.

---

## Phase 0: Interactive Environment Setup
Before running any tests, explicitly prompt the user:
1. **GCP Project & Cluster Conflict Check:**
   - Check current project (`gcloud config get-value project`) and current `kubectl config current-context`.
   - If there is any ambiguity or project mismatch between the local gcloud config and GKE cluster, explicitly ask: *"Which exact GCP project do you want to use?"* and run `gcloud config set project <PROJECT_NAME>`.
2. **GKE Cluster Connection:** *"Which GKE cluster do you want to use? Please provide the cluster name, location, and project."*
   - Connect via:
     ```bash
     gcloud container clusters get-credentials <cluster-name> --location <location> --project <project-name>
     ```
   - Record the cluster's VPC network (`gcloud container clusters describe <cluster-name> --location <location> --format='value(networkConfig.network)'`).
3. **Provisioning Mode Selection:** *"Do you want to test with Static Provisioning using an existing Lustre instance (fastest), or Dynamic Provisioning?"*
   - **If Static Provisioning:**
     - Ask if the user already has the instance handle, IP, and filesystem name, OR if they want you to list existing instances using `gcloud alpha lustre instances list`.
     - If listing existing instances, ask which API endpoint environment to use (**Staging**, **Autopush**, or **Default**) and query using:
       ```bash
       # Staging Example:
       CLOUDSDK_API_ENDPOINT_OVERRIDES_LUSTRE=https://staging-lustre.mtls.sandbox.googleapis.com/ \
         gcloud alpha lustre instances list --location=- --project=<PROJECT_NAME>
       ```
     - Record:
       1. Full managed instance handle (`<project-id>/<instance-location>/<instance-name>`)
       2. Lustre server IP address (`EXISTING_LUSTRE_IP_ADDRESS`)
       3. Lustre filesystem name (`EXISTING_LUSTRE_FSNAME`)
     - **MANDATORY SAME-VPC VERIFICATION:** Inspect the Lustre instance's network (`gcloud alpha lustre instances describe ... --format='value(network)'`). Confirm it matches the GKE cluster's VPC network. If they differ, STOP and inform the user immediately—mounting across separate VPCs without peering will fail.
   - **If Dynamic Provisioning:** No existing instance details needed; the CSI driver will dynamically provision storage via `lustre-rwx` StorageClass.

---

## Phase 1: Local Validation & Image Build
1. **Run Local Validation Suites:**
   ```bash
   make unit-test
   make sanity-test
   make verify
   ```
2. **Iterate:** Fix any build, lint, or test failures autonomously until all three commands pass cleanly.
3. **CHECKPOINT — STOP AND WAIT:** Inform the user that local suites passed. Request permission to build and push the multi-arch staging image:
   ```bash
   STAGINGVERSION=dev-$(git rev-parse --short HEAD)-$(date +%m%d%H%M) make build-all-image-and-push-multi-arch
   ```

---

## Phase 2: GKE Driver Replacement & Health Verification
Only proceed after explicit user approval and successful image push.

1. **Uninstall Existing Driver:**
   ```bash
   make uninstall
   ```
   Wait until all existing CSI driver resources terminate cleanly.
2. **Install Built Driver:**
   ```bash
   STAGINGVERSION=<tag-from-phase-1> make install
   ```
3. **Verify DaemonSet & Deployment Status:** Check `kubectl get Deployment,DaemonSet,Pods -n lustre-csi-driver` until all CSI pods reach `Running`.
4. **CSI Driver Failure Escalation Protocol:**
   - If CSI driver pods do not enter `Running` state:
     1. Check init container (`lustre-kmod-installer`) and main container (`lustre-csi-driver`) logs via `kubectl logs -n lustre-csi-driver <pod> -c <container>`. Note: `deploy/base/node/node.yaml` defines DaemonSet `lustre-csi-node` with init container `lustre-kmod-installer` and main container `lustre-csi-driver`.
     2. If container logs do not explain the failure, SSH into the underlying GKE node (`gcloud compute ssh <node-name>`), run `lsmod | grep lustre` to check if the Lustre kernel module is loaded, and then run `dmesg` to inspect kernel module loading/LNet logs.

---

## Phase 3: Integration Testing (Static or Dynamic Provisioning)

### Mode A: Static Provisioning Test (Fastest)
Uses `examples/pre-prov/preprov-pvc-pv.yaml` and `examples/pre-prov/preprov-pod.yaml`.
1. Substitute placeholders (`volumeHandle`, `ip`, `filesystem`) dynamically without modifying tracked git files:
   ```bash
   sed -e "s|<project-id>/<instance-location>/<instance-name>|$INSTANCE_HANDLE|g" \
       -e "s|\${EXISTING_LUSTRE_IP_ADDRESS}|$LUSTRE_IP|g" \
       -e "s|\${EXISTING_LUSTRE_FSNAME}|$FS_NAME|g" \
       examples/pre-prov/preprov-pvc-pv.yaml | kubectl apply -f -
   kubectl apply -f examples/pre-prov/preprov-pod.yaml
   ```
   - Expected Pod Name: `lustre-pod`

### Mode B: Dynamic Provisioning Test
Uses `examples/dynamic-prov/dynamic-pvc.yaml` and `examples/dynamic-prov/dynamic-pod.yaml`.
1. Apply dynamic manifests directly:
   ```bash
   kubectl apply -f examples/dynamic-prov/dynamic-pvc.yaml
   kubectl apply -f examples/dynamic-prov/dynamic-pod.yaml
   ```
   - Expected Pod Name: `lustre-pod`

---

## Phase 4: 30-Second Pod Readiness & Deep Debugging Escalation
1. Monitor the target test pod (`lustre-pod`).
2. **30-Second Timeout Rule:** If the pod does not reach `Running` within **30 seconds**, assume mounting failed:
   - First inspect CSI node driver logs:
     ```bash
     kubectl logs -n lustre-csi-driver <node-pod> -c lustre-csi-driver
     ```
   - If CSI driver logs are insufficient, SSH into the GKE node (`gcloud compute ssh <node-name>`), run `lsmod | grep lustre` to verify if the Lustre kernel module is loaded, and then run `dmesg` to inspect kernel-level Lustre mount errors.
   - If Lustre utility inspection (`lctl`, `lfs`) is needed, execute inside the CSI node driver container (`kubectl exec -it <node-pod> -n lustre-csi-driver -c lustre-csi-driver -- /bin/sh`).

---

## Phase 5: Active End-to-End I/O Verification & Clean Teardown
1. Once the test pod (`lustre-pod`) enters `Running`, execute an interactive I/O check inside the pod:
   ```bash
   kubectl exec -it lustre-pod -- /bin/sh -c '
     echo "lustre-test-data-$(date +%s)" > /lustre_volume/testfile && \
     cat /lustre_volume/testfile && \
     rm /lustre_volume/testfile
   '
   ```
   Confirm file write, read, and delete succeed without errors.
2. **Teardown:** Clean up test PV/PVC/Pod resources after verification so the cluster remains clean. Report test results to the user and remain active for further follow-up instructions.
