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

package pcc

import (
	"testing"
)

func TestParseConfigAndBuildLctlParam(t *testing.T) {
	tests := []struct {
		name      string
		vc        map[string]string
		wantEn    bool
		wantParam string
		wantErr   bool
	}{
		{
			name:   "disabled by default",
			vc:     map[string]string{},
			wantEn: false,
		},
		{
			name: "one-knob default enable-pcc-cache",
			vc: map[string]string{
				KeyEnablePCCCache: "true",
			},
			wantEn:    true,
			wantParam: "projid={0} rwid=1 roid=1 pccro=1 auto_attach=1 open_attach=1",
		},
		{
			name: "include patterns and max file size override",
			vc: map[string]string{
				KeyEnablePCCCache:     "true",
				KeyPCCIncludePatterns: "*.safetensors,*.bin",
				KeyPCCMaxFileSize:     "100G",
				KeyPCCCacheSize:       "10Gi",
			},
			wantEn:    true,
			wantParam: "fname={*.safetensors,*.bin}&size<100G rwid=1 roid=1 pccro=1 auto_attach=1 open_attach=1",
		},
		{
			name: "invalid cache size",
			vc: map[string]string{
				KeyEnablePCCCache: "true",
				KeyPCCCacheSize:   "100Mi",
			},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := ParseConfig(tc.vc)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cfg.Enabled != tc.wantEn {
				t.Fatalf("Enabled = %v, want %v", cfg.Enabled, tc.wantEn)
			}
			if cfg.Enabled {
				if got := cfg.BuildLctlParam(); got != tc.wantParam {
					t.Errorf("BuildLctlParam() = %q, want %q", got, tc.wantParam)
				}
			}
		})
	}
}
