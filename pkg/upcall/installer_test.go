/*
Copyright 2026 Google LLC

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

package upcall

import "testing"

func TestLustreSupportsGSSIAM(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		want    bool
		wantVer string
		wantErr bool
	}{
		{
			name:    "Lustre 2.16.0_ddn60",
			input:   "2.16.0_ddn60\n",
			want:    true,
			wantVer: "2.16.0_ddn60",
		},
		{
			name:    "Lustre version=2.16.0_ddn60 prefix",
			input:   "version=2.16.0_ddn60\n",
			want:    true,
			wantVer: "2.16.0_ddn60",
		},
		{
			name:    "Lustre 2.16.0-ddn60-1 debian format",
			input:   "2.16.0-ddn60-1\n",
			want:    true,
			wantVer: "2.16.0-ddn60-1",
		},
		{
			name:    "Lustre 2.16 minor-only",
			input:   "2.16\n",
			want:    true,
			wantVer: "2.16",
		},
		{
			name:    "Lustre 2.17.0",
			input:   "2.17.0\n",
			want:    true,
			wantVer: "2.17.0",
		},
		{
			name:    "Lustre 3.0.0",
			input:   "3.0.0\n",
			want:    true,
			wantVer: "3.0.0",
		},
		{
			name:    "Lustre 2.14.0_ddn255 (< 2.16)",
			input:   "2.14.0_ddn255\n",
			want:    false,
			wantVer: "2.14.0_ddn255",
		},
		{
			name:    "Lustre 2.14.0-ddn255-1 (< 2.16)",
			input:   "2.14.0-ddn255-1\n",
			want:    false,
			wantVer: "2.14.0-ddn255-1",
		},
		{
			name:    "Lustre 2.15.99 (< 2.16)",
			input:   "2.15.99\n",
			want:    false,
			wantVer: "2.15.99",
		},
		{
			name:    "Lustre 1.8.9 (< 2.16)",
			input:   "1.8.9\n",
			want:    false,
			wantVer: "1.8.9",
		},
		{
			name:    "empty output",
			input:   "   \n",
			wantErr: true,
		},
		{
			name:    "unparseable version output",
			input:   "unknown\n",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, gotVer, err := lustreSupportsGSSIAM(tc.input)
			if (err != nil) != tc.wantErr {
				t.Fatalf("lustreSupportsGSSIAM(%q) error = %v, wantErr %v", tc.input, err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if got != tc.want {
				t.Errorf("lustreSupportsGSSIAM(%q) = %v, want %v", tc.input, got, tc.want)
			}
			if gotVer != tc.wantVer {
				t.Errorf("lustreSupportsGSSIAM(%q) ver = %q, want %q", tc.input, gotVer, tc.wantVer)
			}
		})
	}
}
