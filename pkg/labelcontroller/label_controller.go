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

	"github.com/GoogleCloudPlatform/lustre-csi-driver/pkg/cloud_provider/lustre"
	"github.com/GoogleCloudPlatform/lustre-csi-driver/pkg/metrics"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	managedByLabelKey   = "storage_gke_io_managed-by"
	createdByLabelKey   = "storage_gke_io_created-by"
	lustreCSILabelValue = "lustre_csi_storage_gke_io"

	csiDriverName    = "lustre.csi.storage.gke.io"
	leaderElectionID = "lustre-csi-label-controller-leader-election"
)

type PvReconciler struct {
	client.Client
	Scheme             *runtime.Scheme
	Log                logr.Logger
	Cloud              *lustre.Cloud
	MetricsManager     *metrics.Manager
	volumeIDToInstance func(string) (*lustre.ServiceInstance, error)
}

func (r *PvReconciler) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	pv := &corev1.PersistentVolume{}
	if err := r.Get(ctx, request.NamespacedName, pv); err != nil {
		if errors.IsNotFound(err) {
			return reconcile.Result{}, nil
		}

		return reconcile.Result{}, fmt.Errorf("failed to get PV %s: %w", request.Name, err)
	}

	if pv.Spec.CSI == nil || pv.Spec.CSI.Driver != csiDriverName {
		return reconcile.Result{}, nil
	}

	instance, err := r.volumeIDToInstance(pv.Spec.CSI.VolumeHandle)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to get lustre instance for PV %s: %w", pv.Name, err)
	}

	lustreInstance, err := r.Cloud.LustreService.GetInstance(ctx, instance)
	if err != nil {
		if lustre.IsNotFoundErr(err) {
			klog.Warningf("Lustre instance for PV %s not found", pv.Name)

			return reconcile.Result{}, nil
		}

		if lustre.IsPermissionDeniedErr(err) {
			klog.Warningf("Lustre instance for PV %s is not accessible (permission denied). Ensure the CSI driver service account has 'lustre.instances.get' permission.", pv.Name)

			return reconcile.Result{}, nil
		}

		return reconcile.Result{}, fmt.Errorf("failed to get lustre instance for PV %s: %w", pv.Name, err)
	}

	for _, key := range []string{managedByLabelKey, createdByLabelKey} {
		if lustreInstance.Labels[key] == lustreCSILabelValue {
			klog.Infof("Lustre instance for PV %s already has the %s label", pv.Name, key)

			return reconcile.Result{}, nil
		}
	}

	klog.Infof("Adding managed-by label to PV %s", pv.Name)
	if lustreInstance.Labels == nil {
		lustreInstance.Labels = make(map[string]string)
	}
	lustreInstance.Labels[managedByLabelKey] = lustreCSILabelValue

	err = r.Cloud.LustreService.UpdateInstance(ctx, lustreInstance)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to update lustre instance for PV %s: %w", pv.Name, err)
	}

	r.MetricsManager.RecordSuccessfullyLabeledVolume()

	return reconcile.Result{}, nil
}

func Start(ctx context.Context, cloud *lustre.Cloud, mm *metrics.Manager, leaderElectionNamespace string) error {
	log.SetLogger(zap.New())

	cfg, err := ctrl.GetConfig()
	if err != nil {
		return fmt.Errorf("unable to load kubernetes config: %w", err)
	}

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Logger:                  log.Log.WithName("label-controller"),
		LeaderElection:          true,
		LeaderElectionID:        leaderElectionID,
		LeaderElectionNamespace: leaderElectionNamespace,
		Metrics: server.Options{
			// Disable the controller-runtime's default metrics server to avoid port conflicts.
			// The main driver process manages its own metrics endpoint.
			BindAddress: "0",
		},
	})
	if err != nil {
		return fmt.Errorf("unable to start manager: %w", err)
	}

	reconciler := &PvReconciler{
		Client:             mgr.GetClient(),
		Scheme:             mgr.GetScheme(),
		Log:                mgr.GetLogger(),
		Cloud:              cloud,
		MetricsManager:     mm,
		volumeIDToInstance: lustre.VolumeIDToInstance,
	}

	if err := ctrl.NewControllerManagedBy(mgr).
		For(&corev1.PersistentVolume{}).
		Complete(reconciler); err != nil {
		return fmt.Errorf("unable to create controller: %w", err)
	}

	klog.Info("Starting GKE Lustre Labeling Controller")
	if err := mgr.Start(ctx); err != nil {
		return fmt.Errorf("problem running manager: %w", err)
	}

	return nil
}
