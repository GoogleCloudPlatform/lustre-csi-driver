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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
		{
			name:         "RDMA o2ib and tcp0 single NIC",
			fileContent:  "o2ib(rdma0[0,1,2,3],rdma1[4,5,6,7]),tcp0(eth0)",
			expectedNics: "o2ib(rdma0[0,1,2,3],rdma1[4,5,6,7]),tcp0(eth0)",
			defaultNic:   "eth0",
			want:         []string{"eth0"},
		},
		{
			name:        "RDMA o2ib and tcp0 multi NIC",
			fileContent: "o2ib(rdma0[0,1],rdma1[2,3]),tcp0(eth0,eth1)",
			defaultNic:  "eth0",
			want:        []string{"eth0", "eth1"},
		},
		{
			name:        "RDMA o2ib only without tcp0",
			fileContent: "o2ib(rdma0[0,1,2,3],rdma1[4,5,6,7])",
			defaultNic:  "eth0",
			want:        []string{},
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
	defaultNetwork := "tcp0(eth0)"
	defaultCustomArgs := []string{"ptlrpc.max_ptlrpcds=8"}

	tests := []struct {
		name                 string
		packageName          string
		previewClientVersion string
		expectedNetwork      string
		customModuleArgs     []string
		disableMultiNIC      bool
		isRDMA               bool
		cpuPartitions        int
		want                 []string
	}{
		{
			name:                 "Default production version (empty) with multi-NIC enabled",
			packageName:          "lustre-client-drivers",
			previewClientVersion: "",
			expectedNetwork:      defaultNetwork,
			customModuleArgs:     defaultCustomArgs,
			disableMultiNIC:      false,
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
				"--module-arg=lnet.lnet_numa_range=1000000",
				`--module-arg=lnet.networks="tcp0(eth0)"`,
				"--module-arg=ptlrpc.max_ptlrpcds=8",
			},
		},
		{
			name:                 "Default production version with multi-NIC disabled",
			packageName:          "lustre-client-drivers",
			previewClientVersion: "",
			expectedNetwork:      defaultNetwork,
			customModuleArgs:     defaultCustomArgs,
			disableMultiNIC:      true,
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
			name:                 "Preview stream version 2.16 with lustre-client-drivers-2_16",
			packageName:          "lustre-client-drivers-2_16",
			previewClientVersion: "2.16",
			expectedNetwork:      defaultNetwork,
			customModuleArgs:     defaultCustomArgs,
			disableMultiNIC:      false,
			want: []string{
				"install",
				"lustre-client-drivers-2_16",
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
				"--module-arg=lnet.lnet_numa_range=1000000",
				`--module-arg=lnet.networks="tcp0(eth0)"`,
				"--module-arg=ptlrpc.max_ptlrpcds=8",
			},
		},
		{
			name:                 "Preview stream version 2.17 with lustre-client-drivers-pre",
			packageName:          "lustre-client-drivers-pre",
			previewClientVersion: "2.17",
			expectedNetwork:      defaultNetwork,
			customModuleArgs:     defaultCustomArgs,
			disableMultiNIC:      false,
			want: []string{
				"install",
				"lustre-client-drivers-pre",
				"--latest",
				"--min-version=2.17.0",
				"--max-version=2.17.9999",
				"--gcs-bucket=cos-default",
				"-w", "0",
				"--kernelmodulestree=/host_modules",
				"--lsb-release-path=/host_etc/lsb-release",
				"--insert-on-install",
				"--logtostderr",
				"--module-arg=lnet.accept_port=988",
				"--module-arg=lnet.lnet_numa_range=1000000",
				`--module-arg=lnet.networks="tcp0(eth0)"`,
				"--module-arg=ptlrpc.max_ptlrpcds=8",
			},
		},
		{
			name:                 "Exact driver build version with multi-NIC enabled",
			packageName:          "lustre-client-drivers-2_16",
			previewClientVersion: "2.16.0_p60",
			expectedNetwork:      defaultNetwork,
			customModuleArgs:     defaultCustomArgs,
			disableMultiNIC:      false,
			want: []string{
				"install",
				"lustre-client-drivers-2_16",
				"--package-version=2.16.0_p60",
				"--gcs-bucket=cos-default",
				"-w", "0",
				"--kernelmodulestree=/host_modules",
				"--lsb-release-path=/host_etc/lsb-release",
				"--insert-on-install",
				"--logtostderr",
				"--module-arg=lnet.accept_port=988",
				"--module-arg=lnet.lnet_numa_range=1000000",
				`--module-arg=lnet.networks="tcp0(eth0)"`,
				"--module-arg=ptlrpc.max_ptlrpcds=8",
			},
		},
		{
			name:            "RDMA node with A5X partitions and default ko2iblnd args",
			packageName:     "lustre-client-drivers",
			expectedNetwork: "o2ib(rdma0[0,1,2,3],rdma1[4,5,6,7]),tcp0(eth0)",
			disableMultiNIC: true,
			isRDMA:          true,
			cpuPartitions:   8,
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
				"--module-arg=lnet.lnet_numa_range=1000000",
				`--module-arg=lnet.networks="o2ib(rdma0[0,1,2,3],rdma1[4,5,6,7]),tcp0(eth0)"`,
				"--module-arg=libcfs.cpu_npartitions=8",
				"--module-arg=ko2iblnd.credits=8192",
				"--module-arg=ko2iblnd.ntx=8192",
				"--module-arg=ko2iblnd.peer_credits=15",
				"--module-arg=ko2iblnd.peer_credits_hiw=7",
				"--module-arg=ko2iblnd.nscheds=16",
				"--module-arg=ko2iblnd.conns_per_peer=8",
				"--module-arg=ko2iblnd.fmr_pool_size=2048",
			},
		},
		{
			name:             "RDMA node with custom module arg overrides",
			packageName:      "lustre-client-drivers",
			expectedNetwork:  "o2ib(rdma0[0,1,2,3],rdma1[4,5,6,7]),tcp0(eth0)",
			customModuleArgs: []string{"ko2iblnd.credits=4096", "libcfs.cpu_npartitions=16"},
			isRDMA:           true,
			cpuPartitions:    8,
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
				"--module-arg=lnet.lnet_numa_range=1000000",
				`--module-arg=lnet.networks="o2ib(rdma0[0,1,2,3],rdma1[4,5,6,7]),tcp0(eth0)"`,
				"--module-arg=ko2iblnd.ntx=8192",
				"--module-arg=ko2iblnd.peer_credits=15",
				"--module-arg=ko2iblnd.peer_credits_hiw=7",
				"--module-arg=ko2iblnd.nscheds=16",
				"--module-arg=ko2iblnd.conns_per_peer=8",
				"--module-arg=ko2iblnd.fmr_pool_size=2048",
				"--module-arg=ko2iblnd.credits=4096",
				"--module-arg=libcfs.cpu_npartitions=16",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := buildCosDKMSArgs(tc.packageName, tc.previewClientVersion, lnetPort, tc.expectedNetwork, tc.customModuleArgs, tc.disableMultiNIC, tc.isRDMA, tc.cpuPartitions)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("buildCosDKMSArgs() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestCosDKMSPackages(t *testing.T) {
	t.Parallel()

	tests := []struct {
		previewClientVersion string
		want                 []string
	}{
		{
			previewClientVersion: "",
			want:                 []string{"lustre-client-drivers"},
		},
		{
			previewClientVersion: "2.16",
			want:                 []string{"lustre-client-drivers-2_16", "lustre-client-drivers", "lustre-client-drivers-pre"},
		},
		{
			previewClientVersion: "2.16.0_p60",
			want:                 []string{"lustre-client-drivers-2_16", "lustre-client-drivers", "lustre-client-drivers-pre"},
		},
		{
			previewClientVersion: "2.16.0_pre1",
			want:                 []string{"lustre-client-drivers-2_16", "lustre-client-drivers", "lustre-client-drivers-pre"},
		},
		{
			previewClientVersion: "2.17",
			want:                 []string{"lustre-client-drivers", "lustre-client-drivers-pre"},
		},
		{
			previewClientVersion: "2.160",
			want:                 []string{"lustre-client-drivers", "lustre-client-drivers-pre"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.previewClientVersion, func(t *testing.T) {
			t.Parallel()

			got := cosDKMSPackages(tc.previewClientVersion)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("cosDKMSPackages(%q) mismatch (-want +got):\n%s", tc.previewClientVersion, diff)
			}
		})
	}
}

func TestInstallLustreKmodOnCos_PackageFallback(t *testing.T) {
	t.Parallel()

	notFoundOut := []byte("Error: could not find any usable versions of the specified package in the local or cache DKMS tree")

	tests := []struct {
		name                 string
		previewClientVersion string
		mockOutputs          map[string]struct {
			out []byte
			err error
		}
		wantCalls   []string
		wantErr     bool
		wantErrText string
	}{
		{
			name:                 "2.16 succeeds on lustre-client-drivers-2_16 without fallback",
			previewClientVersion: "2.16",
			mockOutputs: map[string]struct {
				out []byte
				err error
			}{
				"lustre-client-drivers-2_16": {out: []byte("installed"), err: nil},
			},
			wantCalls: []string{"lustre-client-drivers-2_16"},
		},
		{
			name:                 "2.16 falls through to lustre-client-drivers when -2_16 not found (GKE 1.38+)",
			previewClientVersion: "2.16",
			mockOutputs: map[string]struct {
				out []byte
				err error
			}{
				"lustre-client-drivers-2_16": {out: notFoundOut, err: errors.New("exit status 1")},
				"lustre-client-drivers":      {out: []byte("installed"), err: nil},
			},
			wantCalls: []string{"lustre-client-drivers-2_16", "lustre-client-drivers"},
		},
		{
			name:                 "2.16.0_pre1 falls through to lustre-client-drivers-pre",
			previewClientVersion: "2.16.0_pre1",
			mockOutputs: map[string]struct {
				out []byte
				err error
			}{
				"lustre-client-drivers-2_16": {out: notFoundOut, err: errors.New("exit status 1")},
				"lustre-client-drivers":      {out: notFoundOut, err: errors.New("exit status 1")},
				"lustre-client-drivers-pre":  {out: []byte("installed"), err: nil},
			},
			wantCalls: []string{"lustre-client-drivers-2_16", "lustre-client-drivers", "lustre-client-drivers-pre"},
		},
		{
			name:                 "2.17 falls through lustre-client-drivers to lustre-client-drivers-pre",
			previewClientVersion: "2.17",
			mockOutputs: map[string]struct {
				out []byte
				err error
			}{
				"lustre-client-drivers":     {out: notFoundOut, err: errors.New("exit status 1")},
				"lustre-client-drivers-pre": {out: []byte("installed"), err: nil},
			},
			wantCalls: []string{"lustre-client-drivers", "lustre-client-drivers-pre"},
		},
		{
			name:                 "non-not-found error (LoadPin) stops immediately without fallback",
			previewClientVersion: "2.16",
			mockOutputs: map[string]struct {
				out []byte
				err error
			}{
				"lustre-client-drivers-2_16": {out: []byte("insmod: ERROR: Operation not permitted"), err: errors.New("exit status 1")},
			},
			wantCalls:   []string{"lustre-client-drivers-2_16"},
			wantErr:     true,
			wantErrText: "permission issue",
		},
		{
			name:                 "all candidates not found returns error",
			previewClientVersion: "2.16",
			mockOutputs: map[string]struct {
				out []byte
				err error
			}{
				"lustre-client-drivers-2_16": {out: notFoundOut, err: errors.New("exit status 1")},
				"lustre-client-drivers":      {out: notFoundOut, err: errors.New("exit status 1")},
				"lustre-client-drivers-pre":  {out: notFoundOut, err: errors.New("exit status 1")},
			},
			wantCalls:   []string{"lustre-client-drivers-2_16", "lustre-client-drivers", "lustre-client-drivers-pre"},
			wantErr:     true,
			wantErrText: "could not find any usable versions",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var calls []string
			err := installLustreKmodOnCos(
				context.Background(),
				false,
				nil,
				[]string{"eth0"},
				nil,
				false,
				"eth0",
				tc.previewClientVersion,
				func(_ context.Context, args []string) ([]byte, error) {
					pkg := args[1]
					calls = append(calls, pkg)
					res := tc.mockOutputs[pkg]
					return res.out, res.err
				},
			)
			if (err != nil) != tc.wantErr {
				t.Fatalf("installLustreKmodOnCos() error = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErrText != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErrText)) {
				t.Errorf("installLustreKmodOnCos() error = %v, want substring %q", err, tc.wantErrText)
			}
			if diff := cmp.Diff(tc.wantCalls, calls); diff != "" {
				t.Errorf("candidate package call order mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestCalculateCPUPartitions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		numCPUs     int
		numRDMANICs int
		want        int
	}{
		{
			name:        "A5X metal (144 CPUs, 2 RDMA NICs)",
			numCPUs:     144,
			numRDMANICs: 2,
			want:        8,
		},
		{
			name:        "A5X (96 CPUs, 2 RDMA NICs)",
			numCPUs:     96,
			numRDMANICs: 2,
			want:        8,
		},
		{
			name:        "C4X highcpu-192 (192 CPUs, 8 RDMA NICs)",
			numCPUs:     192,
			numRDMANICs: 8,
			want:        16,
		},
		{
			name:        "C4X (128 CPUs, 8 RDMA NICs)",
			numCPUs:     128,
			numRDMANICs: 8,
			want:        16,
		},
		{
			name:        "C4X (64 CPUs, 8 RDMA NICs)",
			numCPUs:     64,
			numRDMANICs: 8,
			want:        8,
		},
		{
			name:        "Large node (256 CPUs, 8 RDMA NICs)",
			numCPUs:     256,
			numRDMANICs: 8,
			want:        32,
		},
		{
			name:        "Single RDMA NIC (64 CPUs, 1 NIC)",
			numCPUs:     64,
			numRDMANICs: 1,
			want:        4,
		},
		{
			name:        "Low CPU fallback capped at numCPUs (4 CPUs, 8 NICs)",
			numCPUs:     4,
			numRDMANICs: 8,
			want:        4,
		},
		{
			name:        "Zero RDMA NICs returns 1",
			numCPUs:     64,
			numRDMANICs: 0,
			want:        1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := CalculateCPUPartitions(tc.numCPUs, tc.numRDMANICs); got != tc.want {
				t.Errorf("CalculateCPUPartitions(%d, %d) = %d, want %d", tc.numCPUs, tc.numRDMANICs, got, tc.want)
			}
		})
	}
}

func TestBuildRDMALnetNetworkString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		rdmaNICs      []string
		primaryNic    string
		cpuPartitions int
		want          string
	}{
		{
			name:          "A5X 2 RDMA NICs with 8 partitions",
			rdmaNICs:      []string{"rdma0", "rdma1"},
			primaryNic:    "eth0",
			cpuPartitions: 8,
			want:          "o2ib(rdma0[0,1,2,3],rdma1[4,5,6,7]),tcp0(eth0)",
		},
		{
			name:          "Unsorted RDMA NICs are sorted deterministically",
			rdmaNICs:      []string{"rdma1", "rdma0"},
			primaryNic:    "eth0",
			cpuPartitions: 8,
			want:          "o2ib(rdma0[0,1,2,3],rdma1[4,5,6,7]),tcp0(eth0)",
		},
		{
			name:          "C4X 8 RDMA NICs with 16 partitions",
			rdmaNICs:      []string{"rdma0", "rdma1", "rdma2", "rdma3", "rdma4", "rdma5", "rdma6", "rdma7"},
			primaryNic:    "eth0",
			cpuPartitions: 16,
			want:          "o2ib(rdma0[0,1],rdma1[2,3],rdma2[4,5],rdma3[6,7],rdma4[8,9],rdma5[10,11],rdma6[12,13],rdma7[14,15]),tcp0(eth0)",
		},
		{
			name:          "Single RDMA NIC with 4 partitions",
			rdmaNICs:      []string{"rdma0"},
			primaryNic:    "eth0",
			cpuPartitions: 4,
			want:          "o2ib(rdma0[0,1,2,3]),tcp0(eth0)",
		},
		{
			name:          "Fewer partitions than NICs omits CPT lists",
			rdmaNICs:      []string{"rdma0", "rdma1", "rdma2", "rdma3"},
			primaryNic:    "eth0",
			cpuPartitions: 2,
			want:          "o2ib(rdma0,rdma1,rdma2,rdma3),tcp0(eth0)",
		},
		{
			name:          "Empty RDMA NICs falls back to tcp0",
			rdmaNICs:      []string{},
			primaryNic:    "eth0",
			cpuPartitions: 8,
			want:          "tcp0(eth0)",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := BuildRDMALnetNetworkString(tc.rdmaNICs, tc.primaryNic, tc.cpuPartitions); got != tc.want {
				t.Errorf("BuildRDMALnetNetworkString() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestUbuntuClientModules(t *testing.T) {
	t.Parallel()

	baseModules := []string{"ksocklnd", "lustre", "mgc", "lmv", "lov", "mdc", "osc"}
	gssModules := []string{"ksocklnd", "ptlrpc_gss", "lustre", "mgc", "lmv", "lov", "mdc", "osc"}

	tests := []struct {
		name    string
		verOut  string
		want    []string
		wantErr bool
	}{
		{
			name:   "Lustre 2.16.0_ddn60b includes ptlrpc_gss",
			verOut: "2.16.0_ddn60b\n",
			want:   gssModules,
		},
		{
			name:   "Lustre 2.16.0_ddn60 includes ptlrpc_gss",
			verOut: "2.16.0_ddn60\n",
			want:   gssModules,
		},
		{
			name:   "Lustre 2.17.0 includes ptlrpc_gss",
			verOut: "2.17.0\n",
			want:   gssModules,
		},
		{
			name:   "Lustre 2.15.99 (< 2.16) omits ptlrpc_gss",
			verOut: "2.15.99\n",
			want:   baseModules,
		},
		{
			name:   "Lustre 2.14.0_ddn255 (< 2.16) omits ptlrpc_gss",
			verOut: "2.14.0_ddn255\n",
			want:   baseModules,
		},
		{
			name:    "empty version output returns error",
			verOut:  "   \n",
			wantErr: true,
		},
		{
			name:    "unparseable version output returns error",
			verOut:  "unknown\n",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := ubuntuClientModules(tc.verOut)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ubuntuClientModules(%q) error = %v, wantErr %v", tc.verOut, err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("ubuntuClientModules(%q) mismatch (-want +got):\n%s", tc.verOut, diff)
			}
		})
	}
}
