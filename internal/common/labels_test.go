package common

import (
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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
		{"digest wins over any tag", "eclipse-mosquitto@sha256:abc123", "sha256:abc123"},
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
