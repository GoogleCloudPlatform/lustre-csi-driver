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

# Test

The repo has created Make commands for all the tests. Please clone the repo and run the following commands from the root level of the repo.

## Unit test

```bash
make unit-test
```

## Sanity test

```bash
make sanity-test
```

## E2E test

The E2E test for the Lustre CSI driver relies on the [k8s external e2e test suites](https://github.com/kubernetes/kubernetes/tree/master/test/e2e/storage/external). You can run it locally within your own GCP project. Below is an example of running the E2E test against a self-deployed Lustre CSI Driver.

1. Configure your gcloud:

    ```bash
    gcloud auth login
    gcloud config set <your-project>
    gcloud auth application-default login
    ```

2. Download kubetest2:

    ```bash
    go install sigs.k8s.io/kubetest2@latest
    go install sigs.k8s.io/kubetest2/kubetest2-gke@latest
    go install sigs.k8s.io/kubetest2/kubetest2-tester-ginkgo@latest
    ```

3. Download the service account for Lustre (**skip this step if you want to use a GKE managed Lustre CSI Driver**).

    ```bash
    export PROJECT=$(gcloud config get-value project 2>&1 | head -n 1)
    ./deploy/base/setup/gsa_setup.sh
    ```

4. Build the e2e test binary:

    ```bash
    make test-k8s-integration
    ```

5. Run the e2e test:

    ```bash
    test/run-k8s-integration-local.sh | tee log
    ```

You can control the test through the following parameters:

- `pkg-dir`: the absolute directory of the repo.
- `do-network-setup`: default value is `true`. Set it to `false` if you don't want to automatically setup and then cleanup a VPC network during the e2e test.
- `bringup-cluster`: default value is `true`. Set it to `false` if you want to use an existing GKE cluster.
- `test-version`: version of k8s binary to download and use for the e2e test. Example values include 1.29, 1.30, etc.
- `num-nodes`: default value is `-1`. The number of nodes in the test GKE cluster. You have to set it to a positive integer.
- `image-type`: default value is `cos_containerd`. The node image type to use for the cluster.
- `deploy-overlay-name`: default value is `dev`. Set the kustomize overlay name for manual installation.
- `do-driver-build`: default value is `false`. Set it to `true` if you want to build the driver from source, install the driver and uninstall it after the test.
- `use-gke-driver`: default value is `true`. Set it to `false` if you wish to test your local code changes using a self-deployed CSI Driver.
- `test-focus`: default value is `External.Storage`. Test focus for k8s external e2e tests.
- `parallel`: default value is `1`. The number of parallel tests setting for ginkgo parallelism.
- `gke-cluster-version`: GKE cluster version. This parameter only takes effect when `bringup-cluster` is set to `true`.
- `gke-node-version`: GKE node version. This parameter only takes effect when `bringup-cluster` is set to `true`.
- `gke-cluster-name`: GKE cluster name to be used in the test.
- `gce-zone`: GKE cluster zone for zonal cluster.
- `gce-region`: GKE cluster region for regional clusters.
- `cluster-network`: default value is `lustre-network`. The VPC network name to be used in the test.
