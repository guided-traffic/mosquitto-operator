package v1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// AntiAffinityModeOff renders no anti-affinity term at all. It is the
	// default: scheduling stays untouched unless the user opts in.
	AntiAffinityModeOff = "off"

	// AntiAffinityModeSoft renders preferredDuringSchedulingIgnoredDuringExecution:
	// the scheduler spreads the broker pods when it can and places them anyway when
	// it cannot.
	AntiAffinityModeSoft = "soft"

	// AntiAffinityModeHard renders requiredDuringSchedulingIgnoredDuringExecution:
	// a node already running a broker pod of this Mosquitto is not a candidate, so
	// pods beyond the number of nodes stay Pending.
	AntiAffinityModeHard = "hard"

	// AntiAffinityWeight is the weight of the preferred (soft) anti-affinity term.
	// It is the only term the operator emits, so the absolute value only matters
	// next to terms a user adds through other means; 100 keeps it dominant.
	AntiAffinityWeight int32 = 100

	// AntiAffinityTopologyKey is the node label the anti-affinity term spreads
	// over. One broker pod per node is the unit of failure this operator plans
	// for.
	AntiAffinityTopologyKey = "kubernetes.io/hostname"
)

const (
	// PhasePending means the workload does not serve anything yet: the
	// StatefulSet is missing or none of its pods are ready.
	PhasePending = "Pending"

	// PhaseProgressing means some but not all requested broker pods are ready.
	PhaseProgressing = "Progressing"

	// PhaseReady means every requested broker pod is ready.
	PhaseReady = "Ready"

	// PhaseFailed means the operator could not write one of the objects it
	// manages. The phase describes the operator, not the brokers: pods that were
	// already running keep running.
	PhaseFailed = "Failed"
)

// ConditionTypeReady is the data-plane verdict: every broker pod the spec asks
// for is ready. It is recomputed on every pass that reaches the status update,
// and it is present on every Mosquitto once one pass has completed.
const ConditionTypeReady = "Ready"

// MosquittoSpec defines the desired state of a Mosquitto broker.
//
// Authentication posture, because this is the surprising part: the generated
// configuration accepts anonymous clients. Mosquitto 2.x rejects every client on
// a listener that has no configured authentication, and this API models none, so
// the generated mosquitto.conf sets "allow_anonymous true" — without it the
// default resource would serve nobody. Anything that can route to the broker's
// ClusterIP Service can therefore publish and subscribe.
//
// Setting tls does not change that. It encrypts the listener and authenticates
// the broker to its clients; it authenticates no client to the broker, because
// the generated configuration sets no require_certificate.
//
// Until this API models authentication, config is where it goes: on the pinned
// Mosquitto 2.1 image that means the password-file and acl-file plugins, whose
// password_file and acl_file predecessors are deprecated there and removed in
// 3.0.
type MosquittoSpec struct {
	// Replicas is the number of broker pods in the StatefulSet.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=9
	// +kubebuilder:default=1
	Replicas int32 `json:"replicas,omitempty"`

	// Image is the Mosquitto container image. Empty means the operator default
	// (see internal/builder for the pinned default).
	// +optional
	Image string `json:"image,omitempty"`

	// Config is extra mosquitto.conf content appended to the generated base
	// configuration. It is written into the ConfigMap verbatim.
	//
	// The broker reads it after everything the operator generates, so a global
	// option repeated here wins over the generated one, and a listener line here
	// adds a listener the operator neither models nor exposes as a container or
	// Service port. Bridges and extra log destinations are equally possible.
	// Nothing here is validated: the broker sees it first at startup, so a
	// rejected file is a CrashLoopBackOff rather than a rejected resource.
	// +optional
	Config string `json:"config,omitempty"`

	// AntiAffinity controls how broker pods are spread over nodes.
	// "off" adds nothing, "soft" is a scheduler preference, "hard" guarantees
	// one pod per node (surplus pods stay Pending).
	// +kubebuilder:validation:Enum=off;soft;hard
	// +kubebuilder:default=off
	AntiAffinity string `json:"antiAffinity,omitempty"`

	// TLS mounts an existing TLS secret into the broker pods and switches the
	// listener to MQTTS on port 8883. The secret must carry tls.crt and tls.key
	// (the layout cert-manager writes). The operator does NOT create or renew
	// the secret; it only consumes one.
	//
	// It encrypts the connection and proves the broker's identity to its clients.
	// It does not authenticate clients: see the type documentation.
	// +optional
	TLS *MosquittoTLS `json:"tls,omitempty"`

	// Resources are the broker container resource requirements.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// Storage requests a PersistentVolumeClaim for the broker persistence
	// directory. Empty means emptyDir.
	// +optional
	Storage *MosquittoStorage `json:"storage,omitempty"`
}

// MosquittoTLS points at the TLS material the broker listener serves.
type MosquittoTLS struct {
	// SecretName is the name of the TLS secret in the same namespace. It carries
	// tls.crt and tls.key, which is what both ways of provisioning it produce and
	// neither of them is this operator:
	//
	//  1. Create it by hand: kubectl create secret tls <name> --cert=... --key=...
	//  2. Let cert-manager issue it, on clusters that already run cert-manager.
	//     The administrator owns the Certificate object; this operator has no
	//     cert-manager dependency and installs nothing.
	//
	// The operator does not watch this Secret. Renewing or replacing the
	// certificate changes the Secret, but running broker pods keep serving the
	// material they started with, so a rotation only takes effect once the pods
	// restart (for example kubectl rollout restart statefulset/<name>).
	// +kubebuilder:validation:MinLength=1
	SecretName string `json:"secretName"`
}

// MosquittoStorage describes the PersistentVolumeClaim template for the broker
// persistence directory.
type MosquittoStorage struct {
	// Size is the PVC size, e.g. "1Gi".
	// +kubebuilder:validation:MinLength=1
	Size string `json:"size"`
	// StorageClassName selects the storage class; empty uses the cluster default.
	// +optional
	StorageClassName *string `json:"storageClassName,omitempty"`
}

// MosquittoStatus is the observed state of a Mosquitto broker.
type MosquittoStatus struct {
	// Phase is a coarse rollout state: Pending, Progressing, Ready, Failed.
	Phase string `json:"phase,omitempty"`
	// ReadyReplicas mirrors the StatefulSet's ready replica count.
	ReadyReplicas int32 `json:"readyReplicas,omitempty"`
	// ObservedGeneration is the .metadata.generation the operator last acted on.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// Conditions follows the standard Kubernetes condition convention.
	// Type "Ready" is always present once a pass completed.
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// The plural is spelled out: the generator's own pluralisation of "Mosquitto"
// is "mosquittos", and the resource name is what RBAC rules, the Helm chart and
// kubectl all address.
// +kubebuilder:resource:path=mosquittoes,shortName=mq
// +kubebuilder:printcolumn:name="Replicas",type="integer",JSONPath=".spec.replicas",description="Desired number of broker pods"
// +kubebuilder:printcolumn:name="Ready",type="integer",JSONPath=".status.readyReplicas",description="Number of ready broker pods"
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase",description="Current phase"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// Mosquitto is the Schema for the mosquittoes API.
type Mosquitto struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MosquittoSpec   `json:"spec,omitempty"`
	Status MosquittoStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// MosquittoList contains a list of Mosquitto.
type MosquittoList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Mosquitto `json:"items"`
}

// AntiAffinityMode returns the configured anti-affinity mode, falling back to
// the default (off). An unknown value is treated as off: the weakest setting —
// no constraint at all — is the safe fallback if the enum validation is ever
// bypassed.
func (m *Mosquitto) AntiAffinityMode() string {
	switch m.Spec.AntiAffinity {
	case AntiAffinityModeSoft:
		return AntiAffinityModeSoft
	case AntiAffinityModeHard:
		return AntiAffinityModeHard
	default:
		return AntiAffinityModeOff
	}
}

// IsTLSEnabled reports whether the broker serves MQTTS from a mounted secret.
// An empty secretName is treated as disabled so a half-filled spec cannot
// produce a listener with no certificate to serve.
func (m *Mosquitto) IsTLSEnabled() bool {
	return m.Spec.TLS != nil && m.Spec.TLS.SecretName != ""
}

// IsStorageEnabled reports whether the persistence directory is backed by a
// PersistentVolumeClaim template instead of an emptyDir.
func (m *Mosquitto) IsStorageEnabled() bool {
	return m.Spec.Storage != nil && m.Spec.Storage.Size != ""
}
