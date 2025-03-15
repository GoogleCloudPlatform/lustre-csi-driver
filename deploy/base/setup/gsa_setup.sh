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

# Set up the GCP service account for OSS driver.

set -x
set -o nounset
set -o errexit

GSA_DIR="${GSA_DIR:-$HOME}"
GSA_FILE="$GSA_DIR/lustre_csi_driver_sa.json"
GSA_NAME=lustre-csi-driver-sa
GSA_IAM_NAME="$GSA_NAME@$PROJECT.iam.gserviceaccount.com"

gcloud projects remove-iam-policy-binding "$PROJECT" --member serviceAccount:"$GSA_IAM_NAME" --role roles/lustre.admin || true
gcloud iam service-accounts delete "$GSA_IAM_NAME" --quiet || true

gcloud iam service-accounts create "$GSA_NAME"
gcloud projects add-iam-policy-binding "$PROJECT" --member serviceAccount:"$GSA_IAM_NAME" --role roles/lustre.admin

# Enable lustre API for this project.
gcloud services enable lustre.googleapis.com

# Cleanup old service account and key
if [ -f "$GSA_FILE" ]; then
rm "$GSA_FILE"
fi
# Create new service account and key
gcloud iam service-accounts keys create "$GSA_FILE" --iam-account "$GSA_IAM_NAME"