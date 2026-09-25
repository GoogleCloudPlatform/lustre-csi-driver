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

package main

import (
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const stockoutEvent = `I0921 19:44:33.360659   76127 dump.go:53] At 2026-09-21 18:47:36 +0000 UTC - event for lustre-csi-storage-class5stzt: {lustre.csi.storage.gke.io_lustre-csi-controller } ProvisioningFailed: failed to provision volume with StorageClass "multivolume-5257-e2e-sc9ww7m": rpc error: code = ResourceExhausted desc = rpc error: code = ResourceExhausted desc = resource exhausted: not enough resources available to fulfill the request in us-west2-b`

func TestMarkStockoutFailure(t *testing.T) {
	tests := []struct {
		name     string
		tc       TestCase
		wantMark bool
	}{
		{
			name: "stockout event in system-err",
			tc: TestCase{
				SystemErr: stockoutEvent,
				Failure:   &Failure{Message: "PVC not bound", Text: "[FAILED] PVC not bound"},
			},
			wantMark: true,
		},
		{
			name: "stockout event in system-out",
			tc: TestCase{
				SystemOut: stockoutEvent,
				Failure:   &Failure{Message: "PVC not bound", Text: "[FAILED] PVC not bound"},
			},
			wantMark: true,
		},
		{
			name: "ResourceExhausted without the stockout message is not tagged",
			tc: TestCase{
				SystemErr: "rpc error: code = ResourceExhausted desc = googleapi: Error 429: Too Many Requests",
				Failure:   &Failure{Message: "PVC not bound", Text: "[FAILED] PVC not bound"},
			},
			wantMark: false,
		},
		{
			name: "stockout message without the gRPC code",
			tc: TestCase{
				SystemErr: "ProvisioningFailed: not enough resources available to fulfill the request in us-west2-b",
				Failure:   &Failure{Message: "PVC not bound", Text: "[FAILED] PVC not bound"},
			},
			wantMark: true,
		},
		{
			name: "unrelated failure",
			tc: TestCase{
				SystemErr: "rpc error: code = Internal desc = failed to mount",
				Failure:   &Failure{Message: "unexpected error", Text: "[FAILED] unexpected error"},
			},
			wantMark: false,
		},
		{
			name: "passing test with stockout event",
			tc: TestCase{
				SystemErr: stockoutEvent,
			},
			wantMark: false,
		},
		{
			name: "already marked",
			tc: TestCase{
				SystemErr: stockoutEvent,
				Failure:   &Failure{Message: stockoutMarker + "PVC not bound", Text: stockoutMarker + "[FAILED] PVC not bound"},
			},
			wantMark: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			markStockoutFailure(&tt.tc)
			if tt.tc.Failure == nil {
				if tt.wantMark {
					t.Fatalf("expected a failure to be marked, got no failure")
				}

				return
			}
			gotMark := strings.HasPrefix(tt.tc.Failure.Message, stockoutMarker)
			if gotMark != tt.wantMark {
				t.Errorf("message marked = %v, want %v (message %q)", gotMark, tt.wantMark, tt.tc.Failure.Message)
			}
			if gotMark != strings.HasPrefix(tt.tc.Failure.Text, stockoutMarker) {
				t.Errorf("message and body disagree: message %q, body %q", tt.tc.Failure.Message, tt.tc.Failure.Text)
			}
			if strings.Count(tt.tc.Failure.Message, stockoutMarker) > 1 {
				t.Errorf("marker added more than once: %q", tt.tc.Failure.Message)
			}
		})
	}
}

// TestMergeJUnitStockout checks that MergeJUnit keeps the failure message
// attribute and system-err from Ginkgo output, and marks the stockout failure.
func TestMergeJUnitStockout(t *testing.T) {
	src := t.TempDir()
	input := `<?xml version="1.0" encoding="UTF-8"?>
<testsuites>
  <testsuite name="Kubernetes e2e suite">
    <testcase name="External Storage [Driver: lustre] stockout test" classname="Kubernetes e2e suite" status="failed" time="300">
      <failure message="PersistentVolumeClaims [pvc-abc] not all in phase Bound within 5m0s" type="failed">[FAILED] PersistentVolumeClaims [pvc-abc] not all in phase Bound within 5m0s</failure>
      <system-err>` + stockoutEvent + `</system-err>
    </testcase>
    <testcase name="External Storage [Driver: lustre] real failure" classname="Kubernetes e2e suite" status="failed" time="10">
      <failure message="unexpected error" type="failed">[FAILED] unexpected error</failure>
      <system-err>nothing interesting</system-err>
    </testcase>
    <testcase name="External Storage [Driver: lustre] passing test" classname="Kubernetes e2e suite" status="passed" time="5"></testcase>
  </testsuite>
</testsuites>`
	if err := os.WriteFile(filepath.Join(src, "junit_01.xml"), []byte(input), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "junit_merged.xml")
	if err := MergeJUnit("External Storage", []string{src}, dst); err != nil {
		t.Fatalf("MergeJUnit failed: %v", err)
	}

	data, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	var merged TestSuite
	if err := xml.Unmarshal(data, &merged); err != nil {
		t.Fatalf("failed to parse merged junit: %v\n%s", err, data)
	}
	got := map[string]TestCase{}
	for _, tc := range merged.TestCases {
		got[tc.Name] = tc
	}

	stockout := got["External Storage [Driver: lustre] stockout test"]
	if stockout.Failure == nil || !strings.HasPrefix(stockout.Failure.Message, stockoutMarker) {
		t.Errorf("stockout test not marked: %+v", stockout.Failure)
	}
	if !strings.Contains(stockout.SystemErr, "not enough resources available") {
		t.Errorf("system-err was not preserved: %q", stockout.SystemErr)
	}

	real := got["External Storage [Driver: lustre] real failure"]
	if real.Failure == nil || real.Failure.Message != "unexpected error" {
		t.Errorf("real failure should keep its original message, got %+v", real.Failure)
	}

	if passing := got["External Storage [Driver: lustre] passing test"]; passing.Failure != nil {
		t.Errorf("passing test should have no failure, got %+v", passing.Failure)
	}
}

// ginkgoJUnit wraps test cases in the envelope that Ginkgo writes.
func ginkgoJUnit(testcases ...string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<testsuites>
  <testsuite name="Kubernetes e2e suite">
` + strings.Join(testcases, "\n") + `
  </testsuite>
</testsuites>`
}

const (
	stockoutCase = `<testcase name="External Storage [Driver: lustre] stockout test">
      <failure message="PVC not bound" type="failed">[FAILED] PVC not bound</failure>
      <system-err>` + stockoutEvent + `</system-err>
    </testcase>`
	realFailureCase = `<testcase name="External Storage [Driver: lustre] real failure">
      <failure message="unexpected error" type="failed">[FAILED] unexpected error</failure>
    </testcase>`
	passingCase     = `<testcase name="External Storage [Driver: lustre] passing test"></testcase>`
	interruptedCase = `<testcase name="External Storage [Driver: lustre] interrupted test">
      <error message="interrupted by timeout" type="interrupted">[INTERRUPTED] interrupted by timeout</error>
      <system-err>` + stockoutEvent + `</system-err>
    </testcase>`
	beforeSuiteCase = `<testcase name="[SynchronizedBeforeSuite]">
      <failure message="setup failed" type="failed">[FAILED] setup failed</failure>
    </testcase>`
	// kubetest2 writes junit_runner.xml with a single testsuite root.
	kubetest2Runner = `<testsuite name="kubetest2"><testcase name="Test" classname="kubetest2">
      <failure message="exit status 255" type="">exit status 255</failure>
    </testcase></testsuite>`
)

func TestOnlyStockoutFailures(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
		want  bool
	}{
		{
			name:  "only stockout failures",
			files: map[string]string{"junit_01.xml": ginkgoJUnit(stockoutCase, passingCase)},
			want:  true,
		},
		{
			name: "stockout failures in several files",
			files: map[string]string{
				"junit_01.xml": ginkgoJUnit(stockoutCase),
				"junit_02.xml": ginkgoJUnit(stockoutCase, passingCase),
			},
			want: true,
		},
		{
			name: "failing kubetest2 runner result is ignored",
			files: map[string]string{
				"junit_01.xml":     ginkgoJUnit(stockoutCase),
				"junit_runner.xml": kubetest2Runner,
			},
			want: true,
		},
		{
			name:  "stockout and a real failure",
			files: map[string]string{"junit_01.xml": ginkgoJUnit(stockoutCase, realFailureCase)},
			want:  false,
		},
		{
			name: "real failure in another file",
			files: map[string]string{
				"junit_01.xml": ginkgoJUnit(stockoutCase),
				"junit_02.xml": ginkgoJUnit(realFailureCase),
			},
			want: false,
		},
		{
			name:  "interrupted test with a stockout event",
			files: map[string]string{"junit_01.xml": ginkgoJUnit(stockoutCase, interruptedCase)},
			want:  false,
		},
		{
			name:  "failed suite node",
			files: map[string]string{"junit_01.xml": ginkgoJUnit(stockoutCase, beforeSuiteCase)},
			want:  false,
		},
		{
			name:  "no failures",
			files: map[string]string{"junit_01.xml": ginkgoJUnit(passingCase)},
			want:  false,
		},
		{
			name:  "no ginkgo results",
			files: map[string]string{"junit_runner.xml": kubetest2Runner},
			want:  false,
		},
		{
			name:  "malformed junit",
			files: map[string]string{"junit_01.xml": "not xml"},
			want:  false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, content := range tt.files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if got := onlyStockoutFailures(dir); got != tt.want {
				t.Errorf("onlyStockoutFailures() = %v, want %v", got, tt.want)
			}
		})
	}

	t.Run("missing directory", func(t *testing.T) {
		if onlyStockoutFailures(filepath.Join(t.TempDir(), "missing")) {
			t.Errorf("onlyStockoutFailures() = true for a missing directory, want false")
		}
	})
}
