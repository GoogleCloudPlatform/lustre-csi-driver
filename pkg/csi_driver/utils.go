/*
Copyright 2025 Google LLC

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    https://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package driver

import (
	"context"
	"fmt"
	"strings"

	csi "github.com/container-storage-interface/spec/lib/go/csi"
	pbSanitizer "github.com/kubernetes-csi/csi-lib-utils/protosanitizer"
	"google.golang.org/grpc"
	"k8s.io/klog/v2"
)

func NewVolumeCapabilityAccessMode(mode csi.VolumeCapability_AccessMode_Mode) *csi.VolumeCapability_AccessMode {
	return &csi.VolumeCapability_AccessMode{Mode: mode}
}

func NewControllerServiceCapability(c csi.ControllerServiceCapability_RPC_Type) *csi.ControllerServiceCapability {
	return &csi.ControllerServiceCapability{
		Type: &csi.ControllerServiceCapability_Rpc{
			Rpc: &csi.ControllerServiceCapability_RPC{
				Type: c,
			},
		},
	}
}

func NewNodeServiceCapability(c csi.NodeServiceCapability_RPC_Type) *csi.NodeServiceCapability {
	return &csi.NodeServiceCapability{
		Type: &csi.NodeServiceCapability_Rpc{
			Rpc: &csi.NodeServiceCapability_RPC{
				Type: c,
			},
		},
	}
}

func logGRPC(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
	klog.V(5).Infof("GRPC call: %s, GRPC request: %+v", info.FullMethod, pbSanitizer.StripSecretsCSI03(req).String())
	resp, err := handler(ctx, req)
	if err != nil {
		klog.Errorf("GRPC call: %s, GRPC error: %v", info.FullMethod, err.Error())
	} else {
		klog.V(5).Infof("GRPC call: %s, GRPC response: %+v", info.FullMethod, resp)
	}

	return resp, err
}

// normalizeKeys normalizes map keys and validates against a supported set,
// returning a map with normalized keys or an error for unsupported/duplicate keys.
func normalizeKeys(input map[string]string, supported map[string]struct{}) (map[string]string, error) {
	processed := map[string]string{}
	unsupported := []string{}
	duplicate := []string{}

	for inputKey, inputVal := range input {
		normalizedKey := normalize(inputKey)

		// Don't validate k8s keys, keys such as csi.storage.k8s.io/pvc/name and storage.kubernetes.io are added by the system.
		isK8sKey := strings.HasPrefix(normalizedKey, csiPrefixKubernetesStorage) ||
			strings.HasPrefix(normalizedKey, storagePrefixKubernetes)

		if _, ok := supported[normalizedKey]; !ok && !isK8sKey {
			unsupported = append(unsupported, inputKey)
		}

		if _, ok := processed[normalizedKey]; ok {
			duplicate = append(duplicate, inputKey)
		}

		processed[normalizedKey] = inputVal
	}

	if len(unsupported) > 0 {
		return nil, fmt.Errorf("unsupported keys: %+v", unsupported)
	}

	if len(duplicate) > 0 {
		return nil, fmt.Errorf("duplicate keys: %+v", duplicate)
	}

	return processed, nil
}

// normalizeVolumeContext normalizes volume context keys, ensuring they are supported.
func normalizeVolumeContext(input map[string]string) (map[string]string, error) {
	supported := make(map[string]struct{})
	for _, key := range volumeAttributes {
		supported[normalize(key)] = struct{}{}
	}

	processed, err := normalizeKeys(input, supported)
	if err != nil {
		return nil, fmt.Errorf("failed to process volume context: %+w", err)
	}

	return processed, err
}

// normalizeParams normalizes parameter keys, ensuring they are supported.
func normalizeParams(input map[string]string) (map[string]string, error) {
	supported := make(map[string]struct{})
	for _, key := range paramKeys {
		supported[normalize(key)] = struct{}{}
	}

	processed, err := normalizeKeys(input, supported)
	if err != nil {
		return nil, fmt.Errorf("failed to process parameters: %+w", err)
	}

	return processed, err
}

// normalize takes a string input and returns a normalized version of it.
//
// This function is useful for comparing strings that might have different
// formatting conventions (e.g., "some-string", "some_string", "SomeString")
// in a case-insensitive and separator-agnostic manner.
//
// Example:
//   - "file_STRIPE-level" => "filestripelevel"
func normalize(input string) string {
	normalized := strings.ReplaceAll(input, "-", "")
	normalized = strings.ReplaceAll(normalized, "_", "")
	normalized = strings.ToLower(normalized)

	return normalized
}
