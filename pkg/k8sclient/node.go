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

package k8sclient

import (
	"context"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
)

// GetNodeWithRetry returns node information via node name.
func GetNodeWithRetry(ctx context.Context, nodeName string) (*v1.Node, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, err
	}
	kubeClient, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}

	return getNodeWithRetry(ctx, kubeClient, nodeName)
}

func getNodeWithRetry(ctx context.Context, kubeClient *kubernetes.Clientset, nodeName string) (*v1.Node, error) {
	var nodeObj *v1.Node
	backoff := wait.Backoff{
		Duration: 1 * time.Second,
		Factor:   2.0,
		Steps:    5,
	}
	err := wait.ExponentialBackoffWithContext(ctx, backoff, func(_ context.Context) (bool, error) {
		node, err := kubeClient.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
		if err != nil {
			klog.Warningf("Error getting node %s: %v, retrying...", nodeName, err)

			return false, nil
		}
		nodeObj = node
		klog.V(4).Infof("Successfully retrieved node info %s", nodeName)

		return true, nil
	})
	if err != nil {
		klog.Errorf("Failed to get node %s after retries: %v", nodeName, err)
	}

	return nodeObj, err
}
