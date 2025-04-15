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

# Creating and Connecting to a New Lustre Instance - Dynamic Provisioning User Guide

This guide provides a simple example of how to use the Lustre CSI driver with dynamic provisioning. Dynamic provisioning allows you to create storage backed by Google Cloud Managed Lustre instances on demand and use them as volumes for stateful workloads.

## Prerequisites

Ensure that you have followed the [PSA User Guide](https://cloud.google.com/managed-lustre/docs/vpc) to set up PSA for your VPC network.

## Creating a New Volume Using the Lustre CSI Driver

### 1. (Optional) Create a StorageClass

If you followed the [installation guide](installation.md#install) to install the CSI driver, a StorageClass named `lustre-rwx` should already exist in your cluster. Alternatively, you can create a custom StorageClass with specific parameters. The Lustre CSI driver supports the following parameters:

- **network**: (Optional) The VPC network where the Lustre instance will be created. If not specified, the network of the GKE cluster will be used.
  To create a Lustre instance in a shared VPC network, provide the full network name, e.g.,
  `projects/<host-project-id>/global/networks/<vpc-network-name>`.

- **filesystem**: (Optional) The filesystem name for the Lustre instance. It must be an alphanumeric string (up to 8 characters), beginning with a letter.
  If not provided, the CSI driver will automatically generate a filesystem name in the format `lfs<NNNNN>` (e.g., `lfs97603`).

  > **Note:** If you want to mount multiple Lustre instances on the same node, it is recommended to create a separate StorageClass for each instance, and ensuring a unique filesystem name for each. This is necessary because the filesystem name must be unique on each node.

- **labels**: (Optional) A set of key-value pairs to assign labels to the Managed Lustre instance.

- **description**: (Optional) A description of the instance (2048 characters or less).

### 2. Create a PersistentVolumeClaim (PVC)

Apply the example PVC configuration:

```bash
kubectl apply -f ./examples/dynamic-prov/dynamic-pvc.yaml
```

### 3. Verify the PVC and PV Are Bound

Check that the PVC has been successfully bound to a PV:

```bash
kubectl get pvc
```

Expected output:

```bash
NAME         STATUS   VOLUME                                     CAPACITY   ACCESS MODES   STORAGECLASS   VOLUMEATTRIBUTESCLASS   AGE
lustre-pvc   Bound    pvc-be98607a-7a37-40b7-b7d7-28c9adce7b77   16Ti       RWX            lustre-rwx     <unset>                 24s
```

## Using the Persistent Volume in a Pod

### 1. Deploy the Pod

```bash
kubectl apply -f ./examples/dynamic-prov/dynamic-pod.yaml
```

### 2. Verify the Pod Is Running

It may take a few minutes for the Pod to reach the `Running` state:

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

> **Note:** The PV is created with a `"Delete"` `persistentVolumeReclaimPolicy`, meaning that deleting the PVC will also delete the PV and the underlying Lustre instance.

```bash
kubectl delete pod lustre-pod
kubectl delete pvc lustre-pvc
```

### 2. Verify That the PV Has Been Deleted

```bash
kubectl get pv
```

Expected output:

```bash
No resources found
```

> **Note:** It may take a few minutes for the underlying Lustre instance to be fully deleted.
