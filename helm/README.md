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

# Install the Lustre CSI Driver via Helm

The CSI driver can be deployed using Helm with pre-built images. Currently, this approach supports only [static provisioning (importing an existing Lustre instance)](/docs/preprov-guide.md).

## Install Helm

```sh
curl -fsSL https://raw.githubusercontent.com/helm/helm/master/scripts/get-helm-3 | bash
```

## Install the CSI driver

1. Environment Setup

    ```sh
    export VERSION=1.0.0    # Set lustre-csi-driver image tag
    export NAMESPACE="lustre-csi-driver"
    ```

2. Service Account Setup

    To use the Lustre CSI driver with [dynamic provisioning](../docs/dynamic-prov-guide.md), you must set up a service account with the necessary permissions for the Lustre API and download its key. This service account enables the driver to provision Lustre instances and interact with Lustre APIs.

    - Specify a secure directory to store the service account key.
    ⚠️ **Do not make this directory publicly accessible!**

    ```sh
    export PROJECT=$(gcloud config get-value project 2>&1 | tail -n 1)
    export GSA_DIR=${HOME} # Directory to store credentials (default is home directory).
    ```

    - Run the [gsa_setup script](../deploy/base/setup/gsa_setup.sh) to configure the service account:

    ```sh
    ../deploy/base/setup/gsa_setup.sh
    ```

    - Create a Kubernetes secret from the service account key:

    ```sh
    kubectl create secret generic lustre-csi-driver-sa \
        --from-file=${GSA_DIR}/lustre_csi_driver_sa.json \
        --namespace=${NAMESPACE}
    ```

    > Note: After successfully creating the secret, make sure to securely remove the service account key file.
    > If the script returns an error like "Key creation is not allowed on this service account", it is likely due to an organization policy constraint preventing key creation. Contact your organization administrator to disable this restriction for your project.

3. Install the csi driver using helm

    ```sh
    helm upgrade -i lustre-csi-driver lustre-csi-driver \
      --namespace ${NAMESPACE} \
      --create-namespace \
      --set image.lustre.tag="$VERSION"
    ```

4. Check if the CSI driver is successfully installed

    The output from the below command should indicate that the DaemonSet and Pods are running as expected.

    ```sh
    kubectl get CSIDriver,Deployment,DaemonSet,Pods -n lustre-csi-driver
    ```

    ```text
    NAME                                                 ATTACHREQUIRED   PODINFOONMOUNT   STORAGECAPACITY   TOKENREQUESTS   REQUIRESREPUBLISH   MODES        AGE
    csidriver.storage.k8s.io/lustre.csi.storage.gke.io   false            false            false             <unset>         false               Persistent   28s

    NAME                    READY   UP-TO-DATE   AVAILABLE   AGE
    lustre-csi-controller   1/1     1            1           28s

    NAME                             DESIRED   CURRENT   READY   UP-TO-DATE   AVAILABLE   NODE SELECTOR            AGE
    daemonset.apps/lustre-csi-node   1         1         1       1            1           kubernetes.io/os=linux   28s

    NAME                                         READY   STATUS    RESTARTS   AGE
    pod/lustre-csi-controller-b4b7899c6-grx6n    3/3     Running   0          28s
    pod/lustre-csi-node-gqffs                    2/2     Running   0          28s
    ```

## Uninstall the CSI driver

```sh
helm uninstall lustre-csi-driver --namespace lustre-csi-driver
```
