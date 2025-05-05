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
	"reflect"
	"testing"
)

func TestNormalizeVolumeContext(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		input       map[string]string
		expected    map[string]string
		expectedErr bool
	}{
		{
			name: "already normalized",
			input: map[string]string{
				keyFilesystem: "val1",
			},
			expected: map[string]string{
				keyFilesystem: "val1",
			},
			expectedErr: false,
		},
		{
			name: "case insensitive",
			input: map[string]string{
				"Filesystem": "val1",
			},
			expected: map[string]string{
				keyFilesystem: "val1",
			},
			expectedErr: false,
		},
		{
			name: "kebab-case",
			input: map[string]string{
				"file-system": "val1",
			},
			expected: map[string]string{
				keyFilesystem: "val1",
			},
			expectedErr: false,
		},
		{
			name: "snake_case",
			input: map[string]string{
				"file_system": "val1",
			},
			expected: map[string]string{
				keyFilesystem: "val1",
			},
			expectedErr: false,
		},
		{
			name: "mixed",
			input: map[string]string{
				"fileSYstem": "val1",
			},
			expected: map[string]string{
				keyFilesystem: "val1",
			},
			expectedErr: false,
		},
		{
			name:        "unsupported key",
			input:       map[string]string{"unknown": "val2"},
			expected:    nil,
			expectedErr: true,
		},
		{
			name:        "duplicate key",
			input:       map[string]string{"ip": "val1", "Ip": "val2"},
			expected:    nil,
			expectedErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			actual, err := normalizeVolumeContext(tt.input)
			if tt.expectedErr {
				if err == nil {
					t.Errorf("Expected error but got nil")
				}
			} else {
				if err != nil {
					t.Errorf("Expected nil error but got: %+v", err)
				}
				if !reflect.DeepEqual(tt.expected, actual) {
					t.Errorf("Expected %+v, got %+v", tt.expected, actual)
				}
			}
		})
	}
}

func TestNormalizeParams(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		input       map[string]string
		expected    map[string]string
		expectedErr bool
	}{
		{
			name: "already normalized",
			input: map[string]string{
				keyNetwork:      "val1",
				keyDescription:  "val2",
				keyPVCName:      "val3",
				keyPVCNamespace: "val4",
				keyPVName:       "val5",
				keyLabels:       "val6",
				keyFilesystem:   "val7",
			},
			expected: map[string]string{
				keyNetwork:      "val1",
				keyDescription:  "val2",
				keyPVCName:      "val3",
				keyPVCNamespace: "val4",
				keyPVName:       "val5",
				keyLabels:       "val6",
				keyFilesystem:   "val7",
			},
			expectedErr: false,
		},
		{
			name: "case insensitive",
			input: map[string]string{
				"Network":                          "val1",
				"Description":                      "val2",
				"csi.Storage.K8s.io/Pvc/Name":      "val3",
				"csi.Storage.K8s.io/Pvc/Namespace": "val4",
				"csi.Storage.K8s.io/Pv/Name":       "val5",
				"Labels":                           "val6",
			},
			expected: map[string]string{
				keyNetwork:      "val1",
				keyDescription:  "val2",
				keyPVCName:      "val3",
				keyPVCNamespace: "val4",
				keyPVName:       "val5",
				keyLabels:       "val6",
			},
			expectedErr: false,
		},
		{
			name: "mixed",
			input: map[string]string{
				"neTWork":                          "val1",
				"Description":                      "val2",
				"csi.storage.k8s.io/pvc/name":      "val3",
				"csi.storage.k8s.io/pvc/namespace": "val4",
				"csi.storage.k8s.io/pv/name":       "val5",
				"LABELS":                           "val6",
				"FileSystem":                       "val7",
			},
			expected: map[string]string{
				keyNetwork:      "val1",
				keyDescription:  "val2",
				keyPVCName:      "val3",
				keyPVCNamespace: "val4",
				keyPVName:       "val5",
				keyLabels:       "val6",
				keyFilesystem:   "val7",
			},
			expectedErr: false,
		},
		{
			name:        "unsupported key",
			input:       map[string]string{"unknown": "val1"},
			expected:    nil,
			expectedErr: true,
		},
		{
			name:        "duplicate key",
			input:       map[string]string{"network": "val1", "Network": "val2"},
			expected:    nil,
			expectedErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			actual, err := normalizeParams(tt.input)
			if tt.expectedErr {
				if err == nil {
					t.Errorf("Expected error but got nil")
				}
			} else {
				if err != nil {
					t.Errorf("Expected nil error but got: %+v", err)
				}
				if !reflect.DeepEqual(tt.expected, actual) {
					t.Errorf("Expected %+v, got %+v", tt.expected, actual)
				}
			}
		})
	}
}
