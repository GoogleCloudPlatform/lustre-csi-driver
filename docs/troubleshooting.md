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

### MountVolume failures

#### AlreadyExists

- Pod event warning examples:

  ```bash
  MountVolume.MountDevice failed for volume "xxx" : rpc error: code = AlreadyExists desc = A mountpoint with the same lustre filesystem name "xxx" already exists on node "xxx". Please mount different lustre filesystems
  ```

- Solutions:

  Please try recreating the Lustre instance with a different filesystem name or use another Lustre instance with a unique filesystem name. Mounting multiple volumes from different Lustre instances with the same filesystem name on a single node is **not supported** because identical filesystem names result in the same major and minor device numbers, which conflicts with the shared mount architecture on a per-node basis.

#### Internal

- Pod Event Warning Examples:

    ```bash
    MountVolume.MountDevice failed for volume "preprov-pv-wrongfs" : rpc error: code = Internal desc = Could not mount "10.90.2.4@tcp:/testlfs1" at "/var/lib/kubelet/plugins/kubernetes.io/csi/lustre.csi.storage.gke.io/639947affddca6d2ff04eac5ec9766c65dd851516ce34b3b44017babfc01b5dc/globalmount" on node gke-lustre-default-nw-6988-pool-1-acbefebf-jl1v: mount failed: exit status 2
    Mounting command: mount
    Mounting arguments: -t lustre 10.90.2.4@tcp:/testlfs1 /var/lib/kubelet/plugins/kubernetes.io/csi/lustre.csi.storage.gke.io/639947affddca6d2ff04eac5ec9766c65dd851516ce34b3b44017babfc01b5dc/globalmount
    Output: mount.lustre: mount 10.90.2.4@tcp:/testlfs1 at /var/lib/kubelet/plugins/kubernetes.io/csi/lustre.csi.storage.gke.io/639947affddca6d2ff04eac5ec9766c65dd851516ce34b3b44017babfc01b5dc/globalmount failed: No such file or directory
    Is the MGS specification correct?
    Is the filesystem name correct?
    If upgrading, is the copied client log valid? (see upgrade docs)
    ```

- Solutions:

  This error means the filesystem name of the Lustre instance you are trying to mount is incorrect or does not exist. Double-check the filesystem name of the Lustre instance.

---

- Pod Event Warning Examples:

    ```bash
    MountVolume.MountDevice failed for volume "preprov-pv-wrongfs" : rpc error: code = Internal desc = Could not mount "10.90.2.5@tcp:/testlfs" at "/var/lib/kubelet/plugins/kubernetes.io/csi/lustre.csi.storage.gke.io/639947affddca6d2ff04eac5ec9766c65dd851516ce34b3b44017babfc01b5dc/globalmount" on node gke-lustre-default-nw-6988-pool-1-acbefebf-jl1v: mount failed: exit status 5
    Mounting command: mount
    Mounting arguments: -t lustre 10.90.2.5@tcp:/testlfs /var/lib/kubelet/plugins/kubernetes.io/csi/lustre.csi.storage.gke.io/639947affddca6d2ff04eac5ec9766c65dd851516ce34b3b44017babfc01b5dc/globalmount
    Output: mount.lustre: mount 10.90.2.5@tcp:/testlfs at /var/lib/kubelet/plugins/kubernetes.io/csi/lustre.csi.storage.gke.io/639947affddca6d2ff04eac5ec9766c65dd851516ce34b3b44017babfc01b5dc/globalmount failed: Input/output error
    Is the MGS running?
    ```

- Solutions:

  This error means your GKE cluster cannot connect to the Lustre instance using the specified IP and filesystem name. Ensure the instance IP is correct and that your GKE cluster is in the same VPC network as your Lustre instance. Additionally, if you're using a [statically provisioned](./preprov-guide.md) volume, ensure that the Lustre instance has the [gke-support-enabled](https://cloud.google.com/managed-lustre/docs/create-instance#create_an_instance) flag set.

---

- Pod Event Warning Examples:

  - ```bash
    MountVolume.MountDevice failed for volume "xxx" : rpc error: code = Internal desc = xxx
    ```

  - ```bash
    MountVolume.SetUp failed for volume "xxx" : rpc error: code = Internal desc = xxx
    ```

  - ```bash
    UnmountVolume.TearDown failed for volume "xxx" : rpc error: code = Internal desc = xxx
    ```

  - ```bash
    UnmountVolume.Unmount failed for volume "xxx" : rpc error: code = Internal desc = xxx
    ```

- Solutions:

  Warnings not listed above that include an RPC error code `Internal` indicate unexpected issues in the CSI driver. Create a new [issue](https://github.com/GoogleCloudPlatform/lustre-csi-driver/issues) on the GitHub project page, including your GKE cluster version, detailed workload information, and the Pod event warning message in the issue.

## Volume Expansion Issues
This guide provides solutions for common issues you might encounter with Lustre volume expansion

### Expansion Does Not Start / PVC Stuck in ExternalExpanding
**Observation:** You update the PVC's requested storage, but the PVC status doesn't change to `Resizing`, no new events related to resizing appear, or the PVC events only show `ExternalExpanding`.
```bash
kubectl describe pvc <pvc-name>
```

```
Events:
  Type    Reason             Age                From           Message
  ----    ------             ----               ----           -------
  Normal  ExternalExpanding  21m (x2 over 58m)  volume_expand  waiting for an external controller to expand this PVC
```

**Diagnosis:** This behavior usually indicates a problem with the `csi-external-resizer` sidecar that runs within the `lustre-csi-controller` pod. This controller runs on the GKE control plane and is responsible for managing the resizing operation.

**Mitigation:**
1.  Ensure that the `StorageClass` used to provision the Lustre volume has the field `allowVolumeExpansion` set to `true`.
```bash
kubectl get storageclass <storageclass-name> -o yaml
```

2.  Access the hosted master project and examine Lustre CSI controller logs for errors.
**Logging query example**
```
(resource.type="gce_instance" OR resource.type="container")
(
labels."compute.googleapis.com/resource_name":"<your-gke-control-plane-vm-name>"
)
logName="projects/<project-Id>/logs/lustre-csi-driver"
```

### Expansion Fails: Invalid Argument
**Observation:** The PVC enters a `Resizing` state, but eventually fails. A `VolumeResizeFailed` event appears on the PVC.

**Diagnosis:** Check the events on the PVC
```bash
kubectl describe pvc <pvc-name>
```

Look for an event message similar to `VolumeResizeFailed: rpc error: code = InvalidArgument desc = ...` followed by details from the Lustre backend. This typically means the requested size is not valid due to
*   Not being a multiple of the required step size for the tier.
*   Being below the minimum or above the maximum capacity for the tier.

**Mitigation**
*   Check the [Google Cloud Managed Lustre documentation](https://cloud.google.com/managed-lustre/docs/create-instance#performance-tiers) to find the valid step sizes and minimum/maximum sizes for your volume's performance tier.
*   Edit the PVC to request a valid storage size.
*   Monitor the PVC events again to confirm the resize process restarts.

### Expansion Fails: Internal Error
**Observation:** The PVC resize fails, and events show an `INTERNAL` error from the backend.

**Diagnosis:** Check PVC events
```bash
kubectl describe pvc <pvc-name>
```
```
Events:
  Type      Reason              Age          From             Message
  ----      ------              ----         ----             -------
  Warning   VolumeResizeFailed  64s          external-resizer lustre.csi.storage.gke.io  resize volume "pvc-66164549-a645-4a79-9d7b-4fed54da8556" by resizer "lustre.csi.storage.gke.io" failed: rpc error: code = Internal desc = rpc error: code = Internal desc = an internal error has occurred
```

Look for `VolumeResizeFailed` with `code = Internal`. The event message may contain more details from the Lustre backend. Access Lustre CSI Controller logs for Internal errors.
```
(resource.type="gce_instance" OR resource.type="container")
(
labels."compute.googleapis.com/resource_name":"<your-gke-control-plane-vm-name>"
)
logName="projects/<project-id>/logs/lustre-csi-driver"

`"ControllerExpandVolume"`
`"Internal"`
```

**Mitigation:**
*   Retry the expansion by applying the PVC again with the same desired size. This can sometimes resolve transient backend issues.
*   If retrying fails, contact Google Cloud Support. Please provide the error messages, as manual intervention may be necessary.

### Expansion In-progress or stuck: Deadline Exceeded
**Observation:** You might see PVC event `VolumeResizeFailed` with `DEADLINE_EXCEEDED`.

**Diagnosis:**
```bash
kubectl describe pvc <pvc-name>
```

Events might show the `external-resizer` timing out, but the underlying Lustre operation is generally still in progress. The error message "Expansion for volume {Id} to new capacity {cap} GiB is already in progress" will appear on retries.

**Mitigation:**
*   The `external-resizer` will automatically retry. Continue monitoring the PVC events.
*   Check logs in the hosted master project to monitor the expansion process.
```
(resource.type="gce_instance" OR resource.type="container")
(
labels."compute.googleapis.com/resource_name":"<your-gke-control-plane-vm-name>"
)
logName="projects/<project-id>/logs/lustre-csi-driver"

`"ControllerExpandVolume" OR "<pvc-name>"`
```

If the operation succeeds on a retry, a `VolumeResizeSuccessful` event will appear.
If it remains stuck for more than 30 mins for smaller expansions or more than 90 mins for large capacity increases, contact Google Cloud Support.

### Expansion Fails: Lustre System Quota Exceeded
**Observation:** Expansion failed indicating a quota issue on the Lustre backend.

**Mitigation:**
*   Request a smaller capacity increase for the PVC.
*   Contact Support to discuss options for increasing overall Lustre service quotas or capacity.
