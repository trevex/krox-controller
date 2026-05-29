/*
Copyright 2026 krox-controller authors.

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

// +groupName=krox.io
package v1alpha1

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// KroxDeploymentSpec defines a KRO RGD render+apply unit.
type KroxDeploymentSpec struct {
	// Interval is how often the controller re-renders and applies.
	// +required
	Interval metav1.Duration `json:"interval"`

	// Suspend pauses reconciliation when true.
	// +optional
	Suspend bool `json:"suspend,omitempty"`

	// SourceRef points at a Flux source CR containing the RGD artifact.
	// +required
	SourceRef SourceReference `json:"sourceRef"`

	// Path is the location of the RGD YAML inside the source artifact.
	// +required
	// +kubebuilder:validation:MinLength=1
	Path string `json:"path"`

	// Values are the instance values for the RGD. Validated at render time
	// against the RGD's spec schema.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	Values *apiextensionsv1.JSON `json:"values,omitempty"`

	// Prune deletes owned objects no longer present in the latest render.
	// +optional
	Prune bool `json:"prune,omitempty"`

	// Force forces server-side-apply to take ownership on conflicts.
	// +optional
	Force bool `json:"force,omitempty"`
}

// SourceReference identifies a Flux source CR.
type SourceReference struct {
	// +kubebuilder:validation:Enum=GitRepository;OCIRepository
	Kind string `json:"kind"`
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// KroxDeploymentStatus reports the last reconcile outcome.
type KroxDeploymentStatus struct {
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +optional
	LastAppliedRevision string `json:"lastAppliedRevision,omitempty"`
	// +optional
	LastAttemptedRevision string `json:"lastAttemptedRevision,omitempty"`
	// +optional
	Inventory *ResourceInventory `json:"inventory,omitempty"`
}

// ResourceInventory lists objects last applied by a KroxDeployment.
type ResourceInventory struct {
	Entries []ResourceRef `json:"entries"`
}

// ResourceRef is a stable identifier for an applied object.
// ID = "<group>/<version>/<kind>/<namespace>/<name>"; cluster-scoped objects
// use an empty namespace component (e.g. "rbac.authorization.k8s.io/v1/ClusterRole//x");
// core group "" yields a leading slash (e.g. "/v1/ConfigMap/ns/x").
type ResourceRef struct {
	ID      string `json:"id"`
	Version string `json:"v"`
}

// Condition types.
const (
	ConditionReady       = "Ready"
	ConditionReconciling = "Reconciling"
	ConditionStalled     = "Stalled"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=krox
// +kubebuilder:printcolumn:name="Revision",type=string,JSONPath=`.status.lastAppliedRevision`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// KroxDeployment is a KRO RGD applied to the cluster.
type KroxDeployment struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              KroxDeploymentSpec   `json:"spec,omitempty"`
	Status            KroxDeploymentStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// KroxDeploymentList is a list of KroxDeployment.
type KroxDeploymentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []KroxDeployment `json:"items"`
}

func init() {
	SchemeBuilder.Register(&KroxDeployment{}, &KroxDeploymentList{})
}
