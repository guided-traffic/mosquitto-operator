//go:build integration

package integration

// A MosquittoUser converges on its own when what it references arrives later
// (ADR 0012 D6): the watches on users and on Secrets carry the arrival to the
// broker's pass. Only a real API server with a real informer cache can show
// that; the unit tier calls Reconcile by hand.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	mkov1 "github.com/guided-traffic/mosquitto-operator/api/v1"
	"github.com/guided-traffic/mosquitto-operator/internal/auth"
)

// waitForUserReason waits until a MosquittoUser's Ready condition carries the
// reason.
func waitForUserReason(t *testing.T, namespace, name, reason string) *mkov1.MosquittoUser {
	t.Helper()
	u := &mkov1.MosquittoUser{}
	require.Eventually(t, func() bool {
		if err := k8sClient.Get(testCtx, types.NamespacedName{Namespace: namespace, Name: name}, u); err != nil {
			return false
		}
		cond := meta.FindStatusCondition(u.Status.Conditions, mkov1.ConditionTypeReady)
		return cond != nil && cond.Reason == reason
	}, eventuallyTimeout, eventuallyInterval, "MosquittoUser %s/%s never reported %s", namespace, name, reason)
	return u
}

func createCredentials(t *testing.T, namespace, name, username, password string) {
	t.Helper()
	require.NoError(t, k8sClient.Create(testCtx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Type:       corev1.SecretTypeBasicAuth,
		StringData: map[string]string{"username": username, "password": password},
	}))
}

// TestIntegration_Users_ConvergeWhenTheirReferencesArrive: a user applied
// before its broker and its Secret - the order Flux applies a directory in is
// not guaranteed - reports each missing piece and becomes Ready once both exist,
// with nobody reconciling by hand.
func TestIntegration_Users_ConvergeWhenTheirReferencesArrive(t *testing.T) {
	ns := newNamespace(t)
	user := newUser(ns, "z2m", "zigbee2mqtt/#", mkov1.AccessReadWrite)
	user.Spec.BrokerRef.Name = "broker"
	require.NoError(t, k8sClient.Create(testCtx, user))

	waitForUserReason(t, ns, "z2m", mkov1.ReasonBrokerNotFound)

	createMosquitto(t, ns, "broker", mkov1.MosquittoSpec{})
	waitForUserReason(t, ns, "z2m", mkov1.ReasonSecretNotFound)

	createCredentials(t, ns, "z2m-mqtt", "zigbee2mqtt", "z2m-pw")
	stored := waitForUserReason(t, ns, "z2m", mkov1.ReasonUserAccepted)
	assert.Equal(t, "zigbee2mqtt", stored.Status.Username)
	assert.Equal(t, stored.Generation, stored.Status.ObservedGeneration)

	rendered := &corev1.Secret{}
	require.NoError(t, apiReader.Get(testCtx, types.NamespacedName{Namespace: ns, Name: "broker-auth"}, rendered))
	assert.True(t, isControlledBy(rendered, "broker"))
	assert.True(t, auth.VerifyPassword(auth.FilePayload{}.Hashes(rendered.Data)["zigbee2mqtt"], "z2m-pw"))
	assert.Contains(t, string(rendered.Data[auth.ACLKey]), "topic readwrite zigbee2mqtt/#")

	m := &mkov1.Mosquitto{}
	require.Eventually(t, func() bool {
		err := k8sClient.Get(testCtx, types.NamespacedName{Namespace: ns, Name: "broker"}, m)
		return err == nil && m.Status.Users == 1
	}, eventuallyTimeout, eventuallyInterval, "the broker never counted its user")
}

// TestIntegration_Users_ARemovedUserLeavesTheCredentials: deleting a user
// re-renders its broker without it.
func TestIntegration_Users_ARemovedUserLeavesTheCredentials(t *testing.T) {
	ns := newNamespace(t)
	createMosquitto(t, ns, "broker", mkov1.MosquittoSpec{})
	createCredentials(t, ns, "a-mqtt", "alice", "a-pw")
	createCredentials(t, ns, "b-mqtt", "bob", "b-pw")
	for _, name := range []string{"a", "b"} {
		u := newUser(ns, name, "home/#", mkov1.AccessRead)
		u.Spec.BrokerRef.Name = "broker"
		require.NoError(t, k8sClient.Create(testCtx, u))
		waitForUserReason(t, ns, name, mkov1.ReasonUserAccepted)
	}

	require.NoError(t, k8sClient.Delete(testCtx, &mkov1.MosquittoUser{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "b"},
	}))

	require.Eventually(t, func() bool {
		rendered := &corev1.Secret{}
		if err := apiReader.Get(testCtx, types.NamespacedName{Namespace: ns, Name: "broker-auth"}, rendered); err != nil {
			return false
		}
		hashes := auth.FilePayload{}.Hashes(rendered.Data)
		_, bob := hashes["bob"]
		return !bob && hashes["alice"] != ""
	}, eventuallyTimeout, eventuallyInterval, "bob is still in the rendered credentials")
}
