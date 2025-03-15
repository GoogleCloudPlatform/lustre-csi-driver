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

package lustre

import (
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/lustre-csi-driver/pkg/util"
)

func TestCompareInstances(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name               string
		a                  *ServiceInstance
		b                  *ServiceInstance
		expectedMismatches []string
	}{
		{
			name: "matches equal",
			a: &ServiceInstance{
				Name:        "name",
				Project:     "project",
				Location:    "location",
				CapacityGib: 16 * util.Tib,
				Network:     "network",
			},
			b: &ServiceInstance{
				Name:        "name",
				Project:     "project",
				Location:    "location",
				CapacityGib: 16 * util.Tib,
				Network:     "network",
			},
		},
		{
			name: "nothing matches",
			a: &ServiceInstance{
				Name:        "name",
				Project:     "project",
				Location:    "location",
				CapacityGib: 16 * util.Tib,
				Network:     "network",
			},
			b: &ServiceInstance{
				Name:        "name-1",
				Project:     "project-1",
				Location:    "location-1",
				CapacityGib: 24 * util.Tib,
				Network:     "network-1",
			},
			expectedMismatches: []string{
				"instance name",
				"instance project",
				"instance location",
				"instance size",
				"network name",
			},
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := CompareInstances(test.a, test.b)
			if len(test.expectedMismatches) == 0 {
				if err != nil {
					t.Errorf("test %v failed:\nexpected match,\ngot error %q", test.name, err)
				}
			} else {
				if err == nil {
					t.Errorf("test %v failed:\nexpected mismatches %q,\ngot success", test.expectedMismatches, test.name)
				} else {
					missedMismatches := []string{}
					for _, mismatch := range test.expectedMismatches {
						if !strings.Contains(err.Error(), mismatch) {
							missedMismatches = append(missedMismatches, mismatch)
						}
					}
					if len(missedMismatches) > 0 {
						t.Errorf("test %q failed:\nexpected mismatches %q,\nmissed mismatches %q", test.name, test.expectedMismatches, missedMismatches)
					}
				}
			}
		})
	}
}
