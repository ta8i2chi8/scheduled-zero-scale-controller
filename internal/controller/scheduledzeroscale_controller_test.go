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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	clocktesting "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	srev1alpha1 "github.com/ta8i2chi8/scheduled-zero-scale-controller/api/v1alpha1"
)

var _ = Describe("ScheduledZeroScale Controller", func() {
	const (
		selectorLabel = "scheduled-zero-scale"
		api           = "api"
	)

	var (
		ctx        context.Context
		namespace  string
		fakeClock  *clocktesting.FakeClock
		recorder   *events.FakeRecorder
		reconciler *ScheduledZeroScaleReconciler
		tokyo      *time.Location
	)

	// jst returns a time in October 2026 in Asia/Tokyo. 2026-10-21 is a Wednesday.
	jst := func(day, hour, minute int) time.Time {
		return time.Date(2026, time.October, day, hour, minute, 0, 0, tokyo)
	}

	newScheduledZeroScale := func(name, label string) *srev1alpha1.ScheduledZeroScale {
		return &srev1alpha1.ScheduledZeroScale{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec: srev1alpha1.ScheduledZeroScaleSpec{
				Selector: metav1.LabelSelector{MatchLabels: map[string]string{selectorLabel: label}},
				TimeZone: "Asia/Tokyo",
				Schedule: srev1alpha1.ScheduleSpec{Start: "0 21 * * 1-5", End: "0 8 * * 1-5"},
			},
		}
	}

	newDeployment := func(name, label string, replicas int32, annotations map[string]string) *appsv1.Deployment {
		podLabels := map[string]string{"app": name}
		return &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:        name,
				Namespace:   namespace,
				Labels:      map[string]string{selectorLabel: label},
				Annotations: annotations,
			},
			Spec: appsv1.DeploymentSpec{
				Replicas: new(replicas),
				Selector: &metav1.LabelSelector{MatchLabels: podLabels},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: podLabels},
					Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "nginx"}}},
				},
			},
		}
	}

	reconcileAt := func(now time.Time, name string) reconcile.Result {
		GinkgoHelper()
		fakeClock.SetTime(now)
		result, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: namespace}})
		Expect(err).NotTo(HaveOccurred())
		return result
	}

	getDeployment := func(name string) *appsv1.Deployment {
		GinkgoHelper()
		dep := &appsv1.Deployment{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, dep)).To(Succeed())
		return dep
	}

	getScheduledZeroScale := func(name string) *srev1alpha1.ScheduledZeroScale {
		GinkgoHelper()
		szs := &srev1alpha1.ScheduledZeroScale{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, szs)).To(Succeed())
		return szs
	}

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		tokyo, err = time.LoadLocation("Asia/Tokyo")
		Expect(err).NotTo(HaveOccurred())

		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "szs-"}}
		Expect(k8sClient.Create(ctx, ns)).To(Succeed())
		namespace = ns.Name

		fakeClock = clocktesting.NewFakeClock(jst(21, 12, 0))
		recorder = events.NewFakeRecorder(100)
		reconciler = &ScheduledZeroScaleReconciler{
			Client:   k8sClient,
			Scheme:   k8sClient.Scheme(),
			Clock:    fakeClock,
			Recorder: recorder,
		}
	})

	It("scales in to 0 with the annotation and restores it", func() {
		Expect(k8sClient.Create(ctx, newDeployment(api, "office-hours", 3, nil))).To(Succeed())
		Expect(k8sClient.Create(ctx, newScheduledZeroScale("office-hours", "office-hours"))).To(Succeed())

		By("scaling in after start")
		result := reconcileAt(jst(21, 23, 0), "office-hours")
		Expect(result.RequeueAfter).To(Equal(9 * time.Hour))

		dep := getDeployment(api)
		Expect(*dep.Spec.Replicas).To(BeEquivalentTo(0))
		Expect(dep.Annotations).To(HaveKeyWithValue(replicasAnnotation, "3"))
		Expect(recorder.Events).To(Receive(ContainSubstring("ScaledIn")))

		szs := getScheduledZeroScale("office-hours")
		Expect(szs.Status.ScaleState).To(Equal(srev1alpha1.ScaleStateScaledIn))
		Expect(szs.Status.LastTransitionTime.Time).To(BeTemporally("==", jst(21, 21, 0)))
		Expect(szs.Status.NextTransitionTime.Time).To(BeTemporally("==", jst(22, 8, 0)))
		Expect(szs.Status.Targets).To(ConsistOf(srev1alpha1.TargetStatus{Name: api, Result: srev1alpha1.TargetResultScaledIn}))
		Expect(meta.IsStatusConditionTrue(szs.Status.Conditions, srev1alpha1.ConditionReady)).To(BeTrue())
		Expect(szs.Finalizers).To(ContainElement(finalizerName))

		By("not writing again while scaled in")
		resourceVersion := getDeployment(api).ResourceVersion
		reconcileAt(jst(22, 1, 0), "office-hours")
		Expect(getDeployment(api).ResourceVersion).To(Equal(resourceVersion))
		Expect(getScheduledZeroScale("office-hours").Status.Targets).To(ConsistOf(
			srev1alpha1.TargetStatus{Name: api, Result: srev1alpha1.TargetResultUnchanged}))

		By("restoring after end")
		reconcileAt(jst(22, 8, 0), "office-hours")
		dep = getDeployment(api)
		Expect(*dep.Spec.Replicas).To(BeEquivalentTo(3))
		Expect(dep.Annotations).NotTo(HaveKey(replicasAnnotation))
		Expect(recorder.Events).To(Receive(ContainSubstring("Restored")))
		szs = getScheduledZeroScale("office-hours")
		Expect(szs.Status.ScaleState).To(Equal(srev1alpha1.ScaleStateNormal))
		Expect(szs.Status.Targets).To(ConsistOf(srev1alpha1.TargetStatus{Name: api, Result: srev1alpha1.TargetResultRestored}))
	})

	It("scales in again without overwriting the annotation when replicas is increased manually", func() {
		Expect(k8sClient.Create(ctx, newDeployment(api, "office-hours", 3, nil))).To(Succeed())
		Expect(k8sClient.Create(ctx, newScheduledZeroScale("office-hours", "office-hours"))).To(Succeed())
		reconcileAt(jst(21, 23, 0), "office-hours")

		dep := getDeployment(api)
		dep.Spec.Replicas = new(int32(5))
		Expect(k8sClient.Update(ctx, dep)).To(Succeed())

		reconcileAt(jst(21, 23, 30), "office-hours")
		dep = getDeployment(api)
		Expect(*dep.Spec.Replicas).To(BeEquivalentTo(0))
		Expect(dep.Annotations).To(HaveKeyWithValue(replicasAnnotation, "3"))
	})

	It("does not touch Deployments without the annotation during normal hours", func() {
		Expect(k8sClient.Create(ctx, newDeployment(api, "office-hours", 2, nil))).To(Succeed())
		Expect(k8sClient.Create(ctx, newScheduledZeroScale("office-hours", "office-hours"))).To(Succeed())
		resourceVersion := getDeployment(api).ResourceVersion

		result := reconcileAt(jst(21, 12, 0), "office-hours")
		Expect(result.RequeueAfter).To(Equal(9 * time.Hour))
		Expect(getDeployment(api).ResourceVersion).To(Equal(resourceVersion))
		szs := getScheduledZeroScale("office-hours")
		Expect(szs.Status.ScaleState).To(Equal(srev1alpha1.ScaleStateNormal))
		Expect(szs.Status.Targets).To(ConsistOf(srev1alpha1.TargetStatus{Name: api, Result: srev1alpha1.TargetResultUnchanged}))
	})

	It("does not scale while suspended", func() {
		Expect(k8sClient.Create(ctx, newDeployment(api, "office-hours", 3, nil))).To(Succeed())
		szs := newScheduledZeroScale("office-hours", "office-hours")
		szs.Spec.Suspend = true
		Expect(k8sClient.Create(ctx, szs)).To(Succeed())

		result := reconcileAt(jst(21, 23, 0), "office-hours")
		Expect(result.RequeueAfter).To(BeZero())
		Expect(*getDeployment(api).Spec.Replicas).To(BeEquivalentTo(3))
		szs = getScheduledZeroScale("office-hours")
		Expect(meta.IsStatusConditionTrue(szs.Status.Conditions, srev1alpha1.ConditionSuspended)).To(BeTrue())
	})

	It("skips Deployments with an invalid annotation", func() {
		Expect(k8sClient.Create(ctx, newDeployment(api, "office-hours", 3, map[string]string{replicasAnnotation: "abc"}))).To(Succeed())
		Expect(k8sClient.Create(ctx, newScheduledZeroScale("office-hours", "office-hours"))).To(Succeed())

		reconcileAt(jst(21, 23, 0), "office-hours")
		Expect(*getDeployment(api).Spec.Replicas).To(BeEquivalentTo(3))
		Expect(getScheduledZeroScale("office-hours").Status.Targets).To(ConsistOf(
			srev1alpha1.TargetStatus{Name: api, Result: srev1alpha1.TargetResultInvalidAnnotation}))
	})

	It("skips Deployments matched by multiple ScheduledZeroScales", func() {
		dep := newDeployment(api, "office-hours", 3, nil)
		dep.Labels["team"] = "a"
		Expect(k8sClient.Create(ctx, dep)).To(Succeed())
		Expect(k8sClient.Create(ctx, newDeployment("web", "office-hours", 2, nil))).To(Succeed())
		Expect(k8sClient.Create(ctx, newScheduledZeroScale("office-hours", "office-hours"))).To(Succeed())
		other := newScheduledZeroScale("team-a", "")
		other.Spec.Selector = metav1.LabelSelector{MatchLabels: map[string]string{"team": "a"}}
		Expect(k8sClient.Create(ctx, other)).To(Succeed())

		reconcileAt(jst(21, 23, 0), "office-hours")
		Expect(*getDeployment(api).Spec.Replicas).To(BeEquivalentTo(3))
		Expect(*getDeployment("web").Spec.Replicas).To(BeEquivalentTo(0))
		Expect(getScheduledZeroScale("office-hours").Status.Targets).To(ConsistOf(
			srev1alpha1.TargetStatus{Name: api, Result: srev1alpha1.TargetResultConflict},
			srev1alpha1.TargetStatus{Name: "web", Result: srev1alpha1.TargetResultScaledIn},
		))
	})

	It("reports InvalidSchedule without requeueing", func() {
		Expect(k8sClient.Create(ctx, newDeployment(api, "office-hours", 3, nil))).To(Succeed())
		szs := newScheduledZeroScale("office-hours", "office-hours")
		szs.Spec.Schedule.Start = "not a cron"
		Expect(k8sClient.Create(ctx, szs)).To(Succeed())

		result := reconcileAt(jst(21, 23, 0), "office-hours")
		Expect(result.RequeueAfter).To(BeZero())
		Expect(*getDeployment(api).Spec.Replicas).To(BeEquivalentTo(3))
		ready := meta.FindStatusCondition(getScheduledZeroScale("office-hours").Status.Conditions, srev1alpha1.ConditionReady)
		Expect(ready).NotTo(BeNil())
		Expect(ready.Status).To(Equal(metav1.ConditionFalse))
		Expect(ready.Reason).To(Equal("InvalidSchedule"))
	})

	It("restores scaled-in Deployments when deleted", func() {
		Expect(k8sClient.Create(ctx, newDeployment(api, "office-hours", 3, nil))).To(Succeed())
		Expect(k8sClient.Create(ctx, newScheduledZeroScale("office-hours", "office-hours"))).To(Succeed())
		reconcileAt(jst(21, 23, 0), "office-hours")
		Expect(*getDeployment(api).Spec.Replicas).To(BeEquivalentTo(0))

		Expect(k8sClient.Delete(ctx, getScheduledZeroScale("office-hours"))).To(Succeed())
		reconcileAt(jst(21, 23, 30), "office-hours")

		dep := getDeployment(api)
		Expect(*dep.Spec.Replicas).To(BeEquivalentTo(3))
		Expect(dep.Annotations).NotTo(HaveKey(replicasAnnotation))
		err := k8sClient.Get(ctx, client.ObjectKey{Name: "office-hours", Namespace: namespace}, &srev1alpha1.ScheduledZeroScale{})
		Expect(errors.IsNotFound(err)).To(BeTrue())
	})
})
