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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// ScheduleSpec defines when to scale in and when to restore.
type ScheduleSpec struct {
	// start is a 5-field cron expression at which matched Deployments are scaled in to 0 replicas.
	// +kubebuilder:validation:MinLength=1
	// +required
	Start string `json:"start"`

	// end is a 5-field cron expression at which matched Deployments are restored to their original replicas.
	// +kubebuilder:validation:MinLength=1
	// +required
	End string `json:"end"`
}

// ScheduledZeroScaleSpec defines the desired state of ScheduledZeroScale
type ScheduledZeroScaleSpec struct {
	// selector selects the Deployments in the same namespace.
	// +required
	Selector metav1.LabelSelector `json:"selector"`

	// timeZone is an IANA time zone name used to interpret the schedule.
	// +kubebuilder:validation:MinLength=1
	// +required
	TimeZone string `json:"timeZone"`

	// schedule defines when to scale in and when to restore.
	// +required
	Schedule ScheduleSpec `json:"schedule"`

	// suspend stops all scaling while true.
	// +optional
	Suspend bool `json:"suspend,omitempty"`
}

// ScaleState is the state the matched Deployments should be in.
// +kubebuilder:validation:Enum=Normal;ScaledIn
type ScaleState string

const (
	// ScaleStateNormal means the last fired cron is end.
	ScaleStateNormal ScaleState = "Normal"
	// ScaleStateScaledIn means the last fired cron is start.
	ScaleStateScaledIn ScaleState = "ScaledIn"
)

// TargetResult is the result of reconciling a matched Deployment.
// +kubebuilder:validation:Enum=ScaledIn;Restored;Unchanged;InvalidAnnotation;Conflict
type TargetResult string

const (
	TargetResultScaledIn          TargetResult = "ScaledIn"
	TargetResultRestored          TargetResult = "Restored"
	TargetResultUnchanged         TargetResult = "Unchanged"
	TargetResultInvalidAnnotation TargetResult = "InvalidAnnotation"
	TargetResultConflict          TargetResult = "Conflict"
)

// TargetStatus is the result for a matched Deployment.
type TargetStatus struct {
	// name is the name of the Deployment.
	// +required
	Name string `json:"name"`

	// result is the result of reconciling the Deployment.
	// +required
	Result TargetResult `json:"result"`
}

const (
	// ConditionReady indicates whether the schedule was evaluated and applied.
	ConditionReady = "Ready"
	// ConditionSuspended indicates whether spec.suspend is true.
	ConditionSuspended = "Suspended"
)

// ScheduledZeroScaleStatus defines the observed state of ScheduledZeroScale.
type ScheduledZeroScaleStatus struct {
	// observedGeneration is the generation last reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// scaleState is the state the matched Deployments should be in.
	// +optional
	ScaleState ScaleState `json:"scaleState,omitempty"`

	// lastTransitionTime is the time the last cron (start or end) fired.
	// +optional
	LastTransitionTime *metav1.Time `json:"lastTransitionTime,omitempty"`

	// nextTransitionTime is the time the next cron (start or end) fires.
	// +optional
	NextTransitionTime *metav1.Time `json:"nextTransitionTime,omitempty"`

	// targets are the matched Deployments and their results.
	// +listType=map
	// +listMapKey=name
	// +optional
	Targets []TargetStatus `json:"targets,omitempty"`

	// conditions represent the current state of the ScheduledZeroScale resource (Ready / Suspended).
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="State",type=string,JSONPath=`.status.scaleState`
// +kubebuilder:printcolumn:name="Next",type=string,JSONPath=`.status.nextTransitionTime`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// ScheduledZeroScale is the Schema for the scheduledzeroscales API
type ScheduledZeroScale struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of ScheduledZeroScale
	// +required
	Spec ScheduledZeroScaleSpec `json:"spec"`

	// status defines the observed state of ScheduledZeroScale
	// +optional
	Status ScheduledZeroScaleStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// ScheduledZeroScaleList contains a list of ScheduledZeroScale
type ScheduledZeroScaleList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []ScheduledZeroScale `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &ScheduledZeroScale{}, &ScheduledZeroScaleList{})
		return nil
	})
}
