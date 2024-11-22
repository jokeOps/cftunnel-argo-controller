/*
Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	//appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	cftunnelargocontrollerv1alpha1 "github.com/jokeOps/cftunnel-argo-controller/api/v1alpha1"
)

// TunnelsReconciler reconciles a Tunnels object
type TunnelsReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=cftunnel-argo.controller.cftunnel-argo.controller,resources=tunnels,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=cftunnel-argo.controller.cftunnel-argo.controller,resources=tunnels/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=cftunnel-argo.controller.cftunnel-argo.controller,resources=tunnels/finalizers,verbs=update
//+kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch;create;update;patch;delete

func formatIngressRules(ingress []cftunnelargocontrollerv1alpha1.Ingress) string {
	var rules []string
	for _, ing := range ingress {
		rule := fmt.Sprintf("  - hostname: \"%s\"\n    service: %s", ing.Hostname, ing.Service)
		rules = append(rules, rule)
	}
	return strings.Join(rules, "\n")
}

type TunnelPodState struct {
	DesiredReplicas int32
	CurrentReplicas int32
	AvailablePods   []string
	UnhealthyPods   []string
}

func (r *TunnelsReconciler) checkPodsHealth(ctx context.Context, tunnels *cftunnelargocontrollerv1alpha1.Tunnels) (*TunnelPodState, error) {
	logger := log.FromContext(ctx)
	state := &TunnelPodState{
		DesiredReplicas: tunnels.Spec.Replicas,
	}

	podList := &corev1.PodList{}
	if err := r.List(ctx, podList, client.InNamespace(tunnels.Namespace), client.MatchingLabels{"app": tunnels.Name}); err != nil {
		return nil, err
	}

	state.CurrentReplicas = int32(len(podList.Items))

	for _, pod := range podList.Items {
		if pod.Status.Phase == corev1.PodRunning {
			state.AvailablePods = append(state.AvailablePods, pod.Name)
		} else {
			state.UnhealthyPods = append(state.UnhealthyPods, pod.Name)
			logger.Info("Unhealthy pod detected", "pod", pod.Name, "phase", pod.Status.Phase)
		}
	}

	return state, nil
}

func (r *TunnelsReconciler) findNextAvailableIndex(ctx context.Context, tunnels *cftunnelargocontrollerv1alpha1.Tunnels) (int, error) {
	podList := &corev1.PodList{}
	if err := r.List(ctx, podList, client.InNamespace(tunnels.Namespace), client.MatchingLabels{"app": tunnels.Name}); err != nil {
		return 0, err
	}

	usedIndices := make(map[int]bool)
	for _, pod := range podList.Items {
		var index int
		_, err := fmt.Sscanf(pod.Name, tunnels.Name+"-tunnel-%d", &index)
		if err == nil {
			usedIndices[index] = true
		}
	}

	for i := 0; i < 1000; i++ {
		if !usedIndices[i] {
			return i, nil
		}
	}

	return 0, fmt.Errorf("no available indices found")
}

func (r *TunnelsReconciler) createPod(ctx context.Context, tunnels *cftunnelargocontrollerv1alpha1.Tunnels, index int) error {
	logger := log.FromContext(ctx)
	podName := fmt.Sprintf("%s-tunnel-%d", tunnels.Name, index)

	existingPod := &corev1.Pod{}
	err := r.Get(ctx, types.NamespacedName{
		Namespace: tunnels.Namespace,
		Name:      podName,
	}, existingPod)

	if err == nil {
		logger.Info("Pod already exists", "pod", podName)
		return nil
	} else if !errors.IsNotFound(err) {
		return err
	}

	logger.Info("Creating new pod", "name", podName)
	logger.Info("Creating pod", "name", fmt.Sprintf("%s-tunnel-%d", tunnels.Name, index))

	image := "cloudflare/cloudflared:2024.8.3"
	if tunnels.Spec.Image != "" {
		image = tunnels.Spec.Image
	}

	annotations := make(map[string]string)

	if tunnels.GetAnnotations() != nil {
		for k, v := range tunnels.GetAnnotations() {
			annotations[k] = v
		}
	}
	annotations["lastResourceVersion"] = tunnels.ResourceVersion

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-tunnel-%d", tunnels.Name, index),
			Namespace: tunnels.Namespace,
			Labels: map[string]string{
				"app":        tunnels.Name,
				"controller": "custom",
			},
			Annotations: annotations,
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(tunnels, tunnels.GroupVersionKind()),
			},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "cloudflared",
					Image: image,
					Args: []string{
						"tunnel",
						"--config",
						"/etc/cloudflared/config/config.yaml",
						"run",
					},
					VolumeMounts: []corev1.VolumeMount{
						{
							Name:      "config",
							MountPath: "/etc/cloudflared/config",
							ReadOnly:  true,
						},
						{
							Name:      "creds",
							MountPath: "/etc/cloudflared/creds",
							ReadOnly:  true,
						},
					},
					LivenessProbe: &corev1.Probe{
						ProbeHandler: corev1.ProbeHandler{
							HTTPGet: &corev1.HTTPGetAction{
								Path: "/ready",
								Port: intstr.FromInt(2000),
							},
						},
						InitialDelaySeconds: 10,
						PeriodSeconds:       15,
					},
				},
			},
			Volumes: []corev1.Volume{
				{
					Name: "config",
					VolumeSource: corev1.VolumeSource{
						ConfigMap: &corev1.ConfigMapVolumeSource{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: tunnels.Name + "-" + tunnels.Spec.TunnelName + "-config",
							},
						},
					},
				},
				{
					Name: "creds",
					VolumeSource: corev1.VolumeSource{
						Secret: &corev1.SecretVolumeSource{
							SecretName: tunnels.Name + "-" + tunnels.Spec.TunnelName + "-credentials",
						},
					},
				},
			},
		},
	}

	return r.Create(ctx, pod)
}

func (r *TunnelsReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var tunnels cftunnelargocontrollerv1alpha1.Tunnels
	if err := r.Get(ctx, req.NamespacedName, &tunnels); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      tunnels.Name + "-" + tunnels.Spec.TunnelName + "-config",
			Namespace: tunnels.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(&tunnels, tunnels.GroupVersionKind()),
			},
		},
		Data: map[string]string{
			"config.yaml": fmt.Sprintf(`tunnel: %s
credentials-file: /etc/cloudflared/creds/credentials.json
warp-routing:
  enabled: %t
metrics: 0.0.0.0:2000
no-autoupdate: true
ingress:
%s
  - service: http_status:404`, tunnels.Spec.TunnelName, tunnels.Spec.EnableWrapping, formatIngressRules(tunnels.Spec.Ingress)),
		},
	}

	if err := r.Create(ctx, configMap); err != nil {
		if !errors.IsAlreadyExists(err) {
			logger.Error(err, "failed to create ConfigMap")
			return ctrl.Result{}, err
		}
		if err := r.Update(ctx, configMap); err != nil {
			logger.Error(err, "failed to update ConfigMap")
			return ctrl.Result{}, err
		}
	}

	credentialsJSON := fmt.Sprintf(`{
		"AccountTag":   "%s",
		"TunnelID":    "%s",
		"TunnelSecret": "%s"
	}`, tunnels.Spec.AccountNumber, tunnels.Spec.TunnelID, tunnels.Spec.TunnelSecret)

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      tunnels.Name + "-" + tunnels.Spec.TunnelName + "-credentials",
			Namespace: tunnels.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(&tunnels, tunnels.GroupVersionKind()),
			},
		},
		Type: corev1.SecretTypeOpaque,
		StringData: map[string]string{
			"credentials.json": credentialsJSON,
		},
	}

	if err := r.Create(ctx, secret); err != nil {
		if !errors.IsAlreadyExists(err) {
			logger.Error(err, "failed to create Secret")
			return ctrl.Result{}, err
		}
		if err := r.Update(ctx, secret); err != nil {
			logger.Error(err, "failed to update Secret")
			return ctrl.Result{}, err
		}
	}

	podState, err := r.checkPodsHealth(ctx, &tunnels)
	if err != nil {
		logger.Error(err, "failed to check pods health")
		return ctrl.Result{}, err
	}

	desiredReplicas := int(tunnels.Spec.Replicas)
	if len(podState.AvailablePods) < desiredReplicas {
		logger.Info("Not enough running pods, creating new ones",
			"current", len(podState.AvailablePods),
			"desired", desiredReplicas)

		for i := len(podState.AvailablePods); i < desiredReplicas; i++ {
			nextIndex, err := r.findNextAvailableIndex(ctx, &tunnels)
			if err != nil {
				logger.Error(err, "failed to find next available index")
				return ctrl.Result{}, err
			}

			if err := r.createPod(ctx, &tunnels, nextIndex); err != nil {
				if !errors.IsAlreadyExists(err) {
					logger.Error(err, "failed to create new pod", "index", nextIndex)
					return ctrl.Result{}, err
				}
			}
			logger.Info("Created new pod", "index", nextIndex)
		}
	}

	needsUpdate := false
	for _, pod := range podState.AvailablePods {
		if r.isPodOutdated(ctx, &tunnels, pod) {
			needsUpdate = true
			break
		}
	}

	if needsUpdate {
		logger.Info("Starting rolling update of pods")

		newPods := make(map[string]bool)

		for i := 0; i < len(podState.AvailablePods); i++ {
			nextIndex, err := r.findNextAvailableIndex(ctx, &tunnels)
			if err != nil {
				logger.Error(err, "failed to find next available index")
				return ctrl.Result{}, err
			}

			podName := fmt.Sprintf("%s-tunnel-%d", tunnels.Name, nextIndex)

			if err := r.Get(ctx, types.NamespacedName{
				Name:      podName,
				Namespace: tunnels.Namespace,
			}, &corev1.Pod{}); err == nil {
				continue
			}

			if err := r.createPod(ctx, &tunnels, nextIndex); err != nil {
				if !errors.IsAlreadyExists(err) {
					logger.Error(err, "failed to create new pod", "index", nextIndex)
					return ctrl.Result{}, err
				}
			}
			newPods[podName] = true
			logger.Info("Created new pod", "index", nextIndex)
		}

		if err := r.waitForNewPodsHealthy(ctx, &tunnels, desiredReplicas); err != nil {
			return ctrl.Result{}, err
		}

		for _, podName := range podState.AvailablePods {
			if !newPods[podName] {
				pod := &corev1.Pod{}
				if err := r.Get(ctx, types.NamespacedName{
					Name:      podName,
					Namespace: tunnels.Namespace,
				}, pod); err != nil {
					if !errors.IsNotFound(err) {
						return ctrl.Result{}, err
					}
					continue
				}

				if err := r.Delete(ctx, pod); err != nil {
					if !errors.IsNotFound(err) {
						return ctrl.Result{}, err
					}
				}
				logger.Info("Deleted old pod", "pod", podName)
			}
		}
	}

	if len(podState.AvailablePods) > desiredReplicas {
		logger.Info("Too many running pods, removing excess",
			"current", len(podState.AvailablePods),
			"desired", desiredReplicas)

		podsToRemove := len(podState.AvailablePods) - desiredReplicas
		for i := 0; i < podsToRemove; i++ {
			pod := &corev1.Pod{}
			if err := r.Get(ctx, types.NamespacedName{
				Name:      podState.AvailablePods[i],
				Namespace: tunnels.Namespace,
			}, pod); err != nil {
				if !errors.IsNotFound(err) {
					return ctrl.Result{}, err
				}
				continue
			}

			if err := r.Delete(ctx, pod); err != nil {
				if !errors.IsNotFound(err) {
					return ctrl.Result{}, err
				}
			}
			logger.Info("Deleted excess pod", "pod", podState.AvailablePods[i])
		}
	}

	return ctrl.Result{RequeueAfter: time.Second * 10}, nil
}

func (r *TunnelsReconciler) isPodOutdated(ctx context.Context, tunnels *cftunnelargocontrollerv1alpha1.Tunnels, podName string) bool {
	pod := &corev1.Pod{}
	if err := r.Get(ctx, types.NamespacedName{
		Name:      podName,
		Namespace: tunnels.Namespace,
	}, pod); err != nil {
		return false
	}

	// if !reflect.DeepEqual(pod.Annotations, tunnels.GetAnnotations()) {
	// 	return true
	// }

	oldResourceVersion := pod.Annotations["lastResourceVersion"]
	if oldResourceVersion != tunnels.ResourceVersion {
		return true
	}

	return false
}

func (r *TunnelsReconciler) waitForNewPodsHealthy(ctx context.Context, tunnels *cftunnelargocontrollerv1alpha1.Tunnels, expectedPods int) error {
	logger := log.FromContext(ctx)

	for i := 0; i < 60; i++ {
		podState, err := r.checkPodsHealth(ctx, tunnels)
		if err != nil {
			return err
		}

		if len(podState.AvailablePods) >= expectedPods && len(podState.UnhealthyPods) == 0 {
			logger.Info("All new pods are healthy", "count", len(podState.AvailablePods))
			return nil
		}

		logger.Info("Waiting for new pods to become healthy",
			"available", len(podState.AvailablePods),
			"expected", expectedPods,
			"unhealthy", len(podState.UnhealthyPods))

		time.Sleep(5 * time.Second)
	}

	return fmt.Errorf("timeout waiting for new pods to become healthy")
}

// SetupWithManager sets up the controller with the Manager.
func (r *TunnelsReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&cftunnelargocontrollerv1alpha1.Tunnels{}).
		Complete(r)
}
