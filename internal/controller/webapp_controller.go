/*
Copyright 2026.

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

	k8sappsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	appsv1 "github.com/sandesh-ojha/webapp-operator/api/v1"
)

type WebAppReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=apps.sandesh.dev,resources=webapps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps.sandesh.dev,resources=webapps/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=apps.sandesh.dev,resources=webapps/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete

func (r *WebAppReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logf.FromContext(ctx)

	// Fetch the webapp CR
	app := &appsv1.WebApp{}
	err := r.Get(ctx, req.NamespacedName, app)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("WebApp resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get WebApp")
		return ctrl.Result{}, err
	}

	// Deployment Reconciliation
	foundDep := &k8sappsv1.Deployment{}
	err = r.Get(ctx, types.NamespacedName{Name: app.Name, Namespace: app.Namespace}, foundDep)

	if err != nil && apierrors.IsNotFound(err) {
		// Create Deployment
		dep, err := r.deploymentForWebApp(app)
		if err != nil {
			logger.Error(err, "Failed to define a new Deployment resource")
			return ctrl.Result{}, err
		}
		logger.Info("Creating a new Deployment", "Namespace", dep.Namespace, "Name", dep.Name)
		if err := r.Create(ctx, dep); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil

	} else if err != nil {
		return ctrl.Result{}, err
	}

	// Deployment Drift Detection
	desiredReplicas := app.Spec.Replicas
	desiredImage := app.Spec.Image
	actualImage := foundDep.Spec.Template.Spec.Containers[0].Image

	if *foundDep.Spec.Replicas != desiredReplicas || actualImage != desiredImage {
		logger.Info("Drift detected! Updating Deployment", "Desired Replicas", desiredReplicas, "Actual Replicas", *foundDep.Spec.Replicas)
		foundDep.Spec.Replicas = &desiredReplicas
		foundDep.Spec.Template.Spec.Containers[0].Image = desiredImage

		if err = r.Update(ctx, foundDep); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// Service Reconciliation
	foundSvc := &corev1.Service{}
	err = r.Get(ctx, types.NamespacedName{Name: app.Name + "-service", Namespace: app.Namespace}, foundSvc)

	if err != nil && apierrors.IsNotFound(err) {
		// Create Service
		svc, err := r.serviceForWebApp(app)
		if err != nil {
			logger.Error(err, "Failed to define a new Service resource")
			return ctrl.Result{}, err
		}
		logger.Info("Creating a new Service", "Namespace", svc.Namespace, "Name", svc.Name)
		if err := r.Create(ctx, svc); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil

	} else if err != nil {
		return ctrl.Result{}, err
	}

	// Status Reconciliation

	// Read the actual healthy pod count from the native K8s Deployment
	available := foundDep.Status.AvailableReplicas

	// Update our WebApp's internal memory struct
	app.Status.AvailableReplicas = available

	// Determine the Phase based on the math
	if available == app.Spec.Replicas {
		app.Status.Phase = appsv1.PhaseReconciled
	} else {
		// If they don't match, K8s is still working on spinning them up or down
		app.Status.Phase = appsv1.PhaseReconciling
	}

	// Push the Status update back to the Kubernetes API
	if err := r.Status().Update(ctx, app); err != nil {
		logger.Error(err, "Failed to update WebApp status")
		return ctrl.Result{}, err
	}

	// SUCCESS
	return ctrl.Result{}, nil
}

// Custom Helper Functions
func (r *WebAppReconciler) deploymentForWebApp(app *appsv1.WebApp) (*k8sappsv1.Deployment, error) {
	ls := map[string]string{"app": app.Name}
	replicas := app.Spec.Replicas

	dep := &k8sappsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      app.Name,
			Namespace: app.Namespace,
		},
		Spec: k8sappsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: ls},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: ls},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Image: app.Spec.Image,
						Name:  "webapp-container",
						Ports: []corev1.ContainerPort{{
							ContainerPort: 80,
							Name:          "http",
						}},
					}},
				},
			},
		},
	}

	if err := ctrl.SetControllerReference(app, dep, r.Scheme); err != nil {
		return nil, err
	}
	return dep, nil
}

func (r *WebAppReconciler) serviceForWebApp(app *appsv1.WebApp) (*corev1.Service, error) {
	ls := map[string]string{"app": app.Name}

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      app.Name + "-service",
			Namespace: app.Namespace,
		},
		Spec: corev1.ServiceSpec{
			Selector: ls,
			Type:     corev1.ServiceTypeNodePort, // Opens a port on Minikube
			Ports: []corev1.ServicePort{{
				Protocol:   corev1.ProtocolTCP,
				Port:       80,
				TargetPort: intstr.FromInt(80),
			}},
		},
	}

	if err := ctrl.SetControllerReference(app, svc, r.Scheme); err != nil {
		return nil, err
	}
	return svc, nil
}

func (r *WebAppReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&appsv1.WebApp{}).
		Owns(&k8sappsv1.Deployment{}). // Tells the manager to watch the Deployments we own
		Owns(&corev1.Service{}).       // Tells the manager to watch the Services we own
		Complete(r)
}
