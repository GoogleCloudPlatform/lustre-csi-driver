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

if [ "${ENABLE_LEGACY_LUSTRE_PORT}" = "true" ]; then
    LNET_PORT=6988
else
    LNET_PORT=988
fi

disable_loadpin_if_needed() {
    # Check if LoadPin is already disabled.
    if grep -q "loadpin" /proc/cmdline; then
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
}

check_lnet_port() {
    if [ -f "/sys/module/lnet/parameters/accept_port" ]; then
        CURRENT_PORT=$(cat /sys/module/lnet/parameters/accept_port)
        if [ "$CURRENT_PORT" != "$LNET_PORT" ]; then
            echo "ERROR: LNET_PORT mismatch! Expected: $LNET_PORT, but found: $CURRENT_PORT."
            echo "Your node already has Lustre kernel modules installed with an outdated configuration."
            echo "Please upgrade your node pool to apply the correct settings."
            exit 1
        fi
    fi
}

install_lustre_client_drivers() {
    # --gcs-bucket: Specifies the GCS bucket containing the driver packages ('cos-default').
    # --latest: Installs the latest available driver version that is compatible with the kernel version running on the current node. 
    #           We can’t pin to a specific driver version because GKE nodes can skew from the control plane version. 
    #           If we pin to a fixed version and the control plane is ahead of the node, it could result in a kmod installer failure due to the node's kernel being too old for the specified driver version.
    # --kernelmodulestree: Sets the path to the kernel modules directory on the host ('/host_modules').
    # --lsb-release-path: Specifies the path to the lsb-release file on the host ('/host_etc/lsb-release').
    # --insert-on-install: Inserts the module into the kernel after installation.
    # --module-arg lnet.accept_port=${LNET_PORT}: This is crucial for setting the LNET port.
    #                                     Lustre uses LNET for network communication, and this
    #                                     parameter configures the port LNET will use. This is
    #                                     essential for proper communication between Lustre clients
    #                                     and servers. The default value is 988.
    # -w Set the number of parallel downloads (`0` downloads all files in parallel).
    /usr/bin/cos-dkms install lustre-client-drivers \
        --gcs-bucket=cos-default \
        --latest \
        -w 0 \
        --kernelmodulestree=/host_modules \
        --module-arg=lnet.accept_port=${LNET_PORT} \
        --lsb-release-path=/host_etc/lsb-release \
        --insert-on-install \
        --logtostderr
}

if [ "${CHECK_LOADPIN}" = "true" ]; then
    disable_loadpin_if_needed
else
    echo "Skipping LoadPin check and installing lustre kmods"
fi

check_lnet_port
install_lustre_client_drivers