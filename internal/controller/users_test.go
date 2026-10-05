package controller

import (
	"context"

	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mkov1 "github.com/guided-traffic/mosquitto-operator/api/v1"
	"github.com/guided-traffic/mosquitto-operator/internal/auth"
)

func credentials(name, username, password string, labels map[string]string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace, Labels: labels},
		Type:       corev1.SecretTypeBasicAuth,
		Data:       map[string][]byte{"username": []byte(username), "password": []byte(password)},
	}
}

func newUser(name, secret string, age time.Duration, acls ...mkov1.MosquittoACL) *mkov1.MosquittoUser {
	return &mkov1.MosquittoUser{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: testNamespace, Generation: 1,
			CreationTimestamp: metav1.NewTime(time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC).Add(-age)),
		},
		Spec: mkov1.MosquittoUserSpec{
			BrokerRef:         mkov1.MosquittoBrokerRef{Name: testName},
			CredentialsSecret: mkov1.MosquittoCredentialsSecret{Name: secret},
			ACLs:              acls,
		},
	}
}

func authSecret(t *testing.T, c client.Client) *corev1.Secret {
	t.Helper()
	secret := &corev1.Secret{}
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: testName + "-auth", Namespace: testNamespace}, secret))
	return secret
}

func storedUser(t *testing.T, c client.Client, name string) *mkov1.MosquittoUser {
	t.Helper()
	u := &mkov1.MosquittoUser{}
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: name, Namespace: testNamespace}, u))
	return u
}

func userReady(t *testing.T, u *mkov1.MosquittoUser) *metav1.Condition {
	t.Helper()
	cond := meta.FindStatusCondition(u.Status.Conditions, mkov1.ConditionTypeReady)
	require.NotNil(t, cond, "MosquittoUser %s has no Ready condition", u.Name)
	return cond
}

func usersCondition(t *testing.T, m *mkov1.Mosquitto) *metav1.Condition {
	t.Helper()
	cond := meta.FindStatusCondition(m.Status.Conditions, mkov1.ConditionTypeUsers)
	require.NotNil(t, cond)
	return cond
}

// TestReconcile_RendersTheUsersIntoTheAuthSecret is the core of ADR 0014 D2:
// every accepted user of the broker in one owned Secret, hashes only, and each
// user told so in its own status.
func TestReconcile_RendersTheUsersIntoTheAuthSecret(t *testing.T) {
	r, c := newReconcilerFor(t, newCR(),
		credentials("ha-mqtt", "homeassistant", "ha-pw", nil),
		credentials("z2m-mqtt", "zigbee2mqtt", "z2m-pw", nil),
		newUser("homeassistant", "ha-mqtt", time.Hour, mkov1.MosquittoACL{Topic: "homeassistant/#", Access: mkov1.AccessReadWrite}),
		newUser("zigbee2mqtt", "z2m-mqtt", time.Minute, mkov1.MosquittoACL{Topic: "zigbee2mqtt/#", Access: mkov1.AccessReadWrite}),
	)
	stored := reconciled(t, r, c)

	secret := authSecret(t, c)
	assert.True(t, metav1.IsControlledBy(secret, stored), "collected with its Mosquitto (ADR 0009)")
	hashes := auth.FilePayload{}.Hashes(secret.Data)
	assert.True(t, auth.VerifyPassword(hashes["homeassistant"], "ha-pw"))
	assert.True(t, auth.VerifyPassword(hashes["zigbee2mqtt"], "z2m-pw"))
	assert.NotContains(t, string(secret.Data[auth.PasswdKey]), "ha-pw", "no plaintext in the rendered Secret")
	assert.Equal(t, "user homeassistant\ntopic readwrite homeassistant/#\n\nuser zigbee2mqtt\ntopic readwrite zigbee2mqtt/#\n",
		string(secret.Data[auth.ACLKey]))

	for name, username := range map[string]string{"homeassistant": "homeassistant", "zigbee2mqtt": "zigbee2mqtt"} {
		u := storedUser(t, c, name)
		assert.Equal(t, metav1.ConditionTrue, userReady(t, u).Status)
		assert.Equal(t, mkov1.ReasonUserAccepted, userReady(t, u).Reason)
		assert.Equal(t, username, u.Status.Username, "the username is not a credential and is shown")
		assert.Equal(t, int64(1), u.Status.ObservedGeneration)
	}

	assert.Equal(t, int32(2), stored.Status.Users)
	assert.Equal(t, metav1.ConditionTrue, usersCondition(t, stored).Status)
}

// TestReconcile_ABrokerWithoutUsersAcceptsNobodyAndSaysSo is ADR 0008 D13: the
// rendered files exist and are empty, and the Users condition says why nobody
// can log in. Ready is untouched: it is about the pods.
func TestReconcile_ABrokerWithoutUsersAcceptsNobodyAndSaysSo(t *testing.T) {
	r, c := newReconcilerFor(t, newCR())
	stored := reconciled(t, r, c)

	secret := authSecret(t, c)
	assert.Empty(t, secret.Data[auth.PasswdKey])
	assert.Contains(t, secret.Data, auth.PasswdKey, "the key exists, so the plugin loads and accepts nobody")
	assert.Equal(t, int32(0), stored.Status.Users)
	assert.Equal(t, metav1.ConditionFalse, usersCondition(t, stored).Status)
	assert.Equal(t, mkov1.ReasonNoUsers, usersCondition(t, stored).Reason)
	assert.NotEqual(t, mkov1.PhaseFailed, stored.Status.Phase)
}

// TestReconcile_EveryUserReason is ADR 0013 D10: a user reports what is wrong
// with it, and its failure never fails the broker or another user (ADR 0012 D6).
func TestReconcile_EveryUserReason(t *testing.T) {
	keyless := credentials("keyless", "u", "p", nil)
	delete(keyless.Data, "password")
	consenting := map[string]string{mkov1.ConsumableLabel: mkov1.ConsumableLabelValue}

	tests := []struct {
		name           string
		secretSecurity bool
		objects        []client.Object
		wantReason     string
	}{
		{"the Secret does not exist", false, []client.Object{newUser("u", "absent", 0)}, mkov1.ReasonSecretNotFound},
		{"the password key is missing", false, []client.Object{keyless, newUser("u", "keyless", 0)}, mkov1.ReasonKeyNotFound},
		{"the password is empty", false, []client.Object{credentials("s", "alice", "", nil), newUser("u", "s", 0)}, mkov1.ReasonPasswordEmpty},
		{"the username is not allowed", false, []client.Object{credentials("s", "alice\n", "pw", nil), newUser("u", "s", 0)}, mkov1.ReasonUsernameInvalid},
		{"the username is reserved", false, []client.Object{credentials("s", "mko-exporter", "pw", nil), newUser("u", "s", 0)}, mkov1.ReasonUsernameReserved},
		{"a topic under $, the CRD bypassed", false, []client.Object{credentials("s", "alice", "pw", nil),
			newUser("u", "s", 0, mkov1.MosquittoACL{Topic: "$SYS/#", Access: mkov1.AccessRead})}, mkov1.ReasonTopicRefused},
		{"the Secret does not consent", true, []client.Object{credentials("s", "alice", "pw", nil), newUser("u", "s", 0)}, mkov1.ReasonSecretNotConsumable},
		{"an older user holds the username", false, []client.Object{
			credentials("s", "alice", "pw", nil), credentials("older-s", "alice", "pw", consenting),
			newUser("u", "s", 0), newUser("older", "older-s", time.Hour),
		}, mkov1.ReasonUsernameConflict},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bystander := credentials("bystander-s", "bystander", "pw", map[string]string{mkov1.ConsumableLabel: mkov1.ConsumableLabelValue})
			objects := append([]client.Object{newCR(), bystander, newUser("bystander", "bystander-s", 2*time.Hour)}, tt.objects...)
			r, c := newReconcilerFor(t, objects...)
			r.SecretSecurity = tt.secretSecurity
			stored := reconciled(t, r, c)

			u := storedUser(t, c, "u")
			assert.Equal(t, metav1.ConditionFalse, userReady(t, u).Status)
			assert.Equal(t, tt.wantReason, userReady(t, u).Reason, userReady(t, u).Message)

			assert.NotEqual(t, mkov1.PhaseFailed, stored.Status.Phase, "a user's failure never fails the broker")
			assert.Equal(t, metav1.ConditionTrue, userReady(t, storedUser(t, c, "bystander")).Status,
				"nor another user")
		})
	}
}

// TestReconcile_AUserOfAMissingBroker: the user names a Mosquitto that does
// not exist; the pass for that name reports it on the user.
func TestReconcile_AUserOfAMissingBroker(t *testing.T) {
	r, c := newReconcilerFor(t, credentials("s", "alice", "pw", nil), newUser("u", "s", 0))

	_, err := r.Reconcile(context.Background(), request())
	require.NoError(t, err)

	u := storedUser(t, c, "u")
	assert.Equal(t, mkov1.ReasonBrokerNotFound, userReady(t, u).Reason)
	assert.Contains(t, userReady(t, u).Message, testName)
}

// TestReconcile_AnUnchangedPassWritesNothing: the hashes are kept, so a pass
// over the same users leaves <name>-auth untouched and the reloader idle
// (ADR 0014 D3); a changed password rewrites exactly that line.
func TestReconcile_AnUnchangedPassWritesNothing(t *testing.T) {
	r, c := newReconcilerFor(t, newCR(),
		credentials("ha-mqtt", "homeassistant", "ha-pw", nil), newUser("homeassistant", "ha-mqtt", time.Hour),
		credentials("z2m-mqtt", "zigbee2mqtt", "z2m-pw", nil), newUser("zigbee2mqtt", "z2m-mqtt", time.Minute),
	)
	reconciled(t, r, c)
	first := authSecret(t, c)

	reconciled(t, r, c)
	assert.Equal(t, first.ResourceVersion, authSecret(t, c).ResourceVersion, "nothing changed, nothing written")

	secret := &corev1.Secret{}
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "z2m-mqtt", Namespace: testNamespace}, secret))
	secret.Data["password"] = []byte("rotated")
	require.NoError(t, c.Update(context.Background(), secret))
	reconciled(t, r, c)

	before, after := auth.FilePayload{}.Hashes(first.Data), auth.FilePayload{}.Hashes(authSecret(t, c).Data)
	assert.Equal(t, before["homeassistant"], after["homeassistant"])
	assert.True(t, auth.VerifyPassword(after["zigbee2mqtt"], "rotated"))
}

// TestReconcile_ARefusedConfigWritesNothing is ADR 0008 D15: a line outside the
// allowlist fails the pass by name, and not one object is written, so a running
// broker keeps its configuration.
func TestReconcile_ARefusedConfigWritesNothing(t *testing.T) {
	r, c := newReconcilerFor(t, newCR(func(m *mkov1.Mosquitto) {
		m.Spec.Config = "max_keepalive 60\nlistener 1884\nlistener_allow_anonymous true"
	}))

	_, err := r.Reconcile(context.Background(), request())
	require.NoError(t, err)

	stored := &mkov1.Mosquitto{}
	require.NoError(t, c.Get(context.Background(), request().NamespacedName, stored))
	assert.Equal(t, mkov1.PhaseFailed, stored.Status.Phase)
	assert.Equal(t, mkov1.ReasonConfigDirectiveRefused, readyCondition(t, stored).Reason)
	assert.Contains(t, readyCondition(t, stored).Message, `spec.config line 2 ("listener 1884")`)

	for _, obj := range []client.Object{&corev1.Secret{}, &corev1.ConfigMap{}} {
		key := types.NamespacedName{Name: testName + "-auth", Namespace: testNamespace}
		if _, ok := obj.(*corev1.ConfigMap); ok {
			key.Name = testName + "-config"
		}
		assert.True(t, apierrors.IsNotFound(c.Get(context.Background(), key, obj)), "%T was written", obj)
	}
}

// TestReconcile_ANamespaceOutsideTheGrant is ADR 0014 D7's namespaces mode: the
// broker and its users say which setting refuses them, and nothing is written.
func TestReconcile_ANamespaceOutsideTheGrant(t *testing.T) {
	r, c := newReconcilerFor(t, newCR(), credentials("s", "alice", "pw", nil), newUser("u", "s", 0))
	r.SecretNamespaces = []string{"home"}

	_, err := r.Reconcile(context.Background(), request())
	require.NoError(t, err)

	stored := &mkov1.Mosquitto{}
	require.NoError(t, c.Get(context.Background(), request().NamespacedName, stored))
	assert.Equal(t, mkov1.ReasonNamespaceNotGranted, readyCondition(t, stored).Reason)
	assert.Contains(t, readyCondition(t, stored).Message, "--secret-namespaces")
	assert.Equal(t, mkov1.ReasonNamespaceNotGranted, userReady(t, storedUser(t, c, "u")).Reason)
	assert.True(t, apierrors.IsNotFound(c.Get(context.Background(),
		types.NamespacedName{Name: testName + "-auth", Namespace: testNamespace}, &corev1.Secret{})))
}

func TestBrokersForSecret(t *testing.T) {
	r, _ := newReconcilerFor(t,
		newCR(func(m *mkov1.Mosquitto) { m.Spec.TLS = &mkov1.MosquittoTLS{SecretName: "shared"} }),
		newUser("a", "shared", 0), newUser("b", "other", 0),
		func() *mkov1.MosquittoUser {
			u := newUser("c", "shared", 0)
			u.Spec.BrokerRef.Name = "second"
			return u
		}(),
	)
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "shared", Namespace: testNamespace}}

	requests := r.brokersForSecret(context.Background(), secret)
	assert.ElementsMatch(t, []reconcile.Request{
		{NamespacedName: types.NamespacedName{Name: testName, Namespace: testNamespace}},
		{NamespacedName: types.NamespacedName{Name: "second", Namespace: testNamespace}},
	}, requests, "each broker once, whether a user or its TLS listener names the Secret")
}

// TestUserBrokers_AMoveWakesBothBrokers: a user whose brokerRef changes has to
// leave the old broker's credentials as it enters the new one's.
func TestUserBrokers_AMoveWakesBothBrokers(t *testing.T) {
	queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]())
	defer queue.ShutDown()

	old := newUser("u", "s", 0)
	moved := old.DeepCopy()
	moved.Spec.BrokerRef.Name = "other"
	userBrokers().Update(context.Background(), event.TypedUpdateEvent[client.Object]{ObjectOld: old, ObjectNew: moved}, queue)

	var names []string
	for queue.Len() > 0 {
		item, _ := queue.Get()
		names = append(names, item.Name)
		queue.Done(item)
	}
	assert.ElementsMatch(t, []string{testName, "other"}, names)
}
