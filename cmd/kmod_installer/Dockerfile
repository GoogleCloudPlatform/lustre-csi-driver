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

FROM --platform=$BUILDPLATFORM google-go.pkg.dev/golang:1.26.2@sha256:1bee769a7a50eea7730ac31f75182ae2614f50a70902407312db390a7c7cb2ff AS builder

ARG STAGINGVERSION
ARG TARGETARCH

WORKDIR /go/src/github.com/GoogleCloudPlatform/lustre-csi-driver
ADD . .
RUN make kmod_installer GOARCH=${TARGETARCH} BINDIR=/bin

FROM gke.gcr.io/debian-base:bookworm-v1.0.7-gke.2
ENV DEBIAN_FRONTEND=noninteractive

RUN clean-install bash
RUN apt-get update && apt-get install -y --no-install-recommends \
    kmod \
    openssl \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

COPY --from=gcr.io/cos-cloud/cos-dkms:v0.3.12 /usr/bin/cos-dkms /usr/bin/cos-dkms
COPY --from=builder /bin/lustre-kmod-installer /usr/bin/lustre-kmod-installer

ENTRYPOINT ["/usr/bin/lustre-kmod-installer"]
