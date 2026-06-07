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

	// Fetch the WebApp instance
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

	// Fetch the standard Kubernetes Deployment
	found := &k8sappsv1.Deployment{}
	err = r.Get(ctx, types.NamespacedName{Name: app.Name, Namespace: app.Namespace}, found)

	// CREATE PATH: If the deployment doesn't exist, create it
	if err != nil && apierrors.IsNotFound(err) {
		dep, err := r.deploymentForWebApp(app)
		if err != nil {
			logger.Error(err, "Failed to define a new Deployment resource")
			return ctrl.Result{}, err
		}

		logger.Info("Creating a new Deployment", "Deployment.Namespace", dep.Namespace, "Deployment.Name", dep.Name)
		if err := r.Create(ctx, dep); err != nil {
			logger.Error(err, "Failed to create new Deployment", "Deployment.Namespace", dep.Namespace, "Deployment.Name", dep.Name)
			return ctrl.Result{}, err
		}
		// Deployment created successfully - return and requeue
		return ctrl.Result{Requeue: true}, nil

	} else if err != nil {
		logger.Error(err, "Failed to get Deployment")
		return ctrl.Result{}, err
	}

	// UPDATE PATH (DRIFT DETECTION): The deployment exists, let's check if it matches our desired state
	desiredReplicas := app.Spec.Replicas
	desiredImage := app.Spec.Image
	actualImage := found.Spec.Template.Spec.Containers[0].Image

	// Compare actual state vs desired state
	if *found.Spec.Replicas != desiredReplicas || actualImage != desiredImage {
		logger.Info("Drift detected! Updating Deployment",
			"Desired Replicas", desiredReplicas, "Actual Replicas", *found.Spec.Replicas,
			"Desired Image", desiredImage, "Actual Image", actualImage)

		// Overwrite the local memory with the desired values
		found.Spec.Replicas = &desiredReplicas
		found.Spec.Template.Spec.Containers[0].Image = desiredImage

		// Push the update to the Kubernetes API
		if err = r.Update(ctx, found); err != nil {
			logger.Error(err, "Failed to update Deployment", "Deployment.Namespace", found.Namespace, "Deployment.Name", found.Name)
			return ctrl.Result{}, err
		}

		// Update successful - return and requeue
		return ctrl.Result{Requeue: true}, nil
	}

	// SUCCESS: If we reach this point, the deployment exists and perfectly matches our desired state.
	return ctrl.Result{}, nil
}

// deploymentForWebApp returns a standard Kubernetes Deployment object based on our WebApp specs
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
			Selector: &metav1.LabelSelector{
				MatchLabels: ls,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: ls,
				},
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

func (r *WebAppReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&appsv1.WebApp{}).
		Named("webapp").
		Complete(r)
}
