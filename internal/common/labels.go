// Package common provides the shared labels, constants and name helpers used
// across the Mosquitto operator.
package common

import (
	"fmt"
	"strings"

	mkov1 "github.com/guided-traffic/mosquitto-operator/api/v1"
)

const (
	// LabelComponent is the standard Kubernetes recommended label for the component.
	LabelComponent = "app.kubernetes.io/component"
	// LabelInstance is the standard Kubernetes recommended label for the instance.
	LabelInstance = "app.kubernetes.io/instance"
	// LabelManagedBy is the standard Kubernetes recommended label for the manager.
	LabelManagedBy = "app.kubernetes.io/managed-by"
	// LabelName is the standard Kubernetes recommended label for the name.
	LabelName = "app.kubernetes.io/name"
	// LabelVersion is the standard Kubernetes recommended label for the version.
	LabelVersion = "app.kubernetes.io/version"

	// AppName is the value of the name label on every object the operator creates.
	AppName = "mosquitto"

	// ManagedBy is the value of the managed-by label on every object the operator
	// creates.
	ManagedBy = "mosquitto-operator"

	// ComponentBroker is the component value for the MQTT broker pods.
	ComponentBroker = "broker"
)

// ExtractVersionFromImage extracts the tag portion from a container image string.
// Returns "latest" if no tag is present.
func ExtractVersionFromImage(image string) string {
	// Handle images with digest (e.g., "image@sha256:...").
	if idx := strings.LastIndex(image, "@"); idx != -1 {
		return image[idx+1:]
	}

	// Handle images with tag (e.g., "image:tag").
	// Account for a registry port (e.g., "registry:5000/image").
	lastSlash := strings.LastIndex(image, "/")
	tagPart := image
	if lastSlash != -1 {
		tagPart = image[lastSlash:]
	}

	if idx := strings.LastIndex(tagPart, ":"); idx != -1 {
		return tagPart[idx+1:]
	}

	return "latest"
}

// BaseLabels returns the labels stamped on every object the operator creates for
// one Mosquitto. The image argument is the image the pods actually run, which is
// the CR's image or the operator default — never the empty spec field, because a
// label value must not be empty.
func BaseLabels(m *mkov1.Mosquitto, image string) map[string]string {
	return map[string]string{
		LabelName:      AppName,
		LabelInstance:  m.Name,
		LabelManagedBy: ManagedBy,
		LabelComponent: ComponentBroker,
		LabelVersion:   ExtractVersionFromImage(image),
	}
}

// SelectorLabels returns the minimal label set used in label selectors (Services,
// StatefulSet, anti-affinity terms).
//
// It deliberately excludes the version label: a selector carrying it would stop
// matching the running pods the moment the image changes, which is exactly when
// the Service has to keep routing.
func SelectorLabels(m *mkov1.Mosquitto) map[string]string {
	return map[string]string{
		LabelName:      AppName,
		LabelInstance:  m.Name,
		LabelManagedBy: ManagedBy,
	}
}

// StatefulSetName returns the name of the broker StatefulSet.
func StatefulSetName(m *mkov1.Mosquitto) string {
	return m.Name
}

// HeadlessServiceName returns the name of the headless Service that gives the
// StatefulSet its per-pod DNS records.
func HeadlessServiceName(m *mkov1.Mosquitto) string {
	return fmt.Sprintf("%s-headless", m.Name)
}

// ClientServiceName returns the name of the ClusterIP Service clients connect to.
func ClientServiceName(m *mkov1.Mosquitto) string {
	return m.Name
}

// MapEntriesMissing reports whether current is missing any of desired's entries
// or disagrees on a value. Extra keys in current are ignored: other controllers
// and users label objects too, and reverting their labels is not this operator's
// job.
func MapEntriesMissing(desired, current map[string]string) bool {
	for k, v := range desired {
		if current[k] != v {
			return true
		}
	}
	return false
}
