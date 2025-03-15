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

export OVERLAY ?= dev
export STAGINGVERSION ?= $(shell git describe --long --tags --match='v*' --dirty 2>/dev/null || git rev-list -n1 HEAD)
DRIVER_BINARY = lustre-csi-driver

PROJECT ?= $(shell gcloud config list --format 'value(core.project)')
REGISTRY ?= gcr.io/$(PROJECT)
DRIVER_IMAGE = $(REGISTRY)/$(DRIVER_BINARY)
GSA_FILE ?= ${HOME}/lustre_csi_driver_sa.json
GSA_NS=lustre-csi-driver
LUSTRE_ENDPOINT ?= prod

$(info OVERLAY is ${OVERLAY})
$(info STAGINGVERSION is ${STAGINGVERSION})
$(info DRIVER_IMAGE is $(DRIVER_IMAGE))
$(info LUSTRE_ENDPOINT is $(LUSTRE_ENDPOINT))

BINDIR?=bin

all: driver

build-driver-image-and-push: init-buildx
		{                                                                   \
		set -e ;                                                            \
		docker buildx build \
			--platform linux/amd64 \
			--build-arg STAGINGVERSION=$(STAGINGVERSION) \
			--build-arg TARGETPLATFORM=linux/amd64 \
			-f ./cmd/csi_driver/Dockerfile \
			-t $(DRIVER_IMAGE):$(STAGINGVERSION) --push .; \
		}

driver:
	mkdir -p ${BINDIR}
	CGO_ENABLED=0 GOOS=linux GOARCH=$(shell dpkg --print-architecture) go build -mod vendor -ldflags "${LDFLAGS}" -o ${BINDIR}/${DRIVER_BINARY} cmd/csi_driver/main.go

install:
	make generate-spec-yaml OVERLAY=${OVERLAY} STAGINGVERSION=${STAGINGVERSION}
	kubectl apply -f ${BINDIR}/lustre-csi-driver-specs-generated.yaml
	if [ "${OVERLAY}" != "gke-release" ]; then kubectl create secret generic lustre-csi-driver-sa --from-file=${GSA_FILE} --namespace=${GSA_NS}; fi

uninstall:
	kubectl delete -k deploy/overlays/${OVERLAY} --wait

generate-spec-yaml:
	mkdir -p ${BINDIR}
	./deploy/install-kustomize.sh
	if [ "${OVERLAY}" != "gke-release" ]; then \
		cd ./deploy/overlays/${OVERLAY}; ../../../${BINDIR}/kustomize edit set image gke.gcr.io/lustre-csi-driver=${DRIVER_IMAGE}:${STAGINGVERSION}; \
		cd ./deploy/overlays/${OVERLAY}; ../../../${BINDIR}/kustomize edit add configmap lustre-config --behavior=merge --disableNameSuffixHash --from-literal=endpoint=${LUSTRE_ENDPOINT}; \
	fi
	kubectl kustomize deploy/overlays/${OVERLAY} | tee ${BINDIR}/lustre-csi-driver-specs-generated.yaml > /dev/null
	git restore ./deploy/overlays/${OVERLAY}/kustomization.yaml

init-buildx:
	# Ensure we use a builder that can leverage it (the default on linux will not)
	-docker buildx rm multiarch-multiplatform-builder
	docker buildx create --use --name=multiarch-multiplatform-builder
	docker run --rm --privileged multiarch/qemu-user-static --reset --credential yes --persistent yes
	# Register gcloud as a Docker credential helper.
	# Required for "docker buildx build --push".
	gcloud auth configure-docker --quiet

verify:
	hack/verify-all.sh

unit-test:
	go test -v -mod=vendor -timeout 30s "./pkg/..." -cover

sanity-test:
	go test -v -mod=vendor -timeout 30s "./test/sanity/" -run TestSanity

test-k8s-integration:
	go build -mod=vendor -o bin/k8s-integration-test ./test/k8s-integration