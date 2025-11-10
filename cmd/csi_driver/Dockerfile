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

FROM --platform=$BUILDPLATFORM google-go.pkg.dev/golang:1.25.4@sha256:3f90cabe0630565f75de1f7cdb0898bf9e39ff4b52df1d1e21aff75b87e68fde AS driverbuilder

ARG STAGINGVERSION
ARG TARGETARCH

WORKDIR /go/src/github.com/GoogleCloudPlatform/lustre-csi-driver
ADD . .
RUN make driver GOARCH=${TARGETARCH} BINDIR=/bin

FROM gke.gcr.io/debian-base:bookworm-v1.0.6-gke.1 AS debian
ENV DEBIAN_FRONTEND=noninteractive
ARG TARGETPLATFORM

# Install necessary dependencies and tools.
RUN clean-install bash

RUN apt-get update && apt-get install -y --no-install-recommends \
    apt \
    kmod \
    dpkg \
    libyaml-0-2 \
    libnl-3-200 \
    libnl-genl-3-200 \
    libkeyutils1 \
    libreadline8 \
    krb5-locales \
    libgssapi-krb5-2 \
    libk5crypto3 \
    libkrb5-3 \
    libkrb5support0 \
    libssl3 \
    libjson-c5 \
    ca-certificates && \
    rm -rf /var/lib/apt/lists/*

COPY --from=gcr.io/cos-cloud/cos-dkms:v0.3.5 /usr/bin/cos-dkms /usr/bin/cos-dkms

COPY /bin/lustre/$TARGETPLATFORM/lustre-client.deb /lustre/lustre-client.deb

# Install Lustre client utilities.
RUN dpkg -i /lustre/lustre-client.deb || apt-get -f install -y

# Verify installation
RUN dpkg-query -l | grep lustre || (echo "ERROR: Lustre client not installed!" && exit 1)

# Clean up temporary files.
RUN rm -rf /lustre

FROM debian
ARG DRIVER_BINARY=lustre-csi-driver
COPY --from=driverbuilder /bin/${DRIVER_BINARY} /${DRIVER_BINARY}
ENTRYPOINT ["/lustre-csi-driver"]
