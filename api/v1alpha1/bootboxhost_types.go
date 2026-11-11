package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// HostState is the lifecycle state of a BootBoxHost.
// +kubebuilder:validation:Enum="";Available;Provisioning;Provisioned;Deprovisioning
type HostState string

const (
	// HostStateAvailable means the host is free and can be claimed.
	HostStateAvailable HostState = "Available"
	// HostStateProvisioning means a BootBoxMachine claimed the host and it is
	// being PXE-booted and installed.
	HostStateProvisioning HostState = "Provisioning"
	// HostStateProvisioned means the host joined its cluster as a Node.
	HostStateProvisioned HostState = "Provisioned"
	// HostStateDeprovisioning means the host is being powered off and released.
	HostStateDeprovisioning HostState = "Deprovisioning"
)

const (
	// HostFinalizer keeps a claimed host from being deleted.
	HostFinalizer = "bootboxhost.infrastructure.cluster.x-k8s.io"

	// ConditionBMCReachable reports whether the last Redfish call succeeded.
	ConditionBMCReachable = "BMCReachable"
)

// BMC describes how to reach the host's baseboard management controller.
type BMC struct {
	// Address is the Redfish base URL, for example https://10.0.0.10.
	// +kubebuilder:validation:Pattern=`^https?://`
	Address string `json:"address"`

	// CredentialsName names a Secret in the host's namespace with the keys
	// "username" and "password".
	// +kubebuilder:validation:MinLength=1
	CredentialsName string `json:"credentialsName"`

	// CertificateFingerprint pins the BMC's TLS certificate by the SHA-256
	// fingerprint of its leaf certificate (hex, colons optional). BMCs
	// usually present self-signed certificates; pinning avoids disabling
	// verification altogether.
	// +optional
	CertificateFingerprint string `json:"certificateFingerprint,omitempty"`

	// InsecureSkipVerify disables TLS verification. Ignored when
	// CertificateFingerprint is set.
	// +optional
	InsecureSkipVerify bool `json:"insecureSkipVerify,omitempty"`

	// SystemID selects the Redfish ComputerSystem, for example
	// System.Embedded.1. Defaults to the first system.
	// +optional
	SystemID string `json:"systemID,omitempty"`
}

// ConfigPatch is a JSON merge patch (RFC 7386) applied to the documents of
// the Talos machine config served to this host.
type ConfigPatch struct {
	// Kind selects the documents by kind, for example BondConfig.
	// "v1alpha1" selects the legacy v1alpha1 document.
	// +kubebuilder:validation:MinLength=1
	Kind string `json:"kind"`

	// Name restricts the match to the document with this name.
	// +optional
	Name string `json:"name,omitempty"`

	// Patch is the merge patch.
	// +kubebuilder:pruning:PreserveUnknownFields
	Patch apiextensionsv1.JSON `json:"patch"`
}

// BootBoxHostSpec defines a bare-metal server known to BootBox.
type BootBoxHostSpec struct {
	// HardwareName names the Tinkerbell Hardware object (same namespace)
	// that holds the server's DHCP reservation. The provider points its
	// iPXE URL at the provisioning endpoint while the host provisions.
	// +kubebuilder:validation:MinLength=1
	HardwareName string `json:"hardwareName"`

	// BMC is the Redfish endpoint of the server.
	BMC BMC `json:"bmc"`

	// ConsumerNamespaces lists the namespaces whose BootBoxMachines may claim
	// this host. Hosts with an empty list cannot be claimed.
	// +optional
	ConsumerNamespaces []string `json:"consumerNamespaces,omitempty"`

	// Maintenance keeps the host from being claimed. A claimed host is not
	// affected.
	// +optional
	Maintenance bool `json:"maintenance,omitempty"`

	// Hostname of the node. Defaults to the hostname of the Hardware DHCP
	// reservation, then to the object name.
	// +optional
	Hostname string `json:"hostname,omitempty"`

	// ConfigPatches are host-specific changes to the Talos machine config,
	// for example static addresses.
	// +optional
	ConfigPatches []ConfigPatch `json:"configPatches,omitempty"`

	// ExtraKernelArgs are appended to the kernel command line of the
	// provisioning boot.
	// +optional
	ExtraKernelArgs []string `json:"extraKernelArgs,omitempty"`
}

// HardwareInfo is what the BMC reports about the server.
type HardwareInfo struct {
	Manufacturer string `json:"manufacturer,omitempty"`
	Model        string `json:"model,omitempty"`
	SerialNumber string `json:"serialNumber,omitempty"`
	BIOSVersion  string `json:"biosVersion,omitempty"`
	PowerState   string `json:"powerState,omitempty"`
}

// ProvisioningStatus tracks the provisioning boot.
type ProvisioningStatus struct {
	// BootRequestedAt is when the PXE boot was requested from the BMC.
	// +optional
	BootRequestedAt *metav1.Time `json:"bootRequestedAt,omitempty"`
	// BootAttempts counts PXE boot requests of the current provisioning.
	// +optional
	BootAttempts int32 `json:"bootAttempts,omitempty"`
	// ConfigServedAt is when the host fetched its machine config.
	// +optional
	ConfigServedAt *metav1.Time `json:"configServedAt,omitempty"`
}

// BootBoxHostStatus is the observed state of a BootBoxHost.
type BootBoxHostStatus struct {
	// +optional
	State HostState `json:"state,omitempty"`

	// ConsumerRef is the BootBoxMachine that claimed the host.
	// +optional
	ConsumerRef *corev1.ObjectReference `json:"consumerRef,omitempty"`

	// +optional
	Hardware HardwareInfo `json:"hardware,omitempty"`

	// +optional
	Provisioning ProvisioningStatus `json:"provisioning,omitempty"`

	// LastInspected is the time of the last successful BMC inspection.
	// +optional
	LastInspected *metav1.Time `json:"lastInspected,omitempty"`

	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// BootBoxHost is a bare-metal server that BootBoxMachines can claim.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=bbh,categories=cluster-api
// +kubebuilder:printcolumn:name="State",type=string,JSONPath=`.status.state`
// +kubebuilder:printcolumn:name="Consumer",type=string,JSONPath=`.status.consumerRef.name`
// +kubebuilder:printcolumn:name="Power",type=string,JSONPath=`.status.hardware.powerState`
// +kubebuilder:printcolumn:name="Model",type=string,JSONPath=`.status.hardware.model`
// +kubebuilder:printcolumn:name="Maintenance",type=boolean,JSONPath=`.spec.maintenance`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type BootBoxHost struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   BootBoxHostSpec   `json:"spec,omitempty"`
	Status BootBoxHostStatus `json:"status,omitempty"`
}

// BootBoxHostList is a list of BootBoxHosts.
//
// +kubebuilder:object:root=true
type BootBoxHostList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []BootBoxHost `json:"items"`
}

func init() {
	SchemeBuilder.Register(&BootBoxHost{}, &BootBoxHostList{})
}
