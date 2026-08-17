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

# Lustre CSI Driver Testing Skill (`lustre-csi-test`)

`lustre-csi-test` is an AI agent skill that automates local verification suites, container builds, and live GKE integration testing (Static or Dynamic provisioning) for the **Lustre CSI Driver** on **Ubuntu** or **Container-Optimized OS (COS)** GKE nodes.

---

## Prerequisites

Before invoking this skill, ensure you have the following installed and configured on your workstation:
- **`gcloud` CLI:** Authenticated with permissions to access your GCP project, GKE cluster, and GCE instances.
- **`kubectl`:** Installed and accessible in your `$PATH`.
- **`make` & Docker/Buildx:** For building and pushing multi-arch container images.

---

## How to Use

### 1. Invoking the Skill
When working inside this repository, simply ask Antigravity to test your local changes:
- *"Use `lustre-csi-test` to test my local changes on GKE with static provisioning."*
- *"Run `lustre-csi-test` to build and test dynamic provisioning on my cluster."*

---

### 2. Interactive Setup Flow
When triggered, the agent will guide you through three interactive steps:

1. **GCP Project & Cluster Conflict Check:** Detects any mismatch between your active `gcloud` project and `kubectl` cluster context, asking explicitly which project to use (`gcloud config set project <PROJECT_NAME>`).
2. **GKE Cluster:** Connects your local `kubectl` to your target GKE cluster (`gcloud container clusters get-credentials ...`) and inspects its VPC network.
3. **Provisioning Mode:**
   - **Static Provisioning (Fastest):** Uses an existing Lustre filesystem. You provide the full managed instance handle (`<project-id>/<instance-location>/<instance-name>`), IP, and filesystem name.
     - **Same-VPC Enforcement:** Verifies that the existing Lustre instance and the GKE cluster belong to the exact same VPC network before proceeding.
     - If you need the agent to list existing instances, it queries `gcloud alpha lustre instances list --location=-` and supports custom API endpoints via `CLOUDSDK_API_ENDPOINT_OVERRIDES_LUSTRE`:
       - **Staging:** `https://staging-lustre.mtls.sandbox.googleapis.com/`
       - **Autopush:** `https://autopush-lustre.mtls.sandbox.googleapis.com/`
       - **Default:** Omit variable to use local environment default.
   - **Dynamic Provisioning:** Tests dynamic volume creation via the `lustre-rwx` StorageClass.

---

### 3. What the Agent Does Automatically

1. **Runs Local Suites First:** Executes `make unit-test`, `make sanity-test`, and `make verify`. If any fail, it self-corrects your code until all pass.
2. **Builds & Pushes Staging Image:** Pauses for your approval, then builds and pushes a traceable staging image (`STAGINGVERSION=dev-<sha>-<timestamp>`).
3. **Replaces Driver on GKE:** Uninstalls existing driver pods (`make uninstall`) and deploys your staging build (`make install`), monitoring `lustre-csi-node` (defined in `deploy/base/node/node.yaml` with init container `lustre-kmod-installer` and main container `lustre-csi-driver`).
4. **Automated Deep Troubleshooting:** If any pod fails to start within **30 seconds**, the agent automatically inspects container logs (`lustre-kmod-installer` / `lustre-csi-driver`) and SSHes into the GKE node, running `lsmod | grep lustre` to verify kernel module loading before executing `dmesg`.
5. **Verifies Active I/O:** Executes file write/read/delete tests inside `/lustre_volume` on `lustre-pod` and cleans up test workloads automatically.
