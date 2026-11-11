package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
)

const (
	// MachineFinalizer lets the provider release the host before the
	// BootBoxMachine goes away.
	MachineFinalizer = "bootboxmachine.infrastructure.cluster.x-k8s.io"

	// HostProvisionedCondition reports the provisioning progress of the
	// claimed host.
	HostProvisionedCondition clusterv1.ConditionType = "HostProvisioned"

	// WaitingForBootstrapDataReason: the bootstrap provider has not
	// produced the machine config yet.
	WaitingForBootstrapDataReason = "WaitingForBootstrapData"
	// NoHostAvailableReason: no BootBoxHost matches the selector.
	NoHostAvailableReason = "NoHostAvailable"
	// BootFailedReason: the BMC rejected the PXE boot request.
	BootFailedReason = "BootFailed"
	// WaitingForConfigFetchReason: the host was booted but has not fetched
	// its config yet.
	WaitingForConfigFetchReason = "WaitingForConfigFetch"
)

// BootBoxMachineSpec defines the desired state of a BootBoxMachine.
type BootBoxMachineSpec struct {
	// ProviderID is set by the provider to bootbox://<host namespace>/<host name>.
	// +optional
	ProviderID *string `json:"providerID,omitempty"`

	// HostSelector selects the BootBoxHosts this machine may claim.
	// +optional
	HostSelector metav1.LabelSelector `json:"hostSelector,omitempty"`
}

// HostReference points at the claimed BootBoxHost.
type HostReference struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

// BootBoxMachineStatus is the observed state of a BootBoxMachine.
type BootBoxMachineStatus struct {
	// Ready is true once the host booted and fetched its machine config.
	// +optional
	Ready bool `json:"ready"`

	// HostRef is the claimed host.
	// +optional
	HostRef *HostReference `json:"hostRef,omitempty"`

	// Addresses of the host, from its DHCP reservation.
	// +optional
	Addresses []clusterv1.MachineAddress `json:"addresses,omitempty"`

	// +optional
	Conditions clusterv1.Conditions `json:"conditions,omitempty"`
}

// BootBoxMachine is the Cluster API infrastructure machine for a
// bare-metal server booted through BootBox.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=bbm,categories=cluster-api
// +kubebuilder:printcolumn:name="Cluster",type=string,JSONPath=`.metadata.labels['cluster\.x-k8s\.io/cluster-name']`
// +kubebuilder:printcolumn:name="Host",type=string,JSONPath=`.status.hostRef.name`
// +kubebuilder:printcolumn:name="Ready",type=boolean,JSONPath=`.status.ready`
// +kubebuilder:printcolumn:name="ProviderID",type=string,JSONPath=`.spec.providerID`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type BootBoxMachine struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   BootBoxMachineSpec   `json:"spec,omitempty"`
	Status BootBoxMachineStatus `json:"status,omitempty"`
}

// GetConditions implements conditions.Getter.
func (m *BootBoxMachine) GetConditions() clusterv1.Conditions { return m.Status.Conditions }

// SetConditions implements conditions.Setter.
func (m *BootBoxMachine) SetConditions(c clusterv1.Conditions) { m.Status.Conditions = c }

// BootBoxMachineList is a list of BootBoxMachines.
//
// +kubebuilder:object:root=true
type BootBoxMachineList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []BootBoxMachine `json:"items"`
}

// BootBoxMachineTemplateResource describes the machines created from a template.
type BootBoxMachineTemplateResource struct {
	// +optional
	ObjectMeta clusterv1.ObjectMeta `json:"metadata,omitempty"`
	Spec       BootBoxMachineSpec   `json:"spec"`
}

// BootBoxMachineTemplateSpec defines a BootBoxMachineTemplate.
type BootBoxMachineTemplateSpec struct {
	Template BootBoxMachineTemplateResource `json:"template"`
}

// BootBoxMachineTemplate is the template MachineDeployments and
// MachineSets create BootBoxMachines from.
//
// +kubebuilder:object:root=true
// +kubebuilder:resource:shortName=bbmt,categories=cluster-api
type BootBoxMachineTemplate struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec BootBoxMachineTemplateSpec `json:"spec,omitempty"`
}

// BootBoxMachineTemplateList is a list of BootBoxMachineTemplates.
//
// +kubebuilder:object:root=true
type BootBoxMachineTemplateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []BootBoxMachineTemplate `json:"items"`
}

func init() {
	SchemeBuilder.Register(&BootBoxMachine{}, &BootBoxMachineList{},
		&BootBoxMachineTemplate{}, &BootBoxMachineTemplateList{})
}
