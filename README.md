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

# Lustre CSI Driver

The Google Cloud Managed Lustre Container Storage Interface (CSI) Plugin.

> WARNING: Manual deployment of the driver is intended for test environments only. DO NOT use it in production clusters. Instead, customers should rely on GKE to automatically deploy and manage the CSI driver as an add-on feature. For more information, please refer to the [GKE-managed Lustre CSI driver documentation](https://cloud.google.com/kubernetes-engine/docs/concepts/managed-lustre).

> DISCLAIMER: Manual deployment of the driver to your cluster is not officially supported by Google.

## Project Overview

The Lustre CSI driver allows k8s workloads (a group of k8s pods) to connect to Google Cloud Managed Lustre instances - transparently through the Kubernetes Persistent Volume Claims (PVCs) API. The Lustre CSI driver supports creating and deleting Google Cloud Managed Lustre instances, as well as mounting and unmounting them on GKE nodes in response to CSI calls, ensuring transparent and efficient integration with GKE environments.

## Project Status

Status: General Availability

## Get Started

* [Lustre CSI Driver Manual Installation](./docs/installation.md)
* [Creating and Connecting to a New Lustre Instance](./docs/dynamic-prov-guide.md)
* [Importing an Existing Lustre Instance](./docs/preprov-guide.md)
* [Troubleshooting](./docs/troubleshooting.md)

## Development and Contribution

Refer to the [Lustre CSI Driver Development Guide](./docs/development.md).
