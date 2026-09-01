package common

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"

	mkov1 "github.com/guided-traffic/mosquitto-operator/api/v1"
)

func testMosquitto() *mkov1.Mosquitto {
	return &mkov1.Mosquitto{
		ObjectMeta: metav1.ObjectMeta{Name: "broker", Namespace: "messaging"},
	}
}

func TestExtractVersionFromImage(t *testing.T) {
	tests := []struct {
		name  string
		image string
		want  string
	}{
		{"tag", "eclipse-mosquitto:2.1.2-alpine", "2.1.2-alpine"},
		{"no tag", "eclipse-mosquitto", "latest"},
		{"registry with port and tag", "registry.example.com:5000/eclipse-mosquitto:2.1.2-alpine", "2.1.2-alpine"},
		{"registry with port, no tag", "registry.example.com:5000/eclipse-mosquitto", "latest"},
		// A digest is reduced to its hex prefix. The algorithm prefix and its colon
		// are dropped because a label value may not contain a colon at all.
		{"digest is reduced to its short hex", "eclipse-mosquitto@sha256:abc123", "abc123"},
		{"long digest is truncated to the short form", "eclipse-mosquitto@sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", "e3b0c44298fc"},
		{"empty image", "", "latest"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ExtractVersionFromImage(tt.image))
		})
	}
}

func TestBaseLabels(t *testing.T) {
	labels := BaseLabels(testMosquitto(), "eclipse-mosquitto:2.1.2-alpine")

	assert.Equal(t, map[string]string{
		LabelName:      AppName,
		LabelInstance:  "broker",
		LabelManagedBy: ManagedBy,
		LabelComponent: ComponentBroker,
		LabelVersion:   "2.1.2-alpine",
	}, labels)
}

// TestSelectorLabelsSurviveAnImageChange is the reason the version label is not
// part of the selector: a selector that moved with the image would stop matching
// the running pods during an upgrade, which is when the Service has to keep
// routing.
func TestSelectorLabelsSurviveAnImageChange(t *testing.T) {
	m := testMosquitto()

	assert.Equal(t, map[string]string{
		LabelName:      AppName,
		LabelInstance:  "broker",
		LabelManagedBy: ManagedBy,
	}, SelectorLabels(m))

	assert.NotContains(t, SelectorLabels(m), LabelVersion)

	for _, label := range SelectorLabels(m) {
		assert.NotEmpty(t, label, "an empty selector label matches nothing")
	}
}

func TestSelectorLabelsAreASubsetOfBaseLabels(t *testing.T) {
	m := testMosquitto()
	base := BaseLabels(m, "eclipse-mosquitto:2.1.2-alpine")

	for k, v := range SelectorLabels(m) {
		assert.Equal(t, v, base[k], "selector label %q must be stamped on the pods too", k)
	}
}

func TestDeterministicNames(t *testing.T) {
	m := testMosquitto()

	assert.Equal(t, "broker", StatefulSetName(m))
	assert.Equal(t, "broker-headless", HeadlessServiceName(m))
	assert.Equal(t, "broker", ClientServiceName(m))
}

func TestMapEntriesMissing(t *testing.T) {
	tests := []struct {
		name    string
		desired map[string]string
		current map[string]string
		want    bool
	}{
		{"identical", map[string]string{"a": "1"}, map[string]string{"a": "1"}, false},
		{"value differs", map[string]string{"a": "1"}, map[string]string{"a": "2"}, true},
		{"key missing", map[string]string{"a": "1"}, map[string]string{}, true},
		{"foreign extra keys are left alone", map[string]string{"a": "1"}, map[string]string{"a": "1", "b": "2"}, false},
		{"nothing desired", nil, map[string]string{"a": "1"}, false},
		{"desired against nil", map[string]string{"a": "1"}, nil, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, MapEntriesMissing(tt.desired, tt.current))
		})
	}
}

// TestExtractVersionFromImage_AlwaysProducesAValidLabel is the guard that the old
// behaviour lacked. It asserts against apimachinery's own validator rather than
// against expected strings, because the defect it exists to prevent was a test
// pinning "sha256:abc123" as the intended result - a value the API server rejects,
// so every object the operator writes for that resource would have been refused and
// the resource would have sat in Failed forever.
//
// Anything that reaches spec.image belongs here: the CRD validates nothing beyond
// MinLength, so the inputs below are all reachable by a namespace user.
func TestExtractVersionFromImage_AlwaysProducesAValidLabel(t *testing.T) {
	longTag := strings.Repeat("a", 128) // the maximum a docker tag may be
	images := []string{
		"",
		"eclipse-mosquitto",
		"eclipse-mosquitto:2.1.2-alpine",
		"eclipse-mosquitto@sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		"registry.example.com:5000/eclipse-mosquitto@sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		"eclipse-mosquitto:" + longTag,
		"registry.example.com:5000/eclipse-mosquitto:" + longTag,
		"eclipse-mosquitto:---",
		"eclipse-mosquitto:_leading",
		"eclipse-mosquitto:tag with spaces",
		"eclipse-mosquitto:naïve-ünïcode",
		"eclipse-mosquitto:v1.0.0+build.1",
	}

	for _, image := range images {
		t.Run(image, func(t *testing.T) {
			version := ExtractVersionFromImage(image)

			require.Empty(t, validation.IsValidLabelValue(version),
				"image %q produced label value %q, which the API server would reject", image, version)
			require.NotEmpty(t, version,
				"image %q produced an empty version label; every object carries this label", image)
		})
	}
}

// TestBaseLabels_AreAllValid closes the loop: it is BaseLabels, not the extractor,
// whose output is written to the API server, and every key and value in it has to
// pass validation for the write to be accepted.
func TestBaseLabels_AreAllValid(t *testing.T) {
	m := &mkov1.Mosquitto{ObjectMeta: metav1.ObjectMeta{Name: "broker", Namespace: "default"}}
	digestImage := "eclipse-mosquitto@sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

	for key, value := range BaseLabels(m, digestImage) {
		require.Empty(t, validation.IsQualifiedName(key), "label key %q is not a qualified name", key)
		require.Empty(t, validation.IsValidLabelValue(value), "label %s=%q is not a valid label value", key, value)
	}
}
