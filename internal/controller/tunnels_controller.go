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
	"strconv"
	"strings"
	"time"

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

func removeElementFromList(slice []string, name string) []string {
	for i, v := range slice {
		if v == name {
			return append(slice[:i], slice[i+1:]...)
		}
	}
	return slice
}

func (r *TunnelsReconciler) getNodesName(ctx context.Context, tunnels *cftunnelargocontrollerv1alpha1.Tunnels) (string, error) {
	nodesName := &corev1.NodeList{}
	logger := log.FromContext(ctx)
	logger.Info("Getting nodes name")

	if err := r.List(ctx, nodesName); err != nil {
		return "", err
	}

	podList := &corev1.PodList{}
	if err := r.List(ctx, podList, client.InNamespace(tunnels.Namespace), client.MatchingLabels{"app": tunnels.Name}); err != nil {
		return "", err
	}

	nodes := []string{}

	for _, node := range nodesName.Items {
		if _, exists := node.Labels["node-role.kubernetes.io/control-plane"]; exists {
			continue
		} else {
			nodes = append(nodes, node.Name)
		}
	}

	for _, node := range nodesName.Items {
		for _, pod := range podList.Items {
			if pod.Spec.NodeName == node.Name {
				nodes = removeElementFromList(nodes, node.Name)
			}
		}
	}
	if len(nodes) == 0 {
		return "", nil
	}

	logger.Info(fmt.Sprintf("Assigned node %s", nodes[0]))

	return nodes[0], nil
}

func (r *TunnelsReconciler) checkConfigMap(ctx context.Context, tunnels *cftunnelargocontrollerv1alpha1.Tunnels) error {
	logger := log.FromContext(ctx)
	logger.Info("Checking ConfigMap")

	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      tunnels.Name + "-" + tunnels.Spec.TunnelName + "-config",
			Namespace: tunnels.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(tunnels, tunnels.GroupVersionKind()),
			},
			Annotations: map[string]string{
				"lastResourceVersion": tunnels.ResourceVersion,
			},
		},
		Data: map[string]string{
			"config.yaml": fmt.Sprintf(`tunnel: %s
credentials-file: /etc/cloudflared/creds/credentials.json
warp-routing:
  enabled: %t
originRequest: 
  connectTimeout: %s
  keepAliveTimeout: %s
  keepAliveConnections: %d
no-autoupdate: true
ingress:
%s
  - service: http_status:404`,
				tunnels.Spec.TunnelName,
				tunnels.Spec.EnableWrapping,
				tunnels.Spec.TunnelConnectTimeout,
				tunnels.Spec.TunnelKeepAliveTimeout,
				tunnels.Spec.TunnelKeepAliveConnections,
				formatIngressRules(tunnels.Spec.Ingress)),
		},
	}

	existingConfigMap := &corev1.ConfigMap{}
	if err := r.Get(ctx, types.NamespacedName{
		Name:      configMap.Name,
		Namespace: configMap.Namespace,
	}, existingConfigMap); err != nil {
		if !errors.IsNotFound(err) {
			logger.Error(err, "failed to get ConfigMap")
			return err
		} else {
			logger.Info("ConfigMap not found, creating")
			if err := r.Create(ctx, configMap); err != nil {
				logger.Error(err, "failed to create ConfigMap")
				return err
			}
		}
	}

	if existingConfigMap.Annotations["lastResourceVersion"] == tunnels.ResourceVersion {
		logger.Info("ConfigMap is up to date")
		return nil
	} else {
		logger.Info("ConfigMap is outdated, updating")
		if err := r.Update(ctx, configMap); err != nil {
			logger.Error(err, "failed to update ConfigMap")
			return err
		}
	}
	return nil
}

func (r *TunnelsReconciler) checkSecret(ctx context.Context, tunnels *cftunnelargocontrollerv1alpha1.Tunnels) error {
	logger := log.FromContext(ctx)
	logger.Info("Checking Secret")

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
				*metav1.NewControllerRef(tunnels, tunnels.GroupVersionKind()),
			},
			Annotations: map[string]string{
				"lastResourceVersion": tunnels.ResourceVersion,
			},
		},
		Type: corev1.SecretTypeOpaque,
		StringData: map[string]string{
			"credentials.json": credentialsJSON,
		},
	}
	existingSecret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{
		Name:      secret.Name,
		Namespace: secret.Namespace,
	}, existingSecret); err != nil {
		if !errors.IsNotFound(err) {
			logger.Error(err, "failed to get Secret")
			return err
		} else {
			logger.Info("Secret not found, creating")
			if err := r.Create(ctx, secret); err != nil {
				logger.Error(err, "failed to create Secret")
				return err
			}
		}
	}

	if existingSecret.Annotations["lastResourceVersion"] == tunnels.ResourceVersion {
		logger.Info("Secret is up to date")
		return nil
	} else {
		logger.Info("Secret is outdated, updating")
		if err := r.Update(ctx, secret); err != nil {
			logger.Error(err, "failed to update Secret")
			return err
		}
	}

	return nil
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

	nodeName, err := r.getNodesName(ctx, tunnels)
	if err != nil {
		logger.Error(err, "failed to get nodes name")
		return err
	}

	existingPod := &corev1.Pod{}
	err = r.Get(ctx, types.NamespacedName{
		Namespace: tunnels.Namespace,
		Name:      podName,
	}, existingPod)

	if err == nil {
		logger.Info("Pod already exists", "pod", podName)
		return nil
	} else if !errors.IsNotFound(err) {
		return err
	}

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

	labels := make(map[string]string)

	if tunnels.GetLabels() != nil {
		for k, v := range tunnels.GetLabels() {
			labels[k] = v
		}
	}
	labels["app"] = tunnels.Name
	labels["controller"] = "custom"

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        fmt.Sprintf("%s-tunnel-%d", tunnels.Name, index),
			Namespace:   tunnels.Namespace,
			Labels:      labels,
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
					Env: []corev1.EnvVar{
						{
							Name:  "TUNNEL_LOGLEVEL",
							Value: tunnels.Spec.TunnelLogLevel,
						},
						{
							Name:  "TUNNEL_METRICS",
							Value: "0.0.0.0:2000",
						},
						{
							Name:  "TUNNEL_REGION",
							Value: tunnels.Spec.TunnelRegion,
						},
						{
							Name:  "TUNNEL_RETRIES",
							Value: strconv.Itoa(int(tunnels.Spec.TunnelRetries)),
						},
						{
							Name:  "TUNNEL_PROTOCOL",
							Value: tunnels.Spec.TunnelProtocol,
						},
						{
							Name:  "TUNNEL_GRACE_PERIOD",
							Value: fmt.Sprintf("%ds", tunnels.Spec.TunnelGracePeriodSeconds),
						},
					},
					Ports: []corev1.ContainerPort{
						{
							ContainerPort: 2000,
							Protocol:      corev1.ProtocolTCP,
							Name:          "metrics",
						},
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
					ReadinessProbe: &corev1.Probe{
						ProbeHandler: corev1.ProbeHandler{
							HTTPGet: &corev1.HTTPGetAction{
								Path: "/ready",
								Port: intstr.FromInt(2000),
							},
						},
						InitialDelaySeconds: 10,
						PeriodSeconds:       15,
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
			TerminationGracePeriodSeconds: &tunnels.Spec.TunnelGracePeriodSeconds,
			NodeName:                      nodeName,
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

func (r *TunnelsReconciler) checkService(ctx context.Context, tunnels *cftunnelargocontrollerv1alpha1.Tunnels) error {
	logger := log.FromContext(ctx)
	logger.Info("Creating Service")

	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      tunnels.Name + "-service",
			Namespace: tunnels.Namespace,
			Labels: map[string]string{
				"app":        tunnels.Name,
				"controller": "custom",
			},
			Annotations: map[string]string{
				"lastResourceVersion": tunnels.ResourceVersion,
			},
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(tunnels, tunnels.GroupVersionKind()),
			},
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{
				"app":        tunnels.Name,
				"controller": "custom",
			},
			Type: corev1.ServiceTypeClusterIP,
			Ports: []corev1.ServicePort{
				{
					Port:       2000,
					TargetPort: intstr.FromInt(2000),
					Protocol:   corev1.ProtocolTCP,
					Name:       "metrics",
				},
			},
		},
	}

	existingService := &corev1.Service{}
	if err := r.Get(ctx, types.NamespacedName{
		Name:      tunnels.Name + "-service",
		Namespace: service.Namespace,
	}, existingService); err != nil {
		if !errors.IsNotFound(err) {
			logger.Error(err, "failed to get Service")
			return err
		} else {
			logger.Info("Service not found, creating")
			if err := r.Create(ctx, service); err != nil {
				logger.Error(err, "failed to create Service")
				return err
			}
		}
	}

	if existingService.Annotations["lastResourceVersion"] == tunnels.ResourceVersion {
		logger.Info("Service is up to date")
		return nil
	} else {
		logger.Info("Service is outdated, updating")
		if err := r.Update(ctx, service); err != nil {
			logger.Error(err, "failed to update Service")
			return err
		}
	}

	return nil
}

func (r *TunnelsReconciler) isPodReady(ctx context.Context, pod *corev1.Pod) bool {

	if err := r.Get(ctx, types.NamespacedName{
		Name:      pod.Name,
		Namespace: pod.Namespace,
	}, pod); err != nil {
		return false
	}

	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func (r *TunnelsReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var tunnels cftunnelargocontrollerv1alpha1.Tunnels
	if err := r.Get(ctx, req.NamespacedName, &tunnels); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if err := r.checkConfigMap(ctx, &tunnels); err != nil {
		logger.Error(err, "failed to check ConfigMap")
		return ctrl.Result{}, err
	}
	if err := r.checkSecret(ctx, &tunnels); err != nil {
		logger.Error(err, "failed to check Secret")
		return ctrl.Result{}, err
	}
	if err := r.checkService(ctx, &tunnels); err != nil {
		logger.Error(err, "failed to check Service")
		return ctrl.Result{}, err
	}

	podList := &corev1.PodList{}
	if err := r.List(ctx, podList, client.InNamespace(tunnels.Namespace),
		client.MatchingLabels{"app": tunnels.Name}); err != nil {
		return ctrl.Result{}, err
	}

	newPods := make(map[string]*corev1.Pod)
	oldPods := make(map[string]*corev1.Pod)
	for i := range podList.Items {
		pod := &podList.Items[i]
		if pod.Annotations["lastResourceVersion"] == tunnels.ResourceVersion {
			newPods[pod.Name] = pod
		} else {
			oldPods[pod.Name] = pod
		}
	}

	needsUpdate := len(oldPods) > 0 || len(newPods) != int(tunnels.Spec.Replicas)

	if needsUpdate {
		logger.Info("Update required",
			"new_pods", len(newPods),
			"old_pods", len(oldPods),
			"desired_replicas", tunnels.Spec.Replicas)

		var lastCreatedPod *corev1.Pod
		for _, pod := range newPods {
			if lastCreatedPod == nil || pod.CreationTimestamp.After(lastCreatedPod.CreationTimestamp.Time) {
				lastCreatedPod = pod
			}
		}

		if lastCreatedPod != nil && !r.isPodReady(ctx, lastCreatedPod) {
			logger.Info("Waiting for last created pod to be ready", "pod", lastCreatedPod.Name)
			return ctrl.Result{RequeueAfter: time.Second * 10}, nil
		}

		if len(newPods) < int(tunnels.Spec.Replicas) {
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

			logger.Info("Created new pod",
				"index", nextIndex,
				"current_new_pods", len(newPods),
				"desired_replicas", tunnels.Spec.Replicas)

			return ctrl.Result{RequeueAfter: time.Second * 10}, nil
		}

		allNewPodsReady := true
		for _, pod := range newPods {
			if !r.isPodReady(ctx, pod) {
				allNewPodsReady = false
				logger.Info("Waiting for pod to be ready", "pod", pod.Name)
				break
			}
		}

		if !allNewPodsReady {
			return ctrl.Result{RequeueAfter: time.Second * 10}, nil
		}

		if len(newPods) == int(tunnels.Spec.Replicas) && len(oldPods) > 0 {
			for _, pod := range oldPods {
				if err := r.Delete(ctx, pod); err != nil {
					if !errors.IsNotFound(err) {
						logger.Error(err, "failed to delete old pod", "pod", pod.Name)
						return ctrl.Result{}, err
					}
				}
				logger.Info("Deleted old pod", "pod", pod.Name)
			}
		}
	}

	return ctrl.Result{RequeueAfter: time.Second * 10}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *TunnelsReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&cftunnelargocontrollerv1alpha1.Tunnels{}).
		Complete(r)
}
