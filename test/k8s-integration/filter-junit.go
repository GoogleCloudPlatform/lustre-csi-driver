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
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"k8s.io/klog/v2"
)

/*
 * TestSuite represents a JUnit file. Due to how encoding/xml works, we have
 * represent all fields that we want to be passed through. It's therefore
 * not a complete solution, but good enough for Ginkgo + Spyglass.
 */

type TestSuites struct {
	XMLName   string      `xml:"testsuites"`
	TestSuite []TestSuite `xml:"testsuite"`
}
type TestSuite struct {
	XMLName   string     `xml:"testsuite"`
	TestCases []TestCase `xml:"testcase"`
}

type TestCase struct {
	Name      string     `xml:"name,attr"`
	Time      string     `xml:"time,attr"`
	SystemOut string     `xml:"system-out,omitempty"`
	SystemErr string     `xml:"system-err,omitempty"`
	Failure   *Failure   `xml:"failure,omitempty"`
	Skipped   SkipReason `xml:"skipped,omitempty"`
}

// Failure keeps the message attribute as well as the body, because TestGrid
// reads the message attribute first when it is present.
type Failure struct {
	Message string `xml:"message,attr,omitempty"`
	Type    string `xml:"type,attr,omitempty"`
	Text    string `xml:",chardata"`
}

// stockoutMarker is prepended to the failure of a test that hit a Lustre
// stockout. The TestGrid config for the Lustre CSI dashboards matches this
// prefix and shows those failures as CATEGORIZED_ABORT so they do not alert.
const stockoutMarker = "[Infrastructure Failure] Lustre stockout: "

// stockoutRE matches the Lustre API stockout message on its own rather than
// the full error string, because the driver and the API wrap it in several
// layers whose text can change. The ResourceExhausted code alone is not
// matched, because quota errors and HTTP 429 use it too and should still alert.
var stockoutRE = regexp.MustCompile(`not enough resources available to fulfill the request`)

// markStockoutFailure prefixes the failure with stockoutMarker if the test
// output shows a Lustre stockout. Ginkgo writes the framework log, which
// includes the namespace event dump, to system-err.
func markStockoutFailure(tc *TestCase) {
	if tc.Failure == nil || strings.HasPrefix(tc.Failure.Message, stockoutMarker) {
		return
	}
	if !stockoutRE.MatchString(tc.SystemErr) && !stockoutRE.MatchString(tc.SystemOut) && !stockoutRE.MatchString(tc.Failure.Text) {
		return
	}
	tc.Failure.Message = stockoutMarker + tc.Failure.Message
	tc.Failure.Text = stockoutMarker + tc.Failure.Text
}

// SkipReason deals with the special <skipped></skipped>:
// if present, we must re-encode it, even if empty.
type SkipReason string

func (s *SkipReason) UnmarshalText(text []byte) {
	*s = SkipReason(text)
	if *s == "" {
		*s = " "
	}
}

func (s SkipReason) MarshalText() []byte {
	if s == " " {
		return []byte{}
	}

	return []byte(s)
}

// MergeJUnit merges all junit xml files found in sourceDirectories into a single xml file at destination, using the filter.
// The merging removes duplicate skipped tests. The original files are deleted.
func MergeJUnit(testFilter string, sourceDirectories []string, destination string) error {
	var data []byte

	re := regexp.MustCompile(testFilter)

	var mergeErrors []string
	var filesToDelete []string

	// Keep only matching testcases. Testcases skipped in all test runs are only stored once.
	filtered := map[string]TestCase{}
	for _, dir := range sourceDirectories {
		files, err := os.ReadDir(dir)
		if err != nil {
			klog.Errorf("Failed to read juint directory %s: %v", dir, err)
			mergeErrors = append(mergeErrors, err.Error())

			continue
		}
		for _, file := range files {
			if !strings.HasSuffix(file.Name(), ".xml") || file.Name() == "junit_runner.xml" {
				continue
			}
			fullFilename := filepath.Join(dir, file.Name())
			filesToDelete = append(filesToDelete, fullFilename)
			data, err := os.ReadFile(fullFilename)
			if err != nil {
				return err
			}
			var testSuiteData TestSuites
			if err = xml.Unmarshal(data, &testSuiteData); err != nil {
				return fmt.Errorf("failed to unmarshal XML file %v: %w", fullFilename, err)
			}

			for _, testsuite := range testSuiteData.TestSuite {
				for _, testcase := range testsuite.TestCases {
					if !re.MatchString(testcase.Name) {
						continue
					}
					entry, ok := filtered[testcase.Name]
					if !ok || // not present yet
						entry.Skipped != "" && testcase.Skipped == "" { // replaced skipped test with real test run
						filtered[testcase.Name] = testcase
					}
				}
			}
		}
	}

	// Keep only matching testcases. Testcases skipped in all test runs are only stored once.
	var junit TestSuite
	junit.TestCases = nil
	for _, testcase := range filtered {
		markStockoutFailure(&testcase)
		junit.TestCases = append(junit.TestCases, testcase)
	}

	// Re-encode.
	data, err := xml.MarshalIndent(junit, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal junit data: %w", err)
	}

	if err = os.WriteFile(destination, data, 0o644); err != nil {
		return err
	}

	if mergeErrors != nil {
		return fmt.Errorf("problems reading junit files; partial merge has been performed: %s", strings.Join(mergeErrors, " "))
	}
	// Only delete original files if everything went well.
	var removeErrors []string
	for _, filename := range filesToDelete {
		if err := os.Remove(filename); err != nil {
			removeErrors = append(removeErrors, err.Error())
		}
	}
	if removeErrors != nil {
		return fmt.Errorf("problem removing original junit results: %s", strings.Join(removeErrors, " "))
	}

	return nil
}
