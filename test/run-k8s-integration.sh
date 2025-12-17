#!/bin/bash

# Copyright 2025 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     https://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -o nounset
set -o errexit

readonly SCRIPTDIR="$( realpath -s "$(dirname "$BASH_SOURCE"[0])" )"
readonly PKGDIR="$( realpath -s "$(dirname "$SCRIPTDIR")" )"
echo "PKGDIR is $PKGDIR"

readonly boskos_resource_type="${BOSKOS_RESOURCE_TYPE:-gke-internal-project}"
readonly do_driver_build="${DO_DRIVER_BUILD:-false}"
readonly gce_zone=${GCE_CLUSTER_ZONE:-us-central1-a}
readonly gke_cluster_version=${GKE_CLUSTER_VERSION:-latest}
readonly gke_node_version=${GKE_NODE_VERSION:-}
readonly gce_region=${GCE_CLUSTER_REGION:-}
readonly use_gke_driver=${USE_GKE_DRIVER:-true}
readonly parallel_run=${PARALLEL:-}
readonly test_focus=${TEST_FOCUS:-}
readonly test_version=${TEST_VERSION:-1.32}
readonly enable_legacy_lustre_port=${ENABLE_LEGACY_LUSTRE_PORT:-true}

make -C "${PKGDIR}" test-k8s-integration

# Choose an older Kubetest2 commit version instead of using @latest
# because of a regression in https://github.com/kubernetes-sigs/kubetest2/pull/183.
# Contact engprod oncall and ask about what is the version they are using for internal jobs.
go install sigs.k8s.io/kubetest2@22d5b1410bef09ae679fa5813a5f0d196b6079de;
go install sigs.k8s.io/kubetest2/kubetest2-gke@22d5b1410bef09ae679fa5813a5f0d196b6079de;
go install sigs.k8s.io/kubetest2/kubetest2-tester-ginkgo@22d5b1410bef09ae679fa5813a5f0d196b6079de;

echo "make successful"
base_cmd="${PKGDIR}/bin/k8s-integration-test \
            --run-in-prow=true --service-account-file=${E2E_GOOGLE_APPLICATION_CREDENTIALS} \
            --do-driver-build=${do_driver_build} --boskos-resource-type=${boskos_resource_type} \
            --test-version=${test_version} --num-nodes=1 --multi-nic-num-nodes=3 --pkg-dir=${PKGDIR} \
            --use-gke-driver=${use_gke_driver} --gke-cluster-version=${gke_cluster_version} \
            --enable-legacy-lustre-port=${enable_legacy_lustre_port}

if [ -n "$gke_node_version" ]; then
  base_cmd="${base_cmd} --gke-node-version=${gke_node_version}"
fi

if [ -z "$gce_region" ]; then
  base_cmd="${base_cmd} --gce-zone=${gce_zone}"
else
  base_cmd="${base_cmd} --gce-region=${gce_region}"
fi

if [ -n "$parallel_run" ]; then
  base_cmd="${base_cmd} --parallel=${parallel_run}"
fi

if [ -n "$test_focus" ]; then
  base_cmd="${base_cmd} --test-focus=${test_focus}"
fi

echo "$base_cmd"
eval "$base_cmd"