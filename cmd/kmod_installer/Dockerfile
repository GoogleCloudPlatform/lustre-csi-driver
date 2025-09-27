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
FROM --platform=$BUILDPLATFORM google-go.pkg.dev/golang:1.25.1@sha256:2a5741ae38c60d188ae9144d1b63730b8059d016216e785b9b35acf1ace69bc1 AS builder

ARG STAGINGVERSION
ARG TARGETARCH

WORKDIR /go/src/github.com/GoogleCloudPlatform/lustre-csi-driver
ADD . .
RUN make kmod-installer GOARCH=${TARGETARCH} BINDIR=/bin

FROM gke.gcr.io/debian-base:bookworm-v1.0.5-gke.11 AS debian

# Set non-interactive mode for apt to prevent prompts
ENV DEBIAN_FRONTEND=noninteractive

# Install required dependencies
RUN clean-install bash
RUN apt-get update && apt-get install -y --no-install-recommends \
    kmod \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

COPY --from=gcr.io/cos-cloud/cos-dkms:v0.3.5 /usr/bin/cos-dkms /usr/bin/cos-dkms


FROM debian
ARG KMOD_INSTALLER_BINARY=lustre-kmod-installer
COPY --from=builder /bin/${KMOD_INSTALLER_BINARY} /${KMOD_INSTALLER_BINARY}
ENTRYPOINT ["/lustre-kmod-installer"]
