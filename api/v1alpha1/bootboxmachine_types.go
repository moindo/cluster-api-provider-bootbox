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

// Conditions in the Cluster API v1beta2 format (status.v1beta2.conditions).
// Each one is a step of the provisioning flow; Ready summarises the steps up
// to the config fetch and is what the Machine mirrors as InfrastructureReady.
const (
	// ReadyV1Beta2Condition: the host booted and fetched its machine config
	// (status.ready).
	ReadyV1Beta2Condition = clusterv1.ReadyV1Beta2Condition
	// HostClaimedV1Beta2Condition: a BootBoxHost is claimed for this machine.
	HostClaimedV1Beta2Condition = "HostClaimed"
	// BMCReachableV1Beta2Condition mirrors BMCReachable of the claimed host.
	BMCReachableV1Beta2Condition = "BMCReachable"
	// HostBootedV1Beta2Condition: the host PXE-booted and fetched its iPXE
	// script.
	HostBootedV1Beta2Condition = "HostBooted"
	// MachineConfigServedV1Beta2Condition: the host fetched its machine config.
	MachineConfigServedV1Beta2Condition = "MachineConfigServed"
	// HostProvisionedV1Beta2Condition: the host joined as a Node and its PXE
	// window is closed.
	HostProvisionedV1Beta2Condition = "HostProvisioned"

	ClaimedV1Beta2Reason          = "Claimed"
	NotClaimedV1Beta2Reason       = "NotClaimed"
	BootNotRequestedV1Beta2Reason = "BootNotRequested"
	BootRequestedV1Beta2Reason    = "BootRequested"
	IPXEServedV1Beta2Reason       = "IPXEServed"
	ConfigServedV1Beta2Reason     = "ConfigServed"
	ProvisionedV1Beta2Reason      = "Provisioned"
	NoHostV1Beta2Reason           = "NoHost"
	ConditionUnknownV1Beta2Reason = "Unknown"
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

	// V1Beta2 holds the conditions in the Cluster API v1beta2 format.
	// +optional
	V1Beta2 *BootBoxMachineV1Beta2Status `json:"v1beta2,omitempty"`
}

// BootBoxMachineV1Beta2Status groups the v1beta2 fields of the status.
type BootBoxMachineV1Beta2Status struct {
	// Conditions report the steps of the provisioning flow: HostClaimed,
	// BMCReachable, HostBooted, MachineConfigServed, HostProvisioned and the
	// summary Ready.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +kubebuilder:validation:MaxItems=32
	Conditions []metav1.Condition `json:"conditions,omitempty"`
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
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.status.v1beta2.conditions[?(@.type=="HostProvisioned")].reason`
// +kubebuilder:printcolumn:name="ProviderID",type=string,JSONPath=`.spec.providerID`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:printcolumn:name="Message",type=string,priority=1,JSONPath=`.status.v1beta2.conditions[?(@.type=="HostProvisioned")].message`
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

// GetV1Beta2Conditions implements the v1beta2 conditions.Getter.
func (m *BootBoxMachine) GetV1Beta2Conditions() []metav1.Condition {
	if m.Status.V1Beta2 == nil {
		return nil
	}
	return m.Status.V1Beta2.Conditions
}

// SetV1Beta2Conditions implements the v1beta2 conditions.Setter.
func (m *BootBoxMachine) SetV1Beta2Conditions(c []metav1.Condition) {
	if m.Status.V1Beta2 == nil {
		m.Status.V1Beta2 = &BootBoxMachineV1Beta2Status{}
	}
	m.Status.V1Beta2.Conditions = c
}

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
