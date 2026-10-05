package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// The access modes of an ACL entry, mirroring the acl-file plugin: read is
// subscribe and receive, write is publish (ADR 0013 D3).
const (
	AccessRead      = "read"
	AccessWrite     = "write"
	AccessReadWrite = "readwrite"
)

// The default keys of the credentials Secret: the keys of the built-in
// kubernetes.io/basic-auth Secret type, so one basic-auth Secret serves the
// broker and the client (ADR 0013 D2).
const (
	DefaultUsernameKey = "username"
	DefaultPasswordKey = "password"
)

// Reasons of a MosquittoUser's Ready condition (ADR 0013 D10).
const (
	// ReasonUserAccepted: the user is rendered into its broker's credentials.
	ReasonUserAccepted = "Accepted"
	// ReasonBrokerNotFound: no Mosquitto of brokerRef.name in this namespace.
	ReasonBrokerNotFound = "BrokerNotFound"
	// ReasonKeyNotFound: the credentials Secret has no such key.
	ReasonKeyNotFound = "KeyNotFound"
	// ReasonPasswordEmpty: the password key holds an empty value.
	ReasonPasswordEmpty = "PasswordEmpty"
	// ReasonUsernameInvalid: the username is outside the allowlist of ADR 0013 D5.
	ReasonUsernameInvalid = "UsernameInvalid"
	// ReasonUsernameReserved: the username starts with the reserved prefix mko-.
	ReasonUsernameReserved = "UsernameReserved"
	// ReasonUsernameConflict: an older user of the same broker holds the username.
	ReasonUsernameConflict = "UsernameConflict"
	// ReasonTopicRefused: an ACL topic starts with $ or is not a valid filter.
	ReasonTopicRefused = "TopicRefused"
)

// MosquittoUserSpec defines one client of one broker (ADR 0013).
//
// Everything a MosquittoUser names lives in its own namespace: no reference
// here carries a namespace field, so a reference across namespaces cannot be
// written (ADR 0013 D1).
type MosquittoUserSpec struct {
	// BrokerRef names the Mosquitto in this namespace whose clients this user is
	// one of.
	BrokerRef MosquittoBrokerRef `json:"brokerRef"`

	// CredentialsSecret names the Secret in this namespace that holds the
	// username and the password. The client application can read the same
	// Secret. The username is checked when it is rendered - it lives in the
	// Secret, where no schema can see it - and is written to status.username.
	CredentialsSecret MosquittoCredentialsSecret `json:"credentialsSecret"`

	// ACLs are what this user may read and write. A user without ACLs can log in
	// and reach no topic. No entry may name a topic starting with "$": $SYS and
	// $CONTROL are the broker's own (ADR 0013 D4).
	// +optional
	// +kubebuilder:validation:MaxItems=256
	// +listType=atomic
	ACLs []MosquittoACL `json:"acls,omitempty"`
}

// MosquittoBrokerRef names a Mosquitto in the user's own namespace.
type MosquittoBrokerRef struct {
	// Name of the Mosquitto.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`
}

// MosquittoCredentialsSecret names the Secret holding a user's credentials and
// the keys inside it.
type MosquittoCredentialsSecret struct {
	// Name of the Secret in the user's own namespace.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`

	// UsernameKey is the key holding the MQTT username. Changing it changes the
	// login; renaming the MosquittoUser does not.
	// +kubebuilder:default=username
	// +kubebuilder:validation:Pattern=`^[-._a-zA-Z0-9]+$`
	// +optional
	UsernameKey string `json:"usernameKey,omitempty"`

	// PasswordKey is the key holding the MQTT password.
	// +kubebuilder:default=password
	// +kubebuilder:validation:Pattern=`^[-._a-zA-Z0-9]+$`
	// +optional
	PasswordKey string `json:"passwordKey,omitempty"`
}

// MosquittoACL is one entry of a user's access list: a topic filter and what
// the user may do on it.
type MosquittoACL struct {
	// Topic is an MQTT topic filter; + and # are wildcards. It may not start with
	// "$" and may not contain a control character. Both are refused here and
	// again when the user is rendered.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=1024
	// +kubebuilder:validation:XValidation:rule="!self.startsWith('$')",message="a topic starting with $ is reserved for the broker and the operator"
	// +kubebuilder:validation:XValidation:rule="!self.matches('[\\\\x00-\\\\x1f\\\\x7f]')",message="a topic may not contain a control character"
	Topic string `json:"topic"`

	// Access is read (subscribe and receive), write (publish) or readwrite.
	// +kubebuilder:validation:Enum=read;write;readwrite
	Access string `json:"access"`
}

// MosquittoUserStatus is the observed state of a MosquittoUser.
type MosquittoUserStatus struct {
	// ObservedGeneration is the .metadata.generation the operator last acted on.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Username is the MQTT username read from the credentials Secret, once it
	// was read. A username is not a credential.
	// +optional
	Username string `json:"username,omitempty"`

	// Conditions follows the standard Kubernetes condition convention. Type
	// "Ready" is True when the user is rendered into its broker's credentials,
	// and False with a reason saying what is wrong otherwise.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=mosquittousers,shortName=mqu
// +kubebuilder:printcolumn:name="Broker",type="string",JSONPath=".spec.brokerRef.name",description="The Mosquitto this user belongs to"
// +kubebuilder:printcolumn:name="Username",type="string",JSONPath=".status.username",description="The MQTT username from the credentials Secret"
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type==\"Ready\")].status",description="Whether the user is rendered into its broker"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// MosquittoUser is one client of a Mosquitto broker, with its credentials in a
// Secret of its own and its permissions per topic.
type MosquittoUser struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MosquittoUserSpec   `json:"spec,omitempty"`
	Status MosquittoUserStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// MosquittoUserList contains a list of MosquittoUser.
type MosquittoUserList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MosquittoUser `json:"items"`
}

// UsernameKey returns the key of the username in the credentials Secret, the
// default when the field is empty.
func (u *MosquittoUser) UsernameKey() string {
	if u.Spec.CredentialsSecret.UsernameKey != "" {
		return u.Spec.CredentialsSecret.UsernameKey
	}
	return DefaultUsernameKey
}

// PasswordKey returns the key of the password in the credentials Secret, the
// default when the field is empty.
func (u *MosquittoUser) PasswordKey() string {
	if u.Spec.CredentialsSecret.PasswordKey != "" {
		return u.Spec.CredentialsSecret.PasswordKey
	}
	return DefaultPasswordKey
}
