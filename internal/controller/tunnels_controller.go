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

// Перемістіть функцію formatIngressRules за межі Reconcile
func formatIngressRules(ingress []cftunnelargocontrollerv1alpha1.Ingress) string {
	var rules []string
	for _, ing := range ingress {
		rule := fmt.Sprintf("  - hostname: \"%s\"\n    service: %s", ing.Hostname, ing.Service)
		rules = append(rules, rule)
	}
	return strings.Join(rules, "\n")
}

// Додаємо нову структуру для відстеження стану
type TunnelPodState struct {
	DesiredReplicas int32
	CurrentReplicas int32
	AvailablePods   []string
	UnhealthyPods   []string
}

// Додаємо метод для перевірки здоров'я подів
func (r *TunnelsReconciler) checkPodsHealth(ctx context.Context, tunnels *cftunnelargocontrollerv1alpha1.Tunnels) (*TunnelPodState, error) {
	logger := log.FromContext(ctx)
	state := &TunnelPodState{
		DesiredReplicas: tunnels.Spec.Replicas,
	}

	// Отримуємо список подів з відповідною міткою
	podList := &corev1.PodList{}
	if err := r.List(ctx, podList, client.InNamespace(tunnels.Namespace), client.MatchingLabels{"app": tunnels.Name}); err != nil {
		return nil, err
	}

	state.CurrentReplicas = int32(len(podList.Items))

	// Перевіряємо стан кожного поду
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

// Додаємо нову функцію для пошуку наступного доступного індексу
func (r *TunnelsReconciler) findNextAvailableIndex(ctx context.Context, tunnels *cftunnelargocontrollerv1alpha1.Tunnels) (int, error) {
	podList := &corev1.PodList{}
	if err := r.List(ctx, podList, client.InNamespace(tunnels.Namespace), client.MatchingLabels{"app": tunnels.Name}); err != nil {
		return 0, err
	}

	// Створюємо мапу існуючих індексів
	usedIndices := make(map[int]bool)
	for _, pod := range podList.Items {
		// Витягуємо індекс з імені поду
		var index int
		_, err := fmt.Sscanf(pod.Name, tunnels.Name+"-tunnel-%d", &index)
		if err == nil {
			usedIndices[index] = true
		}
	}

	// Шукаємо перший вільний індекс
	for i := 0; i < 1000; i++ { // Обмежуємо пошук для безпеки
		if !usedIndices[i] {
			return i, nil
		}
	}

	return 0, fmt.Errorf("no available indices found")
}

// Оновлюємо функцію createPod
func (r *TunnelsReconciler) createPod(ctx context.Context, tunnels *cftunnelargocontrollerv1alpha1.Tunnels, index int) error {
	logger := log.FromContext(ctx)
	podName := fmt.Sprintf("%s-tunnel-%d", tunnels.Name, index)

	// Перевіряємо чи под вже існує
	existingPod := &corev1.Pod{}
	err := r.Get(ctx, types.NamespacedName{
		Namespace: tunnels.Namespace,
		Name:      podName,
	}, existingPod)

	if err == nil {
		// Под вже існує
		logger.Info("Pod already exists", "pod", podName)
		return nil
	} else if !errors.IsNotFound(err) {
		// Сталася помилка при перевірці
		return err
	}

	// Створюємо новий под
	logger.Info("Creating new pod", "name", podName)
	logger.Info("Creating pod", "name", fmt.Sprintf("%s-tunnel-%d", tunnels.Name, index))

	image := "cloudflare/cloudflared:2024.8.3"
	if tunnels.Spec.Image != "" {
		image = tunnels.Spec.Image
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-tunnel-%d", tunnels.Name, index),
			Namespace: tunnels.Namespace,
			Labels: map[string]string{
				"app":        tunnels.Name,
				"controller": "custom",
			},
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

// Оновлюємо Reconcile для прямого керування подами
func (r *TunnelsReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Отримуємо об'єкт Tunnels
	var tunnels cftunnelargocontrollerv1alpha1.Tunnels
	if err := r.Get(ctx, req.NamespacedName, &tunnels); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Створюємо ConfigMap
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

	// Створюємо або оновлюємо ConfigMap
	if err := r.Create(ctx, configMap); err != nil {
		if !errors.IsAlreadyExists(err) {
			logger.Error(err, "failed to create ConfigMap")
			return ctrl.Result{}, err
		}
		// Якщо ConfigMap вже існує, оновлюємо його
		if err := r.Update(ctx, configMap); err != nil {
			logger.Error(err, "failed to update ConfigMap")
			return ctrl.Result{}, err
		}
	}

	// Створюємо Secret
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

	// Створюємо або оновлюємо Secret
	if err := r.Create(ctx, secret); err != nil {
		if !errors.IsAlreadyExists(err) {
			logger.Error(err, "failed to create Secret")
			return ctrl.Result{}, err
		}
		// Якщо Secret вже існує, оновлюємо його
		if err := r.Update(ctx, secret); err != nil {
			logger.Error(err, "failed to update Secret")
			return ctrl.Result{}, err
		}
	}

	// Перевіряємо стан подів
	podState, err := r.checkPodsHealth(ctx, &tunnels)
	if err != nil {
		logger.Error(err, "failed to check pods health")
		return ctrl.Result{}, err
	}

	// Якщо кількість подів не відповідає бажаній
	if podState.CurrentReplicas < podState.DesiredReplicas {
		logger.Info("Need to create new pods",
			"current", podState.CurrentReplicas,
			"desired", podState.DesiredReplicas)

		// Створюємо необхідну кількість подів
		for i := int32(0); i < podState.DesiredReplicas-podState.CurrentReplicas; i++ {
			nextIndex, err := r.findNextAvailableIndex(ctx, &tunnels)
			if err != nil {
				logger.Error(err, "failed to find next available index")
				return ctrl.Result{}, err
			}

			if err := r.createPod(ctx, &tunnels, nextIndex); err != nil {
				logger.Error(err, "failed to create pod", "index", nextIndex)
				return ctrl.Result{}, err
			}
			logger.Info("Created new pod", "index", nextIndex)
		}

		return ctrl.Result{RequeueAfter: time.Second * 10}, nil
	}

	// Якщо є нездорові поди
	if len(podState.UnhealthyPods) > 0 {
		logger.Info("Unhealthy pods detected", "count", len(podState.UnhealthyPods))

		// Видаляємо нездорові поди
		for _, podName := range podState.UnhealthyPods {
			pod := &corev1.Pod{}
			if err := r.Get(ctx, types.NamespacedName{
				Name:      podName,
				Namespace: tunnels.Namespace,
			}, pod); err != nil {
				if !errors.IsNotFound(err) {
					logger.Error(err, "failed to get unhealthy pod")
					continue
				}
			}

			if err := r.Delete(ctx, pod); err != nil {
				if !errors.IsNotFound(err) {
					logger.Error(err, "failed to delete unhealthy pod")
				}
			}
		}

		// Запланувати повторну перевірку через 10 секунд
		return ctrl.Result{RequeueAfter: time.Second * 10}, nil
	}

	return ctrl.Result{RequeueAfter: time.Minute}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *TunnelsReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&cftunnelargocontrollerv1alpha1.Tunnels{}).
		Complete(r)
}
