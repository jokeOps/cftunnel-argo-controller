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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

type Ingress struct {
	Hostname string `json:"hostname"`
	Service  string `json:"service"`
}

// TunnelsSpec defines the desired state of Tunnels
type TunnelsSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// Foo is an example field of Tunnels. Edit tunnels_types.go to remove/update

	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	TunnelName string `json:"tunnelName"`
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	TunnelID string `json:"tunnelID"`
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	TunnelSecret string `json:"tunnelSecret"`
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	AccountNumber string `json:"accountNumber"`
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Minimum=1
	Replicas int32 `json:"replicas"`

	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	Ingress []Ingress `json:"ingress"`

	EnableWrapping bool   `json:"enableWrapping,omitempty"`
	Image          string `json:"image,omitempty"`
}

// TunnelsStatus defines the observed state of Tunnels
type TunnelsStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:printcolumn:name="TunnelName",type="string",JSONPath=".spec.tunnelName"
//+kubebuilder:printcolumn:name="TunnelID",type="string",JSONPath=".spec.tunnelID"
//+kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// Tunnels is the Schema for the tunnels API
type Tunnels struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   TunnelsSpec   `json:"spec,omitempty"`
	Status TunnelsStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// TunnelsList contains a list of Tunnels
type TunnelsList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Tunnels `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Tunnels{}, &TunnelsList{})
}
