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
		lustreDirMissing bool
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
			name:             "accept_port exists but lustre module dir missing",
			fileContent:      "988",
			lustreDirMissing: true,
			wantInstalled:    false,
			wantErr:          false,
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
			lustreModuleDir := filepath.Join(tempDir, "lustre")

			if !tc.lustreDirMissing {
				if err := os.Mkdir(lustreModuleDir, 0o755); err != nil {
					t.Fatalf("Failed to create temp lustre dir: %v", err)
				}
			}

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

			gotInstalled, err := isLustreKmodInstalled(tc.enableLegacyPort, acceptPortFile, lustreModuleDir)
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
		defaultNic   string
		want         []string
		// We don't check for specific log output here, but we verify the return values
		// and that it doesn't crash on warnings.
	}{
		{
			name:        "File missing - returns default eth0 on cos",
			fileMissing: true,
			defaultNic:  "eth0",
			want:        []string{"eth0"},
		},
		{
			name:        "File missing - returns default ens4 on ubuntu",
			fileMissing: true,
			defaultNic:  "ens4",
			want:        []string{"ens4"},
		},
		{
			name:        "File empty - returns default eth0 on cos",
			fileContent: "",
			defaultNic:  "eth0",
			want:        []string{"eth0"},
		},
		{
			name:        "File empty - returns default ens4 on ubuntu",
			fileContent: "",
			defaultNic:  "ens4",
			want:        []string{"ens4"},
		},
		{
			name:        "Single NIC",
			fileContent: "tcp0(eth0)",
			defaultNic:  "eth0",
			want:        []string{"eth0"},
		},
		{
			name:        "Multi NIC",
			fileContent: "tcp0(eth0,eth1)",
			defaultNic:  "eth0",
			want:        []string{"eth0", "eth1"},
		},
		{
			name:         "Validation match",
			fileContent:  "tcp0(eth0,eth1)",
			expectedNics: "tcp0(eth0,eth1)",
			defaultNic:   "eth0",
			want:         []string{"eth0", "eth1"},
		},
		{
			name:         "Validation mismatch - single NIC expected",
			fileContent:  "tcp0(eth0,eth1)",
			expectedNics: "tcp0(eth0)",
			defaultNic:   "eth0",
			want:         []string{"eth0", "eth1"},
		},
		{
			name:         "Validation mismatch - multi NIC expected",
			fileContent:  "tcp0(eth0)",
			expectedNics: "tcp0(eth0,eth1)",
			defaultNic:   "eth0",
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

			got, err := getLnetNetwork(tc.expectedNics, networkFile, tc.defaultNic)
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

func TestGetNodeLabelValue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		labelKey  string
		mockNode  *v1.Node
		wantValue string
		mockErr   error
		wantErr   bool
	}{
		{
			name:     "Valid OS label found",
			labelKey: OSNodeLabel,
			mockNode: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{OSNodeLabel: "cos"},
				},
			},
			wantValue: "cos",
		},
		{
			name:     "Valid preview client label found",
			labelKey: PreviewClientLabel,
			mockNode: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{PreviewClientLabel: "2.16"},
				},
			},
			wantValue: "2.16",
		},
		{
			name:     "Label missing - returns empty string",
			labelKey: OSNodeLabel,
			mockNode: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"random-key": "ubuntu"},
				},
			},
			wantValue: "",
		},
		{
			name:     "Node has no labels map - returns empty string",
			labelKey: OSNodeLabel,
			mockNode: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{},
			},
			wantValue: "",
		},
		{
			name:      "API error from k8s client",
			labelKey:  OSNodeLabel,
			mockErr:   fmt.Errorf("k8s node timeout"),
			wantValue: "",
			wantErr:   true,
		},
		{
			name:      "Nil node object returned without error",
			labelKey:  OSNodeLabel,
			mockNode:  nil,
			wantValue: "",
			wantErr:   true,
		},
		{
			name:     "Label value with surrounding whitespace is trimmed",
			labelKey: PreviewClientLabel,
			mockNode: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{PreviewClientLabel: "  2.16  "},
				},
			},
			wantValue: "2.16",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			client := &mockNodeClient{
				node: tc.mockNode,
				err:  tc.mockErr,
			}

			got, err := GetNodeLabelValue(context.Background(), "node-name", tc.labelKey, client)

			// Error check
			if (err != nil) != tc.wantErr {
				t.Fatalf("GetNodeLabelValue() error = %v, wantErr %v", err, tc.wantErr)
			}

			// Node label value check
			if got != tc.wantValue {
				t.Errorf("GetNodeLabelValue() got = %v, want %v", got, tc.wantValue)
			}
		})
	}
}

func TestBuildLnetNetworkString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		nics            []string
		primaryNic      string
		disableMultiNIC bool
		want            string
	}{
		{
			name:            "single NIC, multiNIC disabled",
			nics:            []string{"eth0"},
			primaryNic:      "eth0",
			disableMultiNIC: true,
			want:            "tcp0(eth0)",
		},
		{
			name:            "multiple NICs, multiNIC disabled",
			nics:            []string{"eth0", "eth1"},
			primaryNic:      "eth0",
			disableMultiNIC: true,
			want:            "tcp0(eth0)",
		},
		{
			name:            "multiple NICs, multiNIC enabled, primary is first",
			nics:            []string{"eth0", "eth1"},
			primaryNic:      "eth0",
			disableMultiNIC: false,
			want:            "tcp0(eth0,eth1)",
		},
		{
			name:            "multiple NICs, multiNIC enabled, primary is not first",
			nics:            []string{"eth1", "eth0"},
			primaryNic:      "eth0",
			disableMultiNIC: false,
			want:            "tcp0(eth0,eth1)",
		},
		{
			name:            "multiple NICs, multiNIC enabled, extra NICs",
			nics:            []string{"eth1", "eth0", "eth2"},
			primaryNic:      "eth0",
			disableMultiNIC: false,
			want:            "tcp0(eth0,eth1,eth2)",
		},
		{
			name:            "empty nics, multiNIC enabled",
			nics:            []string{},
			primaryNic:      "eth0",
			disableMultiNIC: false,
			want:            "tcp0(eth0)",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := BuildLnetNetworkString(tc.nics, tc.primaryNic, tc.disableMultiNIC)
			if got != tc.want {
				t.Errorf("BuildLnetNetworkString() got = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestIsPreviewVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		version     string
		wantPreview bool
	}{
		{
			name:        "Preview minor version 2.16",
			version:     "2.16",
			wantPreview: true,
		},
		{
			name:        "Preview minor version 2.15",
			version:     "2.15",
			wantPreview: true,
		},
		{
			name:        "Exact build with pre-release suffix 2.16.0_pre1",
			version:     "2.16.0_pre1",
			wantPreview: false,
		},
		{
			name:        "Exact build full semver 2.16.0",
			version:     "2.16.0",
			wantPreview: false,
		},
		{
			name:        "Exact build with rc suffix 2.16.1-rc2",
			version:     "2.16.1-rc2",
			wantPreview: false,
		},
		{
			name:        "Invalid version string",
			version:     "invalid",
			wantPreview: false,
		},
		{
			name:        "Empty version string",
			version:     "",
			wantPreview: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := isPreviewVersion(tc.version); got != tc.wantPreview {
				t.Errorf("isPreviewVersion(%q) = %v, want %v", tc.version, got, tc.wantPreview)
			}
		})
	}
}

func TestBuildUbuntuAptDownloadArgs(t *testing.T) {
	t.Parallel()

	kernelVersion := "6.8.0-1017-gke"

	tests := []struct {
		name                 string
		previewClientVersion string
		want                 []string
	}{
		{
			name:                 "Default production version (empty)",
			previewClientVersion: "",
			want: []string{
				"download",
				"lustre-client-modules-6.8.0-1017-gke/lustre-client-ubuntu-noble",
			},
		},
		{
			name:                 "Preview stream version 2.16",
			previewClientVersion: "2.16",
			want: []string{
				"download",
				"-t",
				"lustre-client-ubuntu-noble-preview",
				"lustre-client-modules-6.8.0-1017-gke=2.16*",
			},
		},
		{
			name:                 "Exact Debian package build version",
			previewClientVersion: "2.16.0-ddn56b-1",
			want: []string{
				"download",
				"-t",
				"lustre-client-ubuntu-noble-preview",
				"lustre-client-modules-6.8.0-1017-gke=2.16.0-ddn56b-1",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := buildUbuntuAptDownloadArgs(kernelVersion, tc.previewClientVersion)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("buildUbuntuAptDownloadArgs() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestBuildCosDKMSArgs(t *testing.T) {
	t.Parallel()

	lnetPort := 988
	expectedNetwork := "tcp0(eth0)"
	customModuleArgs := []string{"ptlrpc.max_ptlrpcds=8"}

	tests := []struct {
		name                 string
		previewClientVersion string
		want                 []string
	}{
		{
			name:                 "Default production version (empty)",
			previewClientVersion: "",
			want: []string{
				"install",
				"lustre-client-drivers",
				"--latest",
				"--gcs-bucket=cos-default",
				"-w", "0",
				"--kernelmodulestree=/host_modules",
				"--lsb-release-path=/host_etc/lsb-release",
				"--insert-on-install",
				"--logtostderr",
				"--module-arg=lnet.accept_port=988",
				`--module-arg=lnet.networks="tcp0(eth0)"`,
				"--module-arg=ptlrpc.max_ptlrpcds=8",
			},
		},
		{
			name:                 "Preview stream version 2.16",
			previewClientVersion: "2.16",
			want: []string{
				"install",
				"lustre-client-drivers-pre",
				"--latest",
				"--min-version=2.16.0",
				"--max-version=2.16.9999",
				"--gcs-bucket=cos-default",
				"-w", "0",
				"--kernelmodulestree=/host_modules",
				"--lsb-release-path=/host_etc/lsb-release",
				"--insert-on-install",
				"--logtostderr",
				"--module-arg=lnet.accept_port=988",
				`--module-arg=lnet.networks="tcp0(eth0)"`,
				"--module-arg=ptlrpc.max_ptlrpcds=8",
			},
		},
		{
			name:                 "Exact driver build version",
			previewClientVersion: "2.16.0_ddn52c",
			want: []string{
				"install",
				"lustre-client-drivers-pre",
				"--package-version=2.16.0_ddn52c",
				"--gcs-bucket=cos-default",
				"-w", "0",
				"--kernelmodulestree=/host_modules",
				"--lsb-release-path=/host_etc/lsb-release",
				"--insert-on-install",
				"--logtostderr",
				"--module-arg=lnet.accept_port=988",
				`--module-arg=lnet.networks="tcp0(eth0)"`,
				"--module-arg=ptlrpc.max_ptlrpcds=8",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := buildCosDKMSArgs(tc.previewClientVersion, lnetPort, expectedNetwork, customModuleArgs)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("buildCosDKMSArgs() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
