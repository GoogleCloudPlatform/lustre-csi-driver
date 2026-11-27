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

# Importing an Existing Lustre Instance - User Guide

This guide provides a simple example of how to use the Lustre CSI driver to import and connect to an existing Lustre instance that has been pre-provisioned by an administrator.

## Importing a Lustre Instance as a Persistent Volume

If you haven't already provisioned a Google Cloud Managed Lustre instance, please follow the [instructions](https://cloud.google.com/managed-lustre/docs/create-instance) to create one. Make sure to specify the `gke-support-enabled` flag when creating the instance.

## Creating a Persistent Volume for a Lustre Instance

### **Prerequisite**

Before applying the Persistent Volume (PV) and Persistent Volume Claim (PVC) manifest, update `./examples/pre-prov/preprov-pvc-pv.yaml` with the correct values:

- **`volumeHandle`**: Update with the correct **project ID, zone, and Lustre instance name**.
- **`storage`**: This value should match the size of the underlying Lustre instance.
- **`volumeAttributes`**:
  - `ip` must point to the Lustre instance IP.
  - `filesystem` must be the Lustre instance's filesystem name.
  - Alternatively, you can use `mountpoint` which combines both (e.g., `10.108.80.4@tcp:/fs`). If `mountpoint` is provided, it will override `ip` and `filesystem`.

### 1. Create a Persistent Volume (PV) and Persistent Volume Claim (PVC)

Apply the example PV and PVC configuration:

```bash
kubectl apply -f ./examples/pre-prov/preprov-pvc-pv.yaml
```

### 2. Verify that the PVC and PV are bound

```bash
kubectl get pvc
```

Expected output:

```bash
NAME          STATUS   VOLUME        CAPACITY   ACCESS MODES   STORAGECLASS   AGE
preprov-pvc   Bound    preprov-pv    9000Gi         RWX                      76s
```

## Using the Persistent Volume in a Pod

### 1. Deploy the Pod

```bash
kubectl apply -f ./examples/pre-prov/preprov-pod.yaml
```

### 2. Verify that the Pod is running

It may take up to a few minutes for the Pod to reach the `Running` state:

```bash
kubectl get pods
```

Expected output:

```bash
NAME           READY   STATUS    RESTARTS   AGE
lustre-pod     1/1     Running   0          11s
```

## Cleaning Up

### 1. Delete the Pod and PVC

Once you've completed your experiment, delete the Pod and PVC.

> **Note:** The PV was created with a `"Retain"` `persistentVolumeReclaimPolicy`, meaning that deleting the PVC will not remove the PV or the underlying Lustre instance.

```bash
kubectl delete pod lustre-pod
kubectl delete pvc preprov-pvc
```

### 2. Check the PV status

After deleting the Pod and PVC, the PV should report a `Released` state:

```bash
kubectl get pv
```

Expected output:

```bash
NAME        CAPACITY   ACCESS MODES   RECLAIM POLICY   STATUS     CLAIM                 STORAGECLASS   REASON   AGE
preprov-pv   9000Gi      RWX            Retain        Released   default/preprov-pvc                           2m28s
```

### 3. Reuse the PV

To reuse the PV, remove the claim reference (`claimRef`):

```bash
kubectl patch pv preprov-pv --type json -p '[{"op": "remove", "path": "/spec/claimRef"}]'
```

The PV should now report an `Available` status, making it ready to be bound to a new PVC:

```bash
kubectl get pv
```

Expected output:

```bash
NAME        CAPACITY   ACCESS MODES   RECLAIM POLICY   STATUS      CLAIM   STORAGECLASS   REASON   AGE
preprov-pv   9000Gi      RWX           Retain         Available                                   19m
```

### 4. Delete the PV (If No Longer Needed)

If the PV is no longer needed, delete it.

> **Note:** Deleting the PV does **not** remove the underlying Lustre instance.

```bash
kubectl delete pv preprov-pv
```

## Sub-directory (Fileset) Mounts and Project Quotas

For multi-tenant clusters sharing a single Managed Lustre instance, administrators can mount pre-existing Lustre sub-directories directly via `PersistentVolume.spec.csi.volumeAttributes.subPath` (or the `subDir` alias). This executes a native Lustre client fileset mount (`<IP>@tcp:/<fsname>/<subPath>`) on the node instead of mounting the entire filesystem root.

### Prerequisites and Directory Setup

The target sub-directory **must already exist** on the Lustre filesystem before a Pod mounts the PV. The CSI driver does not dynamically create or delete sub-directories.

From an administrative node with the Lustre root mounted at `/mnt/lustre`:

1. **Create the sub-directory** (nested paths such as `team-alpha/logs` are supported and map to `<IP>@tcp:/<fsname>/team-alpha/logs`):

   ```bash
   sudo mkdir -p /mnt/lustre/team-alpha
   ```

2. **Pre-assign a Lustre Project ID (Recommended for Multi-Tenant Quotas):**

   Assign a unique numeric Lustre Project ID (`1`–`4294967295`, distinct from your Google Cloud Project ID) with recursive (`-r`) and directory inheritance (`-s`) flags so all files and nested folders created under that sub-directory are accounted against the same quota:

   ```bash
   sudo lfs project -p 101 -r -s /mnt/lustre/team-alpha
   ```

3. **Configure Block and Inode Quotas:**

   Set soft (`-b`/`-i`) and hard (`-B`/`-I`) limits for the assigned Lustre Project ID:

   ```bash
   # Example: 8 TiB soft block limit, 9 TiB hard block limit,
   #          5M soft inode limit, 6M hard inode limit
   sudo lfs setquota -p 101 -b 8T -B 9T -i 5000000 -I 6000000 /mnt/lustre
   ```

   You can inspect project quota usage and limits with:

   ```bash
   sudo lfs quota -p 101 /mnt/lustre
   ```

### How `statfs_project` Reflects Tenant Storage Limits

When `subPath` (or `subDir`) is specified in `volumeAttributes`, the CSI driver automatically appends the `statfs_project` mount option to the Lustre fileset mount (preserving any user-configured `spec.mountOptions` without duplicating `statfs_project` if already supplied).

- **With a Project Quota Assigned:** `df -h`, `df -i`, and Kubelet `NodeGetVolumeStats` metrics (`statfs` calls against the mounted volume path) report the sub-directory's project quota block and inode capacity rather than the global Lustre filesystem capacity. Once usage reaches the hard limit (or exceeds the soft limit beyond the grace period), writes fail with `EDQUOT` (`Disk quota exceeded`) while other tenants remain unaffected.
- **Without a Project Quota Assigned:** If no project ID or quota is configured for the sub-directory, the Lustre kernel client gracefully falls back to reporting the global filesystem capacity without error.

### `subPath` Validation Rules

The CSI driver validates and normalizes `subPath` (and `subDir`) in `ValidateVolumeCapabilities`, `NodeStageVolume`, and `NodePublishVolume`:

- **Slash normalization:** A single leading or trailing slash (e.g., `/team-alpha/app1/`) is normalized to a relative path (`team-alpha/app1`).
- **Rejected inputs:** Empty strings, root `/`, extra leading or trailing slashes (`//team-alpha`, `team-alpha//`), empty segments (`team-alpha//app1`), relative traversal segments (`.` or `..`, e.g., `../team-beta` or `team-alpha/../team-beta`), null bytes, and conflicting `subPath` / `subDir` values are rejected with `InvalidArgument`.
- **Missing directory guardrail:** If the sub-directory does not exist on the Lustre server, mounting fails fast with `FailedPrecondition` (`subPath "<path>" does not exist on filesystem "<filesystem>"`).

### Static PV Configuration and `DeleteVolume` Behavior

See `./examples/pre-prov/preprov-subpath-pvc-pv.yaml` and `./examples/pre-prov/preprov-subpath-pod.yaml` for a complete multi-tenant example:

- **`volumeHandle`:** Append a unique `:<suffix>` per PV (e.g., `<project-id>/<location>/<instance-name>:team-alpha`) so Kubernetes does not coalesce separate PVs targeting the same Lustre instance.
- **`claimRef` (Recommended):** Pre-bind each static PV to its tenant namespace and PVC name.
- **Reclaim Policy & `DeleteVolume` Handling:** Always set `persistentVolumeReclaimPolicy: Retain`. Static PVs with `Retain` do not invoke `DeleteVolume` when the PVC is deleted. Because `ParseVolumeID` strips the `:<suffix>` when resolving the underlying Lustre instance, setting `persistentVolumeReclaimPolicy: Delete` (or invoking `DeleteVolume` on a sub-directory PV) would delete the **entire Managed Lustre instance** rather than the sub-directory.

### Migrating from Pod `volumeMounts.subPath` to PV `subPath`

Because `PersistentVolume.spec.csi` is immutable, migrate existing workloads without moving data by provisioning new sub-directory PVs/PVCs alongside the legacy root PV/PVC:

1. Ensure the target directories already exist on Lustre (and optionally assign project IDs and quotas via `lfs project` / `lfs setquota`).
2. Create a new static PV (with a unique `volumeHandle` suffix and `volumeAttributes.subPath`) and a new PVC in the workload namespace.
3. Update your Pod/Deployment spec to reference the new PVC and remove Pod-level `volumeMounts[].subPath`, then roll out via a rolling update.

> **Important (Concurrent Root and Sub-directory Mounts on the Same Node):**
> During migration, avoid scheduling legacy root-PV Pods and new `subPath`-PV Pods for the same Lustre filesystem on the same node at the same time. Mount behavior on a single node is order-dependent:
> - If a `subPath` volume is mounted on a node first, a subsequent legacy root-PV `NodeStageVolume` call on that node sees the same `fsname` in `/proc/mounts` and fails with `AlreadyExists`.
> - If a legacy root-PV volume is staged first, `subPath` volumes bypass `NodeStageVolume` and mount directly, meaning both share the same node lifecycle.
> Isolate migration workloads onto separate nodes/node pools or drain legacy Pods from a node before scheduling `subPath` Pods onto it.
