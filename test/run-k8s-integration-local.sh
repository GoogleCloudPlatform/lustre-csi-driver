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

readonly SCRIPTDIR="$( realpath -s "$(dirname "$BASH_SOURCE"[0])" )"
readonly PKGDIR="$( realpath -s "$(dirname "$SCRIPTDIR")" )"
echo "PKGDIR is $PKGDIR"

# Some commonly run subset of tests focus strings.
all_external_tests_focus="External.*Storage"
subpath_test_focus="External.*Storage.*default.*fs.*subPath"
multivolume_fs_test_focus="External.*Storage.*filesystem.*multiVolume"

# E.g. run command: test/run-k8s-integration-local.sh | tee log
"${PKGDIR}"/bin/k8s-integration-test --run-in-prow=false --pkg-dir="${PKGDIR}" \
--bringup-cluster=true --test-focus="${all_external_tests_focus}" --do-network-setup=true \
--do-driver-build=true --gce-zone="us-central1-c" --use-gke-driver=false \
--num-nodes="${NUM_NODES:-3}" --parallel=1 -test-version=1.32 --gke-cluster-version=1.32
