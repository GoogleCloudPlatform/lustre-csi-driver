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

# Troubleshooting

## Log queries

Run the following queries on [GCP Logs Explorer](https://cloud.google.com/logging/docs/view/logs-explorer-interface) to check logs.

- Lustre CSI drver controller server logs:

    ```text
    resource.type="k8s_container"
    resource.labels.pod_name=~"lustre-csi-controller*"
    ```

- Lustre CSI drver node server logs:

    ```text
    resource.type="k8s_container"
    resource.labels.pod_name=~"lustre-csi-node*"
    ```

## Pod event warnings

If your workload Pods cannot start up, run `kubectl describe pod <your-pod-name> -n <your-namespace>` to check the Pod events. Find the troubleshooting guide below according to the Pod event.

### CSI driver enablement issues

- Pod event warning examples:

  - > MountVolume.MountDevice failed for volume "xxx" : kubernetes.io/csi: attacher.MountDevice failed to create newCsiDriverClient: driver name lustre.csi.storage.gke.io not found in the list of registered CSI drivers

  - > MountVolume.SetUp failed for volume "xxx" : kubernetes.io/csi: mounter.SetUpAt failed to get CSI client: driver name lustre.csi.storage.gke.io not found in the list of registered CSI drivers

- Solutions:

  This warning indicates that the CSI driver is either not installed or not yet running. Double-check whether the CSI driver is running in your cluster by referring to [this section](installation.md#check-the-driver-status). If the cluster was recently scaled, updated, or upgraded, this warning is expected and should be transient, as the CSI driver Pods may take a few minutes to become fully functional after cluster operations.

### MountVolume.SetUp failures

> Note: the rpc error code can be used to triage `MountVolume.SetUp` issues. For example, `Unauthenticated` and `PermissionDenied` usually mean the authentication was not configured correctly. A rpc error code `Internal` means that unexpected issues occurred in the CSI driver, create a [new issue](https://github.com/GoogleCloudPlatform/lustre-csi-driver/issues/new) on the GitHub project page.

#### AlreadyExists

- Pod event warning examples:

  - > MountVolume.SetUp failed for volume "xxx" : rpc error: code = AlreadyExists desc = A mountpoint with the same lustre filesystem name "xxx" already exists on node "xxx". Please mount different lustre filesystems

- Solutions:

  Please try recreating the Lustre instance with a different filesystem name or use another Lustre instance with a unique filesystem name. Mounting multiple volumes from different Lustre instances with the same filesystem name on a single node is **not supported** because identical filesystem names result in the same major and minor device numbers, which conflicts with the shared mount architecture on a per-node basis.

#### Internal

- Pod event warning examples:

  - > `MountVolume.SetUp failed for volume "xxx" : rpc error: code = Internal desc = xxx` or `UnmountVolume.TearDown failed for volume "xxx" : rpc error: code = Internal desc = xxx`

- Solutions:

  Warnings that are not listed above and include a rpc error code `Internal` mean that other unexpected issues occurred in the CSI driver, Create a new issue on the GitHub project page. Include your GKE cluster verion, detailed workload information, and the Pod event warning message in the issue.
