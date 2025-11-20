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

package labelcontroller

import (
	"context"
	"fmt"
	"strings"
	"testing"

	longrunningpb "cloud.google.com/go/longrunning/autogen/longrunningpb"
	"github.com/GoogleCloudPlatform/lustre-csi-driver/pkg/cloud_provider/lustre"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	crfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// mockLustreService is a mock implementation of the lustre.Service interface for testing.
type mockLustreService struct {
	instances   map[string]*lustre.ServiceInstance
	getError    error
	updateError error
}

func newMockLustreService() *mockLustreService {
	return &mockLustreService{
		instances: make(map[string]*lustre.ServiceInstance),
	}
}

func (m *mockLustreService) CreateInstance(_ context.Context, _ *lustre.ServiceInstance) (*lustre.ServiceInstance, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *mockLustreService) DeleteInstance(_ context.Context, _ *lustre.ServiceInstance) error {
	return fmt.Errorf("not implemented")
}

func (m *mockLustreService) UpdateInstance(_ context.Context, instance *lustre.ServiceInstance) error {
	if m.updateError != nil {
		return m.updateError
	}
	// Only update labels if there's no error
	if m.instances[instance.Name] != nil {
		m.instances[instance.Name].Labels = instance.Labels
	}

	return nil
}

func (m *mockLustreService) ResizeInstance(_ context.Context, _ *lustre.ServiceInstance) (*lustre.ServiceInstance, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *mockLustreService) GetInstance(_ context.Context, instance *lustre.ServiceInstance) (*lustre.ServiceInstance, error) {
	if m.getError != nil {
		return nil, m.getError
	}
	if inst, ok := m.instances[instance.Name]; ok {
		return inst, nil
	}

	return nil, &errors.StatusError{ErrStatus: metav1.Status{Reason: metav1.StatusReasonNotFound}}
}

func (m *mockLustreService) ListInstance(_ context.Context, _ *lustre.ListFilter) ([]*lustre.ServiceInstance, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *mockLustreService) GetCreateInstanceOp(_ context.Context, _ *lustre.ServiceInstance) (*longrunningpb.Operation, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *mockLustreService) ListLocations(_ context.Context, _ *lustre.ListFilter) ([]string, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *mockLustreService) IsOperationInProgress(_ context.Context, _ *lustre.ServiceInstance, _ string) (bool, error) {
	return false, fmt.Errorf("not implemented")
}

func TestPvReconciler_Reconcile(t *testing.T) {
	t.Parallel()
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	tests := []struct {
		name           string
		pv             *corev1.PersistentVolume
		lustreInstance *lustre.ServiceInstance
		getError       error
		updateError    error
		expectedError  bool
		expectedLabels map[string]string
	}{
		{
			name: "PV with missing label - should add label",
			pv: &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pv",
				},
				Spec: corev1.PersistentVolumeSpec{
					PersistentVolumeSource: corev1.PersistentVolumeSource{
						CSI: &corev1.CSIPersistentVolumeSource{
							Driver:       csiDriverName,
							VolumeHandle: "projects/test-project/locations/us-central1-a/instances/test-instance",
						},
					},
				},
			},
			lustreInstance: &lustre.ServiceInstance{
				Name:   "test-instance",
				Labels: map[string]string{},
			},
			expectedLabels: map[string]string{managedByLabelKey: managedByLabelValue},
		},
		{
			name: "PV with existing label - should do nothing",
			pv: &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pv-labeled",
				},
				Spec: corev1.PersistentVolumeSpec{
					PersistentVolumeSource: corev1.PersistentVolumeSource{
						CSI: &corev1.CSIPersistentVolumeSource{
							Driver:       csiDriverName,
							VolumeHandle: "projects/test-project/locations/us-central1-a/instances/test-instance-labeled",
						},
					},
				},
			},
			lustreInstance: &lustre.ServiceInstance{
				Name:   "test-instance-labeled",
				Labels: map[string]string{managedByLabelKey: managedByLabelValue},
			},
			expectedLabels: map[string]string{managedByLabelKey: managedByLabelValue},
		},
		{
			name: "PV with wrong CSI driver - should do nothing",
			pv: &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pv-wrong-driver",
				},
				Spec: corev1.PersistentVolumeSpec{
					PersistentVolumeSource: corev1.PersistentVolumeSource{
						CSI: &corev1.CSIPersistentVolumeSource{
							Driver:       "other-csi-driver",
							VolumeHandle: "projects/test-project/locations/us-central1-a/instances/test-instance",
						},
					},
				},
			},
			lustreInstance: &lustre.ServiceInstance{
				Name:   "test-instance",
				Labels: map[string]string{},
			},
			expectedLabels: map[string]string{},
		},
		{
			name: "PV not found - should return nil",
			pv: &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name: "non-existent-pv",
				},
			},
			lustreInstance: &lustre.ServiceInstance{
				Name:   "non-existent-instance",
				Labels: map[string]string{},
			},
			expectedError:  false,
			expectedLabels: map[string]string{},
		},
		{
			name: "Invalid volume handle format - should do nothing",
			pv: &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pv-invalid-handle",
				},
				Spec: corev1.PersistentVolumeSpec{
					PersistentVolumeSource: corev1.PersistentVolumeSource{
						CSI: &corev1.CSIPersistentVolumeSource{
							Driver:       csiDriverName,
							VolumeHandle: "invalid-handle",
						},
					},
				},
			},
			lustreInstance: &lustre.ServiceInstance{
				Name:   "test-instance",
				Labels: map[string]string{},
			},
			expectedError:  true,
			expectedLabels: map[string]string{},
		},
		{
			name: "Error getting Lustre instance - should return error",
			pv: &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pv-get-error",
				},
				Spec: corev1.PersistentVolumeSpec{
					PersistentVolumeSource: corev1.PersistentVolumeSource{
						CSI: &corev1.CSIPersistentVolumeSource{
							Driver:       csiDriverName,
							VolumeHandle: "projects/test-project/locations/us-central1-a/instances/error-instance",
						},
					},
				},
			},
			lustreInstance: &lustre.ServiceInstance{
				Name:   "error-instance",
				Labels: map[string]string{},
			},
			getError:       fmt.Errorf("failed to get instance"),
			expectedError:  true,
			expectedLabels: map[string]string{},
		},
		{
			name: "Error updating Lustre instance - should return error",
			pv: &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pv-update-error",
				},
				Spec: corev1.PersistentVolumeSpec{
					PersistentVolumeSource: corev1.PersistentVolumeSource{
						CSI: &corev1.CSIPersistentVolumeSource{
							Driver:       csiDriverName,
							VolumeHandle: "projects/test-project/locations/us-central1-a/instances/update-error-instance",
						},
					},
				},
			},
			lustreInstance: &lustre.ServiceInstance{
				Name:   "update-error-instance",
				Labels: map[string]string{"initial-label": "initial-value"},
			},
			updateError:    fmt.Errorf("failed to update instance"),
			expectedError:  true,
			expectedLabels: map[string]string{"initial-label": "initial-value"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			mockLustre := newMockLustreService()
			if test.lustreInstance != nil {
				mockLustre.instances[test.lustreInstance.Name] = test.lustreInstance
			}
			mockLustre.getError = test.getError
			mockLustre.updateError = test.updateError

			cloud := &lustre.Cloud{
				LustreService: mockLustre,
				Project:       "test-project",
				Zone:          "us-central1-a",
			}

			client := crfake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(test.pv).Build()
			reconciler := &PvReconciler{
				Client: client,
				Scheme: scheme,
				Log:    logr.Discard(),
				Cloud:  cloud,
				volumeIDToInstance: func(volumeID string) (*lustre.ServiceInstance, error) {
					if strings.Contains(volumeID, "invalid") {
						return nil, fmt.Errorf("invalid volume ID")
					}
					if strings.Contains(volumeID, "error-instance") {
						return nil, fmt.Errorf("failed to get instance")
					}

					return &lustre.ServiceInstance{
						Name: test.lustreInstance.Name,
					}, nil
				},
			}

			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name: test.pv.Name,
				},
			}

			_, err := reconciler.Reconcile(t.Context(), req)

			if test.expectedError {
				if err == nil {
					t.Errorf("Expected an error but got none")
				}
			} else {
				if err != nil {
					t.Errorf("Did not expect an error but got: %v", err)
				}
			}

			if test.lustreInstance != nil {
				updatedInstance := mockLustre.instances[test.lustreInstance.Name]
				if updatedInstance == nil {
					if len(test.expectedLabels) > 0 {
						t.Errorf("Expected instance to be updated with labels, but it was not found")
					}
				} else {
					if len(updatedInstance.Labels) != len(test.expectedLabels) {
						t.Errorf("Expected %d labels, got %d. Expected: %v, Got: %v", len(test.expectedLabels), len(updatedInstance.Labels), test.expectedLabels, updatedInstance.Labels)
					}
					for k, v := range test.expectedLabels {
						if updatedInstance.Labels[k] != v {
							t.Errorf("Expected label %s to be %s, got %s", k, v, updatedInstance.Labels[k])
						}
					}
				}
			}
		})
	}
}
