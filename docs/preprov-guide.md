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

If you haven't already provisioned a Google Cloud Managed Lustre instance, follow the instructions [here](https://cloud.google.com/managed-lustre/docs/create-instance) to create one.

## Creating a Persistent Volume for a Lustre Instance

### **Prerequisite**

Before applying the Persistent Volume (PV) and Persistent Volume Claim (PVC) manifest, update `./examples/pre-prov/preprov-pvc-pv.yaml` with the correct values:

- **`volumeHandle`**: Update with the correct **project ID, zone, and Lustre instance name**.
- **`storage`**: This value should match the size of the underlying Lustre instance.
- **`volumeAttributes`**:
  - `ip` must point to the Lustre instance IP.
  - `filesystem` must be the Lustre instance's filesystem name.

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
preprov-pvc   Bound    preprov-pv   16Ti           RWX                        76s
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
preprov-pv   16Ti      RWX            Retain           Released   default/preprov-pvc                           2m28s
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
preprov-pv   16Ti      RWX            Retain           Available                                   19m
```

### 4. Delete the PV (If No Longer Needed)

If the PV is no longer needed, delete it.

> **Note:** Deleting the PV does **not** remove the underlying Lustre instance.

```bash
kubectl delete pv preprov-pv
```
