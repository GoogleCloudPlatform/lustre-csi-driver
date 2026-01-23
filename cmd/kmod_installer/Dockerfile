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

FROM gke.gcr.io/debian-base:bookworm-v1.0.6-gke.1

# Set non-interactive mode for apt to prevent prompts
ENV DEBIAN_FRONTEND=noninteractive

# Install required dependencies
RUN clean-install bash
RUN apt-get update && apt-get install -y --no-install-recommends \
    kmod \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

COPY --from=gcr.io/cos-cloud/cos-dkms:v0.3.9 /usr/bin/cos-dkms /usr/bin/cos-dkms

COPY /cmd/kmod_installer/entrypoint.sh /usr/bin/entrypoint.sh
RUN chmod +x /usr/bin/entrypoint.sh

CMD ["/usr/bin/entrypoint.sh"]
