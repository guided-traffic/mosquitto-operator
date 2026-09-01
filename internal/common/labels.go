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

// maxLabelValueLength is the Kubernetes limit on a label value, in bytes.
const maxLabelValueLength = 63

// shortDigestLength is how much of a digest hex reaches the version label. Twelve
// characters is the short form docker itself prints, long enough to identify an
// image by eye and short enough to leave the value obviously abbreviated.
const shortDigestLength = 12

// ExtractVersionFromImage derives the app.kubernetes.io/version label value from a
// container image reference. It returns "latest" when the reference carries no tag.
//
// The result is always a VALID label value, which is the whole point of this
// function rather than a strings.Split at the call site. spec.image is free-form
// (the CRD validates nothing beyond MinLength), and two shapes of reference produce
// something the API server refuses:
//
//   - A digest reference. "eclipse-mosquitto@sha256:<64 hex>" yields
//     "sha256:<64 hex>", which is 71 bytes and contains a colon. Both break the
//     rules, so EVERY object the operator writes for that resource - ConfigMap, both
//     Services, StatefulSet - is rejected, and the resource sits in Failed forever.
//   - A long tag. A docker tag may be 128 characters, twice what a label value holds.
//
// So a digest is reduced to its hex prefix and anything else is sanitised: characters
// outside [A-Za-z0-9._-] become "-", the value is truncated to the limit, and it is
// trimmed to start and end alphanumerically. A value with nothing usable left becomes
// "unknown" rather than an empty label, so the label is always present and always says
// something.
func ExtractVersionFromImage(image string) string {
	// Handle images with digest (e.g., "image@sha256:...").
	//
	// The algorithm prefix is dropped: it is constant across every image in practice,
	// and the colon separating it from the hex is exactly what a label value may not
	// contain.
	if idx := strings.LastIndex(image, "@"); idx != -1 {
		digest := image[idx+1:]
		if algoEnd := strings.Index(digest, ":"); algoEnd != -1 {
			digest = digest[algoEnd+1:]
		}
		if len(digest) > shortDigestLength {
			digest = digest[:shortDigestLength]
		}
		return sanitizeLabelValue(digest)
	}

	// Handle images with tag (e.g., "image:tag").
	// Account for a registry port (e.g., "registry:5000/image").
	lastSlash := strings.LastIndex(image, "/")
	tagPart := image
	if lastSlash != -1 {
		tagPart = image[lastSlash:]
	}

	if idx := strings.LastIndex(tagPart, ":"); idx != -1 {
		return sanitizeLabelValue(tagPart[idx+1:])
	}

	return "latest"
}

// sanitizeLabelValue coerces a string into a valid Kubernetes label value.
//
// It is deliberately lossy and deliberately never fails: the alternative at the call
// site would be a reconcile that cannot write anything, which is a worse outcome than
// an abbreviated version label. What it must never do is return something the API
// server rejects, and TestSanitizeLabelValue_AlwaysProducesAValidLabel asserts that
// against apimachinery's own validator rather than against expected strings, so a
// future edit cannot pin an invalid value as intended behaviour.
func sanitizeLabelValue(value string) string {
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}

	// Truncate on bytes, not runes: the limit is a byte limit, and every rune that
	// survived the loop above is one byte.
	out := b.String()
	if len(out) > maxLabelValueLength {
		out = out[:maxLabelValueLength]
	}

	// A label value must start and end alphanumerically. Trimming can empty the
	// string - a tag of "---" leaves nothing - hence the fallback.
	out = strings.Trim(out, "-_.")
	if out == "" {
		return "unknown"
	}
	return out
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
