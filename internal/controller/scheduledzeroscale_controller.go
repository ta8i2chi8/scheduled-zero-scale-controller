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
	"cmp"
	"context"
	"fmt"
	"slices"
	"strconv"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	appsv1ac "k8s.io/client-go/applyconfigurations/apps/v1"
	"k8s.io/client-go/tools/events"
	"k8s.io/utils/clock"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	srev1alpha1 "github.com/ta8i2chi8/scheduled-zero-scale-controller/api/v1alpha1"
	"github.com/ta8i2chi8/scheduled-zero-scale-controller/internal/schedule"
)

const (
	// replicasAnnotation records the replicas before scale-in. It exists only while scaled in.
	replicasAnnotation = "example.com/replicas-before-scale-in"
	fieldManager       = "scheduledzeroscale-controller"
	finalizerName      = "sre.example.com/restore-replicas"
)

// ScheduledZeroScaleReconciler reconciles a ScheduledZeroScale object
type ScheduledZeroScaleReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Clock    clock.PassiveClock
	Recorder events.EventRecorder
}

// +kubebuilder:rbac:groups=sre.example.com,resources=scheduledzeroscales,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=sre.example.com,resources=scheduledzeroscales/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=sre.example.com,resources=scheduledzeroscales/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// Reconcile scales in or restores the matched Deployments according to which of
// schedule.start / schedule.end fired last.
func (r *ScheduledZeroScaleReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var szs srev1alpha1.ScheduledZeroScale
	if err := r.Get(ctx, req.NamespacedName, &szs); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !szs.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, r.finalize(ctx, &szs)
	}

	if controllerutil.AddFinalizer(&szs, finalizerName) {
		if err := r.Update(ctx, &szs); err != nil {
			return ctrl.Result{}, err
		}
	}

	status := szs.Status.DeepCopy()
	status.ObservedGeneration = szs.Generation

	if szs.Spec.Suspend {
		setCondition(status, szs.Generation, srev1alpha1.ConditionSuspended, metav1.ConditionTrue, "Suspended", "spec.suspend is true")
		return ctrl.Result{}, r.updateStatus(ctx, &szs, status)
	}
	setCondition(status, szs.Generation, srev1alpha1.ConditionSuspended, metav1.ConditionFalse, "NotSuspended", "")

	sched, err := schedule.Parse(szs.Spec.Schedule.Start, szs.Spec.Schedule.End, szs.Spec.TimeZone)
	if err != nil {
		// Not retried: wait for the spec to be fixed.
		setCondition(status, szs.Generation, srev1alpha1.ConditionReady, metav1.ConditionFalse, "InvalidSchedule", err.Error())
		return ctrl.Result{}, r.updateStatus(ctx, &szs, status)
	}
	selector, err := metav1.LabelSelectorAsSelector(&szs.Spec.Selector)
	if err != nil {
		setCondition(status, szs.Generation, srev1alpha1.ConditionReady, metav1.ConditionFalse, "InvalidSelector", err.Error())
		return ctrl.Result{}, r.updateStatus(ctx, &szs, status)
	}

	now := r.Clock.Now()
	result := sched.Evaluate(now)
	state := srev1alpha1.ScaleStateNormal
	if result.State == schedule.ScaledIn {
		state = srev1alpha1.ScaleStateScaledIn
	}

	targets, err := r.reconcileDeployments(ctx, &szs, selector, state)
	if err != nil {
		return ctrl.Result{}, err
	}

	status.ScaleState = state
	status.LastTransitionTime = nil
	if result.LastTransition != nil {
		status.LastTransitionTime = new(metav1.NewTime(*result.LastTransition))
	}
	status.NextTransitionTime = nil
	if !result.NextTransition.IsZero() {
		status.NextTransitionTime = new(metav1.NewTime(result.NextTransition))
	}
	status.Targets = targets
	setCondition(status, szs.Generation, srev1alpha1.ConditionReady, metav1.ConditionTrue, "Reconciled", "")
	if err := r.updateStatus(ctx, &szs, status); err != nil {
		return ctrl.Result{}, err
	}

	if result.NextTransition.IsZero() {
		return ctrl.Result{}, nil
	}
	return ctrl.Result{RequeueAfter: result.NextTransition.Sub(now)}, nil
}

// reconcileDeployments applies state to each matched Deployment and returns the results sorted by name.
func (r *ScheduledZeroScaleReconciler) reconcileDeployments(
	ctx context.Context, szs *srev1alpha1.ScheduledZeroScale, selector labels.Selector, state srev1alpha1.ScaleState,
) ([]srev1alpha1.TargetStatus, error) {
	var deployments appsv1.DeploymentList
	if err := r.List(ctx, &deployments, client.InNamespace(szs.Namespace), client.MatchingLabelsSelector{Selector: selector}); err != nil {
		return nil, err
	}
	selectors, err := r.namespaceSelectors(ctx, szs.Namespace)
	if err != nil {
		return nil, err
	}

	targets := make([]srev1alpha1.TargetStatus, 0, len(deployments.Items))
	for i := range deployments.Items {
		dep := &deployments.Items[i]
		result := srev1alpha1.TargetResultConflict
		if countMatching(selectors, dep) == 1 {
			if result, err = r.reconcileDeployment(ctx, szs, dep, state); err != nil {
				return nil, err
			}
		}
		targets = append(targets, srev1alpha1.TargetStatus{Name: dep.Name, Result: result})
	}
	slices.SortFunc(targets, func(a, b srev1alpha1.TargetStatus) int { return cmp.Compare(a.Name, b.Name) })
	return targets, nil
}

func (r *ScheduledZeroScaleReconciler) reconcileDeployment(
	ctx context.Context, szs *srev1alpha1.ScheduledZeroScale, dep *appsv1.Deployment, state srev1alpha1.ScaleState,
) (srev1alpha1.TargetResult, error) {
	annotation, scaledIn := dep.Annotations[replicasAnnotation]
	original, valid := parseReplicas(annotation)
	if scaledIn && !valid {
		return srev1alpha1.TargetResultInvalidAnnotation, nil
	}
	replicas := ptr.Deref(dep.Spec.Replicas, 1)

	switch {
	case state == srev1alpha1.ScaleStateScaledIn && !scaledIn:
		if err := r.apply(ctx, dep, 0, new(strconv.Itoa(int(replicas)))); err != nil {
			return "", err
		}
		r.Recorder.Eventf(szs, dep, corev1.EventTypeNormal, "ScaledIn", "ScaleIn",
			"Scaled in Deployment %s from %d to 0 replicas", dep.Name, replicas)
		return srev1alpha1.TargetResultScaledIn, nil
	case state == srev1alpha1.ScaleStateScaledIn && replicas != 0:
		// Already scaled in but replicas was changed: keep the recorded replicas.
		if err := r.apply(ctx, dep, 0, &annotation); err != nil {
			return "", err
		}
		r.Recorder.Eventf(szs, dep, corev1.EventTypeNormal, "ScaledIn", "ScaleIn",
			"Scaled in Deployment %s from %d to 0 replicas again", dep.Name, replicas)
		return srev1alpha1.TargetResultScaledIn, nil
	case state == srev1alpha1.ScaleStateNormal && scaledIn:
		if err := r.restore(ctx, szs, dep, original); err != nil {
			return "", err
		}
		return srev1alpha1.TargetResultRestored, nil
	default:
		return srev1alpha1.TargetResultUnchanged, nil
	}
}

// finalize restores the matched Deployments that are scaled in, then removes the finalizer.
func (r *ScheduledZeroScaleReconciler) finalize(ctx context.Context, szs *srev1alpha1.ScheduledZeroScale) error {
	if !controllerutil.ContainsFinalizer(szs, finalizerName) {
		return nil
	}
	if selector, err := metav1.LabelSelectorAsSelector(&szs.Spec.Selector); err == nil {
		var deployments appsv1.DeploymentList
		if err := r.List(ctx, &deployments, client.InNamespace(szs.Namespace), client.MatchingLabelsSelector{Selector: selector}); err != nil {
			return err
		}
		for i := range deployments.Items {
			dep := &deployments.Items[i]
			annotation, scaledIn := dep.Annotations[replicasAnnotation]
			original, valid := parseReplicas(annotation)
			if !scaledIn || !valid {
				continue
			}
			if err := r.restore(ctx, szs, dep, original); err != nil {
				return err
			}
		}
	}
	controllerutil.RemoveFinalizer(szs, finalizerName)
	return r.Update(ctx, szs)
}

// restore sets replicas back to original and removes the annotation in a single apply.
func (r *ScheduledZeroScaleReconciler) restore(
	ctx context.Context, szs *srev1alpha1.ScheduledZeroScale, dep *appsv1.Deployment, original int32,
) error {
	if err := r.apply(ctx, dep, original, nil); err != nil {
		return err
	}
	r.Recorder.Eventf(szs, dep, corev1.EventTypeNormal, "Restored", "Restore",
		"Restored Deployment %s to %d replicas", dep.Name, original)
	return nil
}

// apply sets spec.replicas and the annotation with Server-Side Apply.
// The annotation is removed when it is nil, because this field manager stops owning it.
func (r *ScheduledZeroScaleReconciler) apply(ctx context.Context, dep *appsv1.Deployment, replicas int32, annotation *string) error {
	ac := appsv1ac.Deployment(dep.Name, dep.Namespace).
		WithSpec(appsv1ac.DeploymentSpec().WithReplicas(replicas))
	if annotation != nil {
		ac = ac.WithAnnotations(map[string]string{replicasAnnotation: *annotation})
	}
	// Force to take over spec.replicas from kubectl or ArgoCD.
	if err := r.Apply(ctx, ac, client.FieldOwner(fieldManager), client.ForceOwnership); err != nil {
		return fmt.Errorf("apply Deployment %s: %w", dep.Name, err)
	}
	return nil
}

// namespaceSelectors returns the selectors of all ScheduledZeroScales in the namespace.
func (r *ScheduledZeroScaleReconciler) namespaceSelectors(ctx context.Context, namespace string) ([]labels.Selector, error) {
	var list srev1alpha1.ScheduledZeroScaleList
	if err := r.List(ctx, &list, client.InNamespace(namespace)); err != nil {
		return nil, err
	}
	selectors := make([]labels.Selector, 0, len(list.Items))
	for i := range list.Items {
		if selector, err := metav1.LabelSelectorAsSelector(&list.Items[i].Spec.Selector); err == nil {
			selectors = append(selectors, selector)
		}
	}
	return selectors, nil
}

func countMatching(selectors []labels.Selector, dep *appsv1.Deployment) int {
	count := 0
	for _, selector := range selectors {
		if selector.Matches(labels.Set(dep.Labels)) {
			count++
		}
	}
	return count
}

// parseReplicas parses the annotation value as a non-negative integer.
func parseReplicas(value string) (int32, bool) {
	n, err := strconv.ParseInt(value, 10, 32)
	if err != nil || n < 0 {
		return 0, false
	}
	return int32(n), true
}

func setCondition(
	status *srev1alpha1.ScheduledZeroScaleStatus, generation int64, conditionType string, conditionStatus metav1.ConditionStatus, reason, message string,
) {
	meta.SetStatusCondition(&status.Conditions, metav1.Condition{
		Type:               conditionType,
		Status:             conditionStatus,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: generation,
	})
}

// updateStatus writes status only when it differs from the current one.
func (r *ScheduledZeroScaleReconciler) updateStatus(
	ctx context.Context, szs *srev1alpha1.ScheduledZeroScale, status *srev1alpha1.ScheduledZeroScaleStatus,
) error {
	if equality.Semantic.DeepEqual(szs.Status, *status) {
		return nil
	}
	updated := szs.DeepCopy()
	updated.Status = *status
	return r.Status().Update(ctx, updated)
}

// requestsForDeployment enqueues all ScheduledZeroScales in the Deployment's namespace.
func (r *ScheduledZeroScaleReconciler) requestsForDeployment(ctx context.Context, obj client.Object) []reconcile.Request {
	var list srev1alpha1.ScheduledZeroScaleList
	if err := r.List(ctx, &list, client.InNamespace(obj.GetNamespace())); err != nil {
		logf.FromContext(ctx).Error(err, "Failed to list ScheduledZeroScales", "namespace", obj.GetNamespace())
		return nil
	}
	requests := make([]reconcile.Request, 0, len(list.Items))
	for i := range list.Items {
		requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&list.Items[i])})
	}
	return requests
}

// SetupWithManager sets up the controller with the Manager.
func (r *ScheduledZeroScaleReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&srev1alpha1.ScheduledZeroScale{}).
		// Deployments created or changed during the scaled-in window are scaled in too.
		// Status-only updates are ignored.
		Watches(&appsv1.Deployment{},
			handler.EnqueueRequestsFromMapFunc(r.requestsForDeployment),
			builder.WithPredicates(predicate.Or(
				predicate.GenerationChangedPredicate{},
				predicate.LabelChangedPredicate{},
				predicate.AnnotationChangedPredicate{},
			))).
		Named("scheduledzeroscale").
		Complete(r)
}
