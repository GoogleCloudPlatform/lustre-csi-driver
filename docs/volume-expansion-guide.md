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

# Lustre Volume Expansion User Guide

>**Attention:** Volume Resize is a Kubernetes Beta feature enabled by default in 1.16+.

### Expansion Example

This example dynamically provisions a Lustre instance and performs online expansion of the instance (i.e while the volume is mounted on a Pod). For more details on the CSI VolumeExpansion capability see [here](https://kubernetes-csi.github.io/docs/volume-expansion.html)

1. Ensure the `allowVolumeExpansion` field is set to true in the Storage Class definition
    ```yaml
    apiVersion: storage.k8s.io/v1
    kind: StorageClass
    metadata:
    name: lustre-rwx
    provisioner: lustre.csi.storage.gke.io
    volumeBindingMode: WaitForFirstConsumer
    reclaimPolicy: Delete
    allowVolumeExpansion: true
    allowedTopologies:
    - matchLabelExpressions:
      - key: topology.gke.io/zone
        values:
          - us-central1-c
    parameters:
      perUnitStorageThroughput: "1000"
      network: projects/${PROJECT_ID}/global/networks/${NETWORK_NAME}
    ```

2. Create the Storage Class with volume expansion field enabled
    ```
    $ kubectl apply -f <path-to-storage-class-file>
    ```

3. Create example PVC and Pod. Note that the volume size in this example is 9000 GiB.
    ```
    $ kubectl apply -f ./examples/dynamic-prov/dynamic-pvc.yaml
    ```

    ```
    $ kubectl apply -f ./examples/dynamic-prov/dynamic-pod.yaml
    ```

4. Verify PV is created and bound to PVC
    ```
    $ kubectl get pvc lustre-pvc
    NAME         STATUS   VOLUME      CAPACITY   ACCESS MODES   STORAGECLASS   VOLUMEATTRIBUTESCLASS   AGE
    lustre-pvc   Bound    <PV_NAME>   9000Gi     RWX            lustre-rwx     <unset>                 26m
    ```

5. Verify pod is created and is in `RUNNING` state (it may take up to 30 minutes or more to reach running state, depending on the volume size)
    ```
    $ kubectl get pods
    NAME         READY   STATUS    RESTARTS   AGE
    lustre-pod   1/1     Running   0          26m
    ```

6. Check Lustre instance size on the running pod
    ```
    $ kubectl exec lustre-pod -- df -h /lustre_volume
    Filesystem                 Size  Used Avail Use% Mounted on
    <Instance IP>@tcp:/lfs51954  8.7T   11M  8.6T   1% /lustre_volume
    ```

7. Get the zone information from the `volumeHandle` of PV spec
    ```console  
    $ kubectl get pv <PV_NAME> -o yaml
    ```
    ```yaml
    apiVersion: v1
    kind: PersistentVolume
    metadata:
    annotations:
      pv.kubernetes.io/provisioned-by: lustre.csi.storage.gke.io
      volume.kubernetes.io/provisioner-deletion-secret-name: ""
      volume.kubernetes.io/provisioner-deletion-secret-namespace: ""
    creationTimestamp: "2025-10-27T23:02:27Z"
    finalizers:
    - external-provisioner.volume.kubernetes.io/finalizer
    - kubernetes.io/pv-protection
    name: <PV_NAME>
    resourceVersion: "89077"
    uid: 8e5b6022-210d-449c-a91a-9d1a0d49427e
    spec:
    accessModes:
    - ReadWriteMany
    capacity:
      storage: 9000Gi
    claimRef:
      apiVersion: v1
      kind: PersistentVolumeClaim
      name: lustre-pvc
      namespace: default
      resourceVersion: "73594"
      uid: 49777948-561d-4361-b7ee-162c6ca7be78
    csi:
      driver: lustre.csi.storage.gke.io
      volumeAttributes:
      filesystem: lfs51954
      ip: <Instance IP>
      storage.kubernetes.io/csiProvisionerIdentity: 1761597905856-8018-lustre.csi.storage.gke.io
      volumeHandle: ${PROJECT_ID}/us-central1-c/<PV_NAME>
    persistentVolumeReclaimPolicy: Delete
    storageClassName: lustre-rwx
    volumeMode: Filesystem
    status:
      lastPhaseTransitionTime: "2025-10-27T23:02:27Z"
      phase: Bound
    ```

8. Check the Lustre instance properties

    ```console
    $ gcloud lustre instances describe <PV_NAME> --location=us-central1-c
    ```

    ```yaml
    capacityGib: '9000'
    createTime: '2025-10-27T22:38:13.236544672Z'
    filesystem: lfs51954
    labels:
      kubernetes_io_created-for_pv_name: <PV_NAME>
      kubernetes_io_created-for_pvc_name: lustre-pvc
      kubernetes_io_created-for_pvc_namespace: default
    storage_gke_io_created-by: lustre_csi_storage_gke_io
    mountPoint: 10.97.160.4@tcp:/lfs51954
    name: projects/${PROJECT_ID}/locations/us-central1-c/instances/<PV_NAME>
    network: projects/${PROJECT_ID}/global/networks/${NETWORK_NAME}
    perUnitStorageThroughput: '1000'
    state: ACTIVE
    uid: d2d5cf79-6bd0-41e9-a43f-a642bc37c9b3
    updateTime: '2025-10-27T23:01:51.109896688Z'
    ```

9. Expand volume by modifying the field `spec -> resources -> requests -> storage` in PVC. Refer to the Managed Lustre performance tiers [documentation](https://cloud.google.com/managed-lustre/docs/create-instance#performance-tiers) to select a new storage size based on the throughput.
    
    ```
    $ kubectl edit pvc lustre-pvc
    apiVersion: v1
    kind: PersistentVolumeClaim
    ...
    spec:
      resources:
        requests:
          storage: 18000Gi
      ...
    ...
    ```

10. Observe the status of the PVC and associated events to track the expansion process.

    ```console
    $ kubectl describe pvc lustre-pvc
    ```
    Look for:
    *   **Conditions:** The PVC will likely have a condition with type: `Resizing`.
    *   **Events:**
        *   `ExternalExpanding`: Indicates waiting for an external controller (external-resizer) to expand the PVC.
        *   `Resizing`: Indicates resize operation is in progress. This can take time, potentially up to 90 minutes for large expansions.
        *   `VolumeResizeSuccessful`: Confirms the volume has been successfully expanded. The `pvc.status.capacity` will update.
        *   `VolumeResizeFailed`: Indicates an error occurred. The event message will contain details from the Lustre backend, e.g., if the requested size is invalid due to step size constraints or tier limits. (Note that this doesn’t indicate a hard failure in all cases)

11. Once the expansion is complete, confirm that the new storage size is reflected in PV and PVC
    ```
    $ kubectl get pvc lustre-pvc
    NAME         STATUS   VOLUME       CAPACITY   ACCESS MODES   STORAGECLASS   VOLUMEATTRIBUTESCLASS   AGE
    lustre-pvc   Bound    <PV_NAME>    18000Gi    RWX            lustre-rwx     <unset>                 55m

    $ kubectl get pv <PV_NAME>
    NAME        CAPACITY   ACCESS MODES   RECLAIM POLICY   STATUS   CLAIM                STORAGECLASS   VOLUMEATTRIBUTESCLASS   REASON   AGE
    <PV_NAME>   18000Gi    RWX            Delete           Bound    default/lustre-pvc   lustre-rwx     <unset>                          31m
    ```

12. Confirm that the filesystem is resized on the running pod
    ```
    $  kubectl exec lustre-pod -- df -h /lustre_volume
    Filesystem               Size  Used Avail Use% Mounted on
    <Instance IP>/lfs51954   18T   23M   18T   1% /lustre_volume
    ```

13. Verify the updated properties of the Lustre instance

    ```console
    $ gcloud lustre instances describe <PV_NAME> --location us-central1-c
    ```

    ```yaml
    capacityGib: '18000' # New storage size
    createTime: '2025-10-27T22:38:13.236544672Z'
    filesystem: lfs51954
    labels:
      kubernetes_io_created-for_pv_name: <PV_NAME>
      kubernetes_io_created-for_pvc_name: lustre-pvc
      kubernetes_io_created-for_pvc_namespace: default
    storage_gke_io_created-by: lustre_csi_storage_gke_io
    mountPoint: 10.97.160.4@tcp:/lfs51954
    name: projects/${PROJECT_ID}/locations/us-central1-c/instances/<PV_NAME>
    network: projects/${PROJECT_ID}/global/networks/${NETWORK_NAME}
    perUnitStorageThroughput: '1000'
    state: ACTIVE
    uid: d2d5cf79-6bd0-41e9-a43f-a642bc37c9b3
    updateTime: '2025-10-27T23:32:05.070843105Z'
    ```