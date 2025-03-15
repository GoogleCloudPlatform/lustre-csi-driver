<!--
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
-->

# Lustre CSI Driver Manual Installation

> WARNING: This documentation describes how to manually install the driver to your GKE clusters. The manual installation should only be used for test purposes.

> NOTE: The manual installation only works on GKE Standard clusters.

## Prerequisites

- Clone the repo by running the following command.

  ```bash
  git clone https://github.com/GoogleCloudPlatform/lustre-csi-driver.git
  cd lustre-csi-driver
  ```

- Install `jq` utility.

  ```bash
  sudo apt-get update
  sudo apt-get install jq
  ```

- Create a standard GKE cluster. Autopilot clusters are not supported for manual installation.

  > Ensure that your cluster's node pool version is at least `1.31.5-gke.1299000` or `1.32.1-gke.1673000` and that the node image is **Container-Optimized OS with containerd** (`cos_containerd`). Additionally, verify that [Secure Boot](https://cloud.google.com/kubernetes-engine/docs/how-to/shielded-gke-nodes#secure_boot) is disabled in your node pool (it is disabled by default in standard GKE clusters).

- Run the following command to ensure the kubectl context is set up correctly.

  ```bash
  gcloud container clusters get-credentials ${CLUSTER_NAME}

  # check the current context
  kubectl config current-context
  ```

## Installation

You may install the Lustre CSI driver via Helm or Kustomize.

### Helm

For experimental use, the CSI driver can be deployed directly using Helm with pre-built images. Refer to the [CSI driver installation via helm](/helm/README.md) documentation for instructions.

### Kustomize

> **NOTE:** You may skip steps 1 and 2 if:
>
> - You plan to use **pre-built images**.
> - You are only **importing an existing Lustre instance** (instead of provisioning a new one via the CSI driver).
> - You are using a **private GKE cluster** where your cluster admin restricts access to public container registries.

1. **Build and push the images** following the [Lustre CSI Driver Development Guide](development.md).

2. **Set up a service account** with the required roles and download a service account key. This account will be used by the driver to provision **Managed Lustre** instances and access Lustre APIs.

   To automate this setup, run the following script, specifying a directory to store the service account key:

   ```bash
   PROJECT=<your-gcp-project> GSA_DIR=<your-directory-to-store-credentials-by-default-home-dir> ./deploy/base/setup/gsa_setup.sh
   ```

   > **Security Note:** Ensure that the specified directory is not publicly accessible to prevent credential leaks.

3. **Install the driver**:

   - To install the CSI driver **with pre-built images**, run:

     ```bash
     OVERLAY=gke-release make install
     ```

   - To install the CSI driver **from custom-built images**, run:

     ```bash
     # Specify the image registry and version if you built the images from source.
     make install REGISTRY=<your-container-registry> STAGINGVERSION=<staging-version> PROJECT=<cluster-project-id>
     ```

## **Verify Driver Installation**

Run the following command:

```bash
kubectl get CSIDriver,Deployment,DaemonSet,Pods -n lustre-csi-driver
```

You should see output similar to the following:

```text
NAME                                                 ATTACHREQUIRED   PODINFOONMOUNT   STORAGECAPACITY   TOKENREQUESTS   REQUIRESREPUBLISH   MODES        AGE
csidriver.storage.k8s.io/lustre.csi.storage.gke.io   false            false            false             <unset>         false               Persistent   27s

NAME                                    READY   UP-TO-DATE   AVAILABLE   AGE
deployment.apps/lustre-csi-controller   1/1     1            1           28s

NAME                             DESIRED   CURRENT   READY   UP-TO-DATE   AVAILABLE   NODE SELECTOR            AGE
daemonset.apps/lustre-csi-node   1         1         1       1            1           kubernetes.io/os=linux   28s

NAME                                         READY   STATUS    RESTARTS   AGE
pod/lustre-csi-controller-5bd4dddf9d-hx9bg   3/3     Running   0          28s
pod/lustre-csi-node-gqffs                    2/2     Running   0          28s
```

> **NOTE:** If you installed the driver using the **gke-release overlay**, the `lustre-csi-controller` deployment will not exist.

## **Uninstallation**

To uninstall the driver, run:

```bash
make uninstall
```

If you installed the CSI driver using the **gke-release** overlay, uninstall it using:

```bash
OVERLAY=gke-release make uninstall
```
