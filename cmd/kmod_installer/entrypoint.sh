#!/bin/sh

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

set -e

# Default to 6988 if LNET_PORT is not set.
LNET_PORT=${LNET_PORT:-6988}

# Check if LoadPin is already disabled.
# TODO: Remove this function once LoadPin excludes kernel modules.
if cat /proc/cmdline | grep -q "loadpin"; then
    echo "LoadPin has already been disabled. Moving to kmod installation."
else
    echo "Sleeping for 60s until the node is ready..."
    sleep 60
    echo "LoadPin is not disabled. Disabling LoadPin now."

    # Mount EFI partition and modify grub configuration
    mkdir -p /mnt/disks
    mount /dev/disk/by-label/EFI-SYSTEM /mnt/disks
    sed -i -e 's|module.sig_enforce=0|module.sig_enforce=0 loadpin.enforce=0|g' /mnt/disks/efi/boot/grub.cfg
    umount /mnt/disks

    # Trigger reboot
    echo 1 > /proc/sys/kernel/sysrq
    echo b > /proc/sysrq-trigger
fi

# Install Lustre client drivers.

# Verify if LNET has been initialized with a different port.
if [ -f "/sys/module/lnet/parameters/accept_port" ]; then
    CURRENT_PORT=$(cat /sys/module/lnet/parameters/accept_port)
    if [ "$CURRENT_PORT" != "$LNET_PORT" ]; then
        echo "ERROR: LNET_PORT mismatch! Expected: $LNET_PORT, but found: $CURRENT_PORT."
        echo "Your node already has Lustre kernel modules installed with an outdated configuration."
        echo "Please upgrade your node pool to apply the correct settings."
        exit 1
    fi
fi

# --gcs-bucket: Specifies the GCS bucket containing the driver packages ('cos-default').
# --module-version: Sets the lustre client driver version.
# --kernelmodulestree: Sets the path to the kernel modules directory on the host ('/host_modules').
# --lsb-release-path: Specifies the path to the lsb-release file on the host ('/host_etc/lsb-release').
# --insert-on-install: Inserts the module into the kernel after installation.
# --module-arg lnet.accept_port=6988: This is crucial for setting the LNET port.
#                                     Lustre uses LNET for network communication, and this
#                                     parameter configures the port LNET will use. This is
#                                     essential for proper communication between Lustre clients
#                                     and servers. In this case, we're setting it to 6988.

# TODO: Set module version to 2.14 when gke picks up cos-117-18613-164-93.
/usr/bin/cos-dkms install lustre-client-drivers \
    --gcs-bucket=cos-default \
    --module-version=2.16.0 \
    --kernelmodulestree=/host_modules \
    --module-arg=lnet.accept_port=${LNET_PORT} \
    --lsb-release-path=/host_etc/lsb-release \
    --insert-on-install \
    --logtostderr
