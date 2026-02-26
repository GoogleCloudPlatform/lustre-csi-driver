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
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"

	"k8s.io/klog/v2"
)

const (
	Gib = 1024 * 1024 * 1024
	Tib = 1024 * Gib
	Pib = 1024 * Tib
)

// ConvertLabelsStringToMap converts the labels from string to map
// example: "key1=value1,key2=value2" gets converted into {"key1": "value1", "key2": "value2"}
func ConvertLabelsStringToMap(labels string) (map[string]string, error) {
	const labelsDelimiter = ","
	const labelsKeyValueDelimiter = "="

	labelsMap := make(map[string]string)
	if labels == "" {
		return labelsMap, nil
	}

	// Following rules enforced for label keys
	// 1. Keys have a minimum length of 1 character and a maximum length of 63 characters, and cannot be empty.
	// 2. Keys and values can contain only lowercase letters, numeric characters, underscores, and dashes.
	// 3. Keys must start with a lowercase letter.
	regexKey := regexp.MustCompile(`^\p{Ll}[\p{Ll}0-9_-]{0,62}$`)
	checkLabelKeyFn := func(key string) error {
		if !regexKey.MatchString(key) {
			return fmt.Errorf("label value %q is invalid (should start with lowercase letter / lowercase letter, digit, _ and - chars are allowed / 1-63 characters", key)
		}

		return nil
	}

	// Values can be empty, and have a maximum length of 63 characters.
	regexValue := regexp.MustCompile(`^[\p{Ll}0-9_-]{0,63}$`)
	checkLabelValueFn := func(value string) error {
		if !regexValue.MatchString(value) {
			return fmt.Errorf("label value %q is invalid (lowercase letter, digit, _ and - chars are allowed / 0-63 characters", value)
		}

		return nil
	}

	keyValueStrings := strings.Split(labels, labelsDelimiter)
	for _, keyValue := range keyValueStrings {
		keyValue := strings.Split(keyValue, labelsKeyValueDelimiter)

		if len(keyValue) != 2 {
			return nil, fmt.Errorf("labels %q are invalid, correct format: 'key1=value1,key2=value2'", labels)
		}

		key := strings.TrimSpace(keyValue[0])
		if err := checkLabelKeyFn(key); err != nil {
			return nil, err
		}

		value := strings.TrimSpace(keyValue[1])
		if err := checkLabelValueFn(value); err != nil {
			return nil, err
		}

		labelsMap[key] = value
	}

	const maxNumberOfLabels = 64
	if len(labelsMap) > maxNumberOfLabels {
		return nil, fmt.Errorf("more than %d labels is not allowed, given: %d", maxNumberOfLabels, len(labelsMap))
	}

	return labelsMap, nil
}

func ParseEndpoint(endpoint string, cleanupSocket bool) (string, string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		klog.Fatal(err.Error())
	}

	var addr string
	switch u.Scheme {
	case "unix":
		addr = u.Path
		if cleanupSocket {
			if err := os.Remove(addr); err != nil && !os.IsNotExist(err) {
				klog.Fatalf("Failed to remove %s, error: %s", addr, err)
			}
		}
	case "tcp":
		addr = u.Host
	default:
		klog.Fatalf("%v endpoint scheme not supported", u.Scheme)
	}

	return u.Scheme, addr, nil
}

// ParseVolumeID parses the volume ID into project, location, and instance name.
// A valid volume ID should follow one of these formats:
//
//	"PROJECT_ID/LOCATION/INSTANCE_NAME"
//
// or
//
//	"PROJECT_ID/LOCATION/INSTANCE_NAME:RANDOM_SUFFIX"
//
// The second format allows customers to specify a fake volume handle for static provisioning,
// enabling multiple PVs in the same pod to mount the same lustre instance. This prevents kubelet from
// skipping mounts of volumes with the same volume handle, which can cause the pod to be stuck in container creation.
func ParseVolumeID(id string) (string, string, string, error) {
	// Split off the optional suffix after ":"
	realID := id
	if colonIndex := strings.Index(id, ":"); colonIndex != -1 {
		realID = id[:colonIndex]
	}

	tokens := strings.Split(realID, "/")
	if len(tokens) != 3 {
		return "", "", "", fmt.Errorf(
			"invalid volume ID %q: expected format PROJECT_ID/LOCATION/INSTANCE_NAME", id)
	}
	project := tokens[0]
	location := tokens[1]
	instanceName := tokens[2]
	if project == "" || location == "" || instanceName == "" {
		return "", "", "", fmt.Errorf(
			"invalid volume ID %q: expected format PROJECT_ID/LOCATION/INSTANCE_NAME", id)
	}

	return project, location, instanceName, nil
}

// GetRegionFromZone return the corresponding region name based on a zone name.
// Example "us-central1-a" return "us-central1".
// Returns an error if the location is not a zone.
func GetRegionFromZone(location string) (string, error) {
	if location == "" {
		return "", fmt.Errorf("failed to parse location: location must be provided")
	}
	tokens := strings.Split(location, "-")
	if len(tokens) != 3 {
		return "", fmt.Errorf("failed to parse location %s: not a valid zone", location)
	}

	return strings.Join(tokens[:2], "-"), nil
}
