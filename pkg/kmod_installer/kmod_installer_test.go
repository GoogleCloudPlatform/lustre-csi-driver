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

package kmodinstaller

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestIsLustreKmodInstalled(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		fileContent      string
		fileMissing      bool
		isDir            bool
		enableLegacyPort bool
		wantInstalled    bool
		wantErr          bool
	}{
		{
			name:          "File missing",
			fileMissing:   true,
			wantInstalled: false,
			wantErr:       false,
		},
		{
			name:             "File exists, default port match",
			fileContent:      "988",
			enableLegacyPort: false,
			wantInstalled:    true,
			wantErr:          false,
		},
		{
			name:             "File exists, legacy port match",
			fileContent:      "6988",
			enableLegacyPort: true,
			wantInstalled:    true,
			wantErr:          false,
		},
		{
			name:             "File exists, port mismatch (expected default 988, got 6988)",
			fileContent:      "6988",
			enableLegacyPort: false,
			wantInstalled:    true,
			wantErr:          true,
		},
		{
			name:             "File exists, port mismatch (expected legacy 6988, got 988)",
			fileContent:      "988",
			enableLegacyPort: true,
			wantInstalled:    true,
			wantErr:          true,
		},
		{
			name:          "File unreadable",
			fileContent:   "988",
			isDir:         true,
			wantInstalled: false,
			wantErr:       true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tempDir := t.TempDir()
			acceptPortFile := filepath.Join(tempDir, "accept_port")

			if !tc.fileMissing {
				if tc.isDir {
					if err := os.Mkdir(acceptPortFile, 0o755); err != nil {
						t.Fatalf("Failed to create temp dir: %v", err)
					}
				} else {
					if err := os.WriteFile(acceptPortFile, []byte(tc.fileContent), 0o644); err != nil {
						t.Fatalf("Failed to write temp file: %v", err)
					}
				}
			}

			gotInstalled, err := isLustreKmodInstalled(tc.enableLegacyPort, acceptPortFile)
			if (err != nil) != tc.wantErr {
				t.Errorf("isLustreKmodInstalled() error = %v, wantErr %v", err, tc.wantErr)
			}
			if gotInstalled != tc.wantInstalled {
				t.Errorf("isLustreKmodInstalled() = %v, want %v", gotInstalled, tc.wantInstalled)
			}
		})
	}
}

func TestGetLnetNetwork(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		fileContent  string
		fileMissing  bool
		expectedNics string
		want         []string
		// We don't check for specific log output here, but we verify the return values
		// and that it doesn't crash on warnings.
	}{
		{
			name:        "File missing - returns default eth0",
			fileMissing: true,
			want:        []string{"eth0"},
		},
		{
			name:        "File empty - returns default eth0",
			fileContent: "",
			want:        []string{"eth0"},
		},
		{
			name:        "Single NIC",
			fileContent: "tcp0(eth0)",
			want:        []string{"eth0"},
		},
		{
			name:        "Multi NIC",
			fileContent: "tcp0(eth0,eth1)",
			want:        []string{"eth0", "eth1"},
		},
		{
			name:         "Validation match",
			fileContent:  "tcp0(eth0,eth1)",
			expectedNics: "tcp0(eth0,eth1)",
			want:         []string{"eth0", "eth1"},
		},
		{
			name:         "Validation mismatch - single NIC expected",
			fileContent:  "tcp0(eth0,eth1)",
			expectedNics: "tcp0(eth0)",
			want:         []string{"eth0", "eth1"},
		},
		{
			name:         "Validation mismatch - multi NIC expected",
			fileContent:  "tcp0(eth0)",
			expectedNics: "tcp0(eth0,eth1)",
			want:         []string{"eth0"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tempDir := t.TempDir()
			networkFile := filepath.Join(tempDir, "networks")

			if !tc.fileMissing {
				if err := os.WriteFile(networkFile, []byte(tc.fileContent), 0o644); err != nil {
					t.Fatalf("Failed to write temp file: %v", err)
				}
			}

			got, err := getLnetNetwork(tc.expectedNics, networkFile)
			if err != nil {
				t.Fatalf("getLnetNetwork() unexpected error: %v", err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("getLnetNetwork() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

type mockNodeClient struct {
	node *v1.Node
	err  error
}

func (m *mockNodeClient) GetNodeWithRetry(ctx context.Context, nodeName string) (*v1.Node, error) {
	return m.node, m.err
}

func TestHostOSFromNodeLabel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		mockNode *v1.Node
		wantOS   string
		mockErr  error
		wantErr  bool
	}{
		{
			name: "Valid label found",
			mockNode: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{osNodeLabel: "cos"},
				},
			},
			wantOS: "cos",
		},
		{
			name: "Host OS Label missing - returns unknown",
			mockNode: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"random-key": "ubuntu"},
				},
			},
			wantOS: "unknown",
		},
		{
			name:    "API error from k8s client",
			mockErr: fmt.Errorf("k8s node timeout"),
			wantOS:  "",
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			client := &mockNodeClient{
				node: tc.mockNode,
				err:  tc.mockErr,
			}

			got, err := HostOSFromNodeLabel(context.Background(), "node-name", client)

			// Error check
			if (err != nil) != tc.wantErr {
				t.Fatalf("HostOSFromNodeLabel() error = %v, wantErr %v", err, tc.wantErr)
			}

			// Node label value check
			if got != tc.wantOS {
				t.Errorf("HostOSFromNodeLabel() got = %v, want %v", got, tc.wantOS)
			}
		})
	}
}
