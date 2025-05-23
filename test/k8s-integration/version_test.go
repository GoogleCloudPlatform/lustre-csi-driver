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

package main

import (
	"strconv"
	"testing"
)

func TestParseVersion(t *testing.T) {
	t.Parallel()
	tests := []struct {
		version   string
		expectErr bool
		expectedV version
	}{
		// Positive test cases.
		{
			version: "v1.1.1",
			expectedV: version{
				version: [4]int{1, 1, 1, -1},
			},
		},
		{
			version: "v1.18.0",
			expectedV: version{
				version: [4]int{1, 18, 0, -1},
			},
		},
		{
			version: "v1.18.0-gke.0",
			expectedV: version{
				version: [4]int{1, 18, 0, 0},
			},
		},
		{
			version: "1.18.3-gke.10",
			expectedV: version{
				version: [4]int{1, 18, 3, 10},
			},
		},
		{
			version: "1.18.9",
			expectedV: version{
				version: [4]int{1, 18, 9, -1},
			},
		},
		{
			version: "1.18.10-gke.10",
			expectedV: version{
				version: [4]int{1, 18, 10, 10},
			},
		},
		{
			version: "10.18.10-gke.10",
			expectedV: version{
				version: [4]int{10, 18, 10, 10},
			},
		},
		{
			version: "100.101.102-gke.103",
			expectedV: version{
				version: [4]int{100, 101, 102, 103},
			},
		},
		// Negative test cases
		{
			version:   "1",
			expectErr: true,
		},
		{
			version:   "-1.18.9",
			expectErr: true,
		},
		{
			version:   "1.-18.9",
			expectErr: true,
		},
		{
			version:   "1.18.-9",
			expectErr: true,
		},
		{
			version:   "1.18.9-gke.-1",
			expectErr: true,
		},
		{
			version:   "1.1",
			expectErr: true,
		},
		{
			version:   "1.18",
			expectErr: true,
		},
		{
			version:   "1.18.9.1",
			expectErr: true,
		},
		{
			version:   "1.18.9-1",
			expectErr: true,
		},
		{
			version:   "1.18-gke.0",
			expectErr: true,
		},
		{
			version:   "1.18.0-alpha.x",
			expectErr: true,
		},
		{
			version:   "1.18.0-alpha.beta.1",
			expectErr: true,
		},
		{
			version:   "alpha.3.673+73326ef01d2d7c",
			expectErr: true,
		},
		{
			version:   "1.18-alpha.3.673+73326ef01d2d7c",
			expectErr: true,
		},
		{
			version:   "1.18.3-alpha.3.673+73326ef01d2d7c",
			expectErr: true,
		},
	}

	for i, tc := range tests {
		t.Run("TestCase"+strconv.Itoa(i), func(t *testing.T) {
			t.Parallel()
			gotV, err := parseVersion(tc.version)
			if err != nil {
				if !tc.expectErr {
					t.Fatalf("Got unexpected err: %v", err)
				}

				return
			}

			if err == nil && tc.expectErr {
				t.Fatal("Got no error but expected one")

				return
			}

			if gotV.version[0] != tc.expectedV.version[0] ||
				gotV.version[1] != tc.expectedV.version[1] ||
				gotV.version[2] != tc.expectedV.version[2] ||
				gotV.version[3] != tc.expectedV.version[3] {
				t.Fatalf("Got version: %s, expected: %s", gotV.String(), tc.expectedV.String())
			}
		})
	}
}

func TestIsVersionLessThan(t *testing.T) {
	t.Parallel()
	tests := []struct {
		leftVersion  string
		rightVersion string
		expectRes    bool
	}{
		// Positive cases (left < right).
		{
			leftVersion:  "1.17.5-gke.9",
			rightVersion: "1.17.6",
			expectRes:    true,
		},
		{
			leftVersion:  "1.17.5-gke.9",
			rightVersion: "1.18.5",
			expectRes:    true,
		},
		{
			leftVersion:  "1.17.5-gke.9",
			rightVersion: "2.17.5",
			expectRes:    true,
		},
		{
			leftVersion:  "1.18.0",
			rightVersion: "1.18.0-gke.0",
			expectRes:    true,
		},
		{
			leftVersion:  "1.18.0",
			rightVersion: "1.18.1-gke.0",
			expectRes:    true,
		},
		{
			leftVersion:  "1.18.0",
			rightVersion: "1.19.0-gke.0",
			expectRes:    true,
		},
		{
			leftVersion:  "1.18.0",
			rightVersion: "2.18.0-gke.0",
			expectRes:    true,
		},
		{
			leftVersion:  "1.18.0-gke.0",
			rightVersion: "1.18.0-gke.1",
			expectRes:    true,
		},
		{
			leftVersion:  "1.17.0-gke.9",
			rightVersion: "1.18.0-gke.0",
			expectRes:    true,
		},
		{
			leftVersion:  "1.18.0-gke.9",
			rightVersion: "1.18.1-gke.0",
			expectRes:    true,
		},
		{
			leftVersion:  "1.18.0-gke.9",
			rightVersion: "1.19.0-gke.0",
			expectRes:    true,
		},
		{
			leftVersion:  "1.18.0-gke.9",
			rightVersion: "2.18.0-gke.0",
			expectRes:    true,
		},
		{
			leftVersion:  "1.18.0",
			rightVersion: "1.18.1",
			expectRes:    true,
		},
		{
			leftVersion:  "1.18.0",
			rightVersion: "1.19.0",
			expectRes:    true,
		},
		{
			leftVersion:  "1.18.0",
			rightVersion: "2.18.0",
			expectRes:    true,
		},
		// Negative test cases.(left == right)
		{
			leftVersion:  "0.0.0",
			rightVersion: "0.0.0",
		},
		{
			leftVersion:  "1.1.1",
			rightVersion: "1.1.1",
		},
		{
			leftVersion:  "1.18.0",
			rightVersion: "1.18.0",
		},
		{
			leftVersion:  "1.18.0-gke.0",
			rightVersion: "1.18.0-gke.0",
		},
		// Negative test cases.(left > right)
		{
			leftVersion:  "1.17.6",
			rightVersion: "1.17.5-gke.9",
		},
		{
			leftVersion:  "1.18.5",
			rightVersion: "1.17.5-gke.9",
		},
		{
			leftVersion:  "2.17.5",
			rightVersion: "1.17.5-gke.9",
		},
		{
			leftVersion:  "1.18.0-gke.0",
			rightVersion: "1.18.0",
		},
		{
			leftVersion:  "1.18.1-gke.0",
			rightVersion: "1.18.0",
		},
		{
			leftVersion:  "1.19.0-gke.0",
			rightVersion: "1.18.0",
		},
		{
			leftVersion:  "2.18.0-gke.0",
			rightVersion: "1.18.0",
		},
		{
			leftVersion:  "1.18.0-gke.1",
			rightVersion: "1.18.0-gke.0",
		},
		{
			leftVersion:  "1.18.0-gke.0",
			rightVersion: "1.17.0-gke.9",
		},
		{
			leftVersion:  "1.18.1-gke.0",
			rightVersion: "1.18.0-gke.9",
		},
		{
			leftVersion:  "1.19.0-gke.0",
			rightVersion: "1.18.0-gke.9",
		},
		{
			leftVersion:  "2.18.0-gke.0",
			rightVersion: "1.18.0-gke.9",
		},
		{
			leftVersion:  "1.18.1",
			rightVersion: "1.18.0",
		},
		{
			leftVersion:  "1.19.0",
			rightVersion: "1.18.0",
		},
		{
			leftVersion:  "2.18.0",
			rightVersion: "1.18.0",
		},
	}

	for i, tc := range tests {
		t.Run("TestCase"+strconv.Itoa(i), func(t *testing.T) {
			t.Parallel()
			left, err := parseVersion(tc.leftVersion)
			if err != nil {
				t.Fatalf("Unexpected error: %v", err)

				return
			}
			right, err := parseVersion(tc.rightVersion)
			if err != nil {
				t.Fatalf("Unexpected error: %v", err)

				return
			}

			got := left.lessThan(right)
			if got != tc.expectRes {
				t.Fatalf("Unpexpected compare value: %v, expected %v, left: %q, right: %q", got, tc.expectRes, left.String(), right.String())
			}
		})
	}
}
