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

package util

import (
	"reflect"
	"testing"
)

func TestConvertLabelsStringToMap(t *testing.T) {
	t.Parallel()
	t.Run("parsing labels string into map", func(t *testing.T) {
		t.Parallel()
		testCases := []struct {
			name           string
			labels         string
			expectedOutput map[string]string
			expectedError  bool
		}{
			// Success test cases.
			{
				name:           "should return empty map when labels string is empty",
				labels:         "",
				expectedOutput: map[string]string{},
				expectedError:  false,
			},
			{
				name:   "single label string",
				labels: "key=value",
				expectedOutput: map[string]string{
					"key": "value",
				},
				expectedError: false,
			},
			{
				name:   "multiple label string",
				labels: "key1=value1,key2=value2",
				expectedOutput: map[string]string{
					"key1": "value1",
					"key2": "value2",
				},
				expectedError: false,
			},
			{
				name:   "multiple labels string with whitespaces gets trimmed",
				labels: "key1=value1, key2=value2",
				expectedOutput: map[string]string{
					"key1": "value1",
					"key2": "value2",
				},
				expectedError: false,
			},
			// Failure test cases.
			{
				name:           "malformed labels string (no keys and values)",
				labels:         ",,",
				expectedOutput: nil,
				expectedError:  true,
			},
			{
				name:           "malformed labels string (incorrect format)",
				labels:         "foo,bar",
				expectedOutput: nil,
				expectedError:  true,
			},
			{
				name:           "malformed labels string (missing key)",
				labels:         "key1=value1,=bar",
				expectedOutput: nil,
				expectedError:  true,
			},
			{
				name:           "malformed labels string (missing key and value)",
				labels:         "key1=value1,=bar,=",
				expectedOutput: nil,
				expectedError:  true,
			},
		}

		for _, tc := range testCases {
			output, err := ConvertLabelsStringToMap(tc.labels)
			if tc.expectedError && err == nil {
				t.Errorf("Test %q failed. Got none, but expected error", tc.name)
			}
			if err != nil {
				if !tc.expectedError {
					t.Errorf("Test %q failed. Did not expect error but got: %v", tc.name, err)
				}

				continue
			}

			if !reflect.DeepEqual(output, tc.expectedOutput) {
				t.Errorf("Test %q failed. Got labels %v, but expected %v", tc.name, output, tc.expectedOutput)
			}
		}
	})

	t.Run("checking google requirements", func(t *testing.T) {
		t.Parallel()
		testCases := []struct {
			name          string
			labels        string
			expectedError bool
		}{
			{
				name: "64 labels at most",
				labels: `k1=v,k2=v,k3=v,k4=v,k5=v,k6=v,k7=v,k8=v,k9=v,k10=v,k11=v,k12=v,k13=v,k14=v,k15=v,k16=v,k17=v,k18=v,k19=v,k20=v,
                         k21=v,k22=v,k23=v,k24=v,k25=v,k26=v,k27=v,k28=v,k29=v,k30=v,k31=v,k32=v,k33=v,k34=v,k35=v,k36=v,k37=v,k38=v,k39=v,k40=v,
                         k41=v,k42=v,k43=v,k44=v,k45=v,k46=v,k47=v,k48=v,k49=v,k50=v,k51=v,k52=v,k53=v,k54=v,k55=v,k56=v,k57=v,k58=v,k59=v,k60=v,
                         k61=v,k62=v,k63=v,k64=v,k65=v`,
				expectedError: true,
			},
			{
				name:          "label key must have atleast 1 char",
				labels:        "=v",
				expectedError: true,
			},
			{
				name:          "label key can only contain lowercase chars, digits, _ and -)",
				labels:        "k*=v",
				expectedError: true,
			},
			{
				name:          "label key can only contain lowercase chars)",
				labels:        "K=v",
				expectedError: true,
			},
			{
				name:          "label key may not have over 63 characters",
				labels:        "abcdefghijabcdefghijabcdefghijabcdefghijabcdefghijabcdefghij1234=v",
				expectedError: true,
			},
			{
				name:          "label value can only contain lowercase chars, digits, _ and -)",
				labels:        "k1=###",
				expectedError: true,
			},
			{
				name:          "label value can only contain lowercase chars)",
				labels:        "k1=V",
				expectedError: true,
			},
			{
				name:          "label key cannot contain . and /",
				labels:        "kubernetes.io/created-for/pvc/namespace=v",
				expectedError: true,
			},
			{
				name:          "label value cannot contain . and /",
				labels:        "kubernetes_io_created-for_pvc_namespace=v./",
				expectedError: true,
			},
			{
				name:          "label value may not have over 63 chars",
				labels:        "v=abcdefghijabcdefghijabcdefghijabcdefghijabcdefghijabcdefghij1234",
				expectedError: true,
			},
			{
				name:          "label key can have up to 63 chars",
				labels:        "abcdefghijabcdefghijabcdefghijabcdefghijabcdefghijabcdefghij123=v",
				expectedError: false,
			},
			{
				name:          "label value can have up to 63 chars",
				labels:        "k=abcdefghijabcdefghijabcdefghijabcdefghijabcdefghijabcdefghij123",
				expectedError: false,
			},
			{
				name:          "label key can contain - and _",
				labels:        "abcdefghijabcdefghijabcdefghijabcdefghijabcdefghijabcdefghij-_=v",
				expectedError: false,
			},
			{
				name:          "label value can contain - and _",
				labels:        "k=abcdefghijabcdefghijabcdefghijabcdefghijabcdefghijabcdefghij-_",
				expectedError: false,
			},
			{
				name:          "label value can have 0 chars",
				labels:        "kubernetes_io_created-for_pvc_namespace=",
				expectedError: false,
			},
		}

		for _, tc := range testCases {
			_, err := ConvertLabelsStringToMap(tc.labels)

			if tc.expectedError && err == nil {
				t.Errorf("Test %q failed. Got none, but expected error", tc.name)
			}

			if !tc.expectedError && err != nil {
				t.Errorf("Test %q failed. Did not expect error but got: %v", tc.name, err)
			}
		}
	})
}

func TestGetRegionFromZone(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name           string
		location       string
		expectedOutput string
		expectedError  bool
	}{
		{
			name:           "Standard zone",
			location:       "us-central1-a",
			expectedOutput: "us-central1",
		},
		{
			name:          "Standard region",
			location:      "us-central1",
			expectedError: true,
		},
		{
			name:          "Multi-part region",
			location:      "northamerica-northeast1",
			expectedError: true,
		},
		{
			name:           "Multi-part zone",
			location:       "northamerica-northeast1-a",
			expectedOutput: "northamerica-northeast1",
		},
		{
			name:          "Empty location",
			location:      "",
			expectedError: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			output, err := GetRegionFromZone(tc.location)
			if tc.expectedError {
				if err == nil {
					t.Errorf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if output != tc.expectedOutput {
				t.Errorf("expected %q, got %q", tc.expectedOutput, output)
			}
		})
	}
}

func TestParseEndpoint(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name            string
		endpoint        string
		expectedScheme  string
		expectedAddress string
		expectedError   bool
	}{
		{
			name:            "should parse unix endpoint correctly",
			endpoint:        "unix:/csi/csi.sock",
			expectedScheme:  "unix",
			expectedAddress: "/csi/csi.sock",
			expectedError:   false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			scheme, address, err := ParseEndpoint(tc.endpoint, false)
			if tc.expectedError && err == nil {
				t.Error("Got none, but expected error")
			}
			if err != nil {
				if !tc.expectedError {
					t.Errorf("Got error %v, but did not expect one", err)
				}

				return
			}
			if !reflect.DeepEqual(scheme, tc.expectedScheme) {
				t.Errorf("Got scheme %v, but expected %v", scheme, tc.expectedScheme)
			}

			if !reflect.DeepEqual(address, tc.expectedAddress) {
				t.Errorf("Got address %v, but expected %v", address, tc.expectedAddress)
			}
		})
	}
}
