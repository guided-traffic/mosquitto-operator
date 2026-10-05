package controller

import (
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestStripSecret: what stays in the cache is what the watches and the
// ownership check read; no credential, and not the last-applied annotation
// kubectl apply writes the whole Secret into.
func TestStripSecret(t *testing.T) {
	controller := true
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: "z2m-mqtt", Namespace: "home", ResourceVersion: "7",
			Labels:          map[string]string{"mko.gtrfc.com/consumable": "true"},
			Annotations:     map[string]string{"kubectl.kubernetes.io/last-applied-configuration": `{"data":{"password":"c2VjcmV0"}}`},
			OwnerReferences: []metav1.OwnerReference{{Name: "broker", Controller: &controller}},
			ManagedFields:   []metav1.ManagedFieldsEntry{{Manager: "kubectl"}},
		},
		Type:       corev1.SecretTypeBasicAuth,
		Data:       map[string][]byte{"password": []byte("secret")},
		StringData: map[string]string{"username": "z2m"},
	}

	out, err := StripSecret(secret)
	assert.NoError(t, err)
	stripped := out.(*corev1.Secret)
	assert.Nil(t, stripped.Data)
	assert.Nil(t, stripped.StringData)
	assert.Nil(t, stripped.Annotations)
	assert.Nil(t, stripped.ManagedFields)
	assert.Equal(t, "true", stripped.Labels["mko.gtrfc.com/consumable"], "the consent label is read from the cache")
	assert.Len(t, stripped.OwnerReferences, 1, "ensureOwned and the Owns watch read the owner")
	assert.Equal(t, corev1.SecretTypeBasicAuth, stripped.Type)
	assert.Equal(t, "7", stripped.ResourceVersion)

	other := &corev1.ConfigMap{Data: map[string]string{"a": "b"}}
	same, err := StripSecret(other)
	assert.NoError(t, err)
	assert.Same(t, other, same, "anything that is not a Secret passes untouched")
}
