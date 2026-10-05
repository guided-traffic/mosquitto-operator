package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mkov1 "github.com/guided-traffic/mosquitto-operator/api/v1"
	"github.com/guided-traffic/mosquitto-operator/internal/builder"
	"github.com/guided-traffic/mosquitto-operator/internal/common"
)

const (
	testName      = "broker"
	testNamespace = "messaging"
)

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme))
	require.NoError(t, mkov1.AddToScheme(scheme))
	return scheme
}

func newCR(mutators ...func(*mkov1.Mosquitto)) *mkov1.Mosquitto {
	m := &mkov1.Mosquitto{
		ObjectMeta: metav1.ObjectMeta{
			Name:       testName,
			Namespace:  testNamespace,
			Generation: 1,
		},
		Spec: mkov1.MosquittoSpec{Replicas: 1},
	}
	for _, mutate := range mutators {
		mutate(m)
	}
	return m
}

// newReconcilerFor wires a reconciler over a fake client seeded with objs. The
// Mosquitto status is declared a subresource so status writes behave the way the
// API server does.
func newReconcilerFor(t *testing.T, objs ...client.Object) (*MosquittoReconciler, client.Client) {
	t.Helper()
	scheme := testScheme(t)
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&mkov1.Mosquitto{}).
		Build()

	return &MosquittoReconciler{Client: c, Scheme: scheme}, c
}

func request() ctrl.Request {
	return ctrl.Request{NamespacedName: types.NamespacedName{Name: testName, Namespace: testNamespace}}
}

// reconciled runs one pass and returns the stored resource.
func reconciled(t *testing.T, r *MosquittoReconciler, c client.Client) *mkov1.Mosquitto {
	t.Helper()
	_, err := r.Reconcile(context.Background(), request())
	require.NoError(t, err)

	stored := &mkov1.Mosquitto{}
	require.NoError(t, c.Get(context.Background(), request().NamespacedName, stored))
	return stored
}

func readyCondition(t *testing.T, m *mkov1.Mosquitto) *metav1.Condition {
	t.Helper()
	cond := meta.FindStatusCondition(m.Status.Conditions, mkov1.ConditionTypeReady)
	require.NotNil(t, cond, "every completed pass must leave a Ready condition")
	return cond
}

func TestReconcile_MissingResourceIsNotAnError(t *testing.T) {
	r, _ := newReconcilerFor(t)

	result, err := r.Reconcile(context.Background(), request())

	require.NoError(t, err, "a deleted resource must not be retried forever")
	assert.Zero(t, result.RequeueAfter, "and it must not be put back on the queue either")
}

// TestReconcile_DeletionIsLeftToGarbageCollection is why the pass returns early:
// the owner references delete the managed objects, and rewriting them would race
// that deletion.
func TestReconcile_DeletionIsLeftToGarbageCollection(t *testing.T) {
	cr := newCR(func(m *mkov1.Mosquitto) {
		m.DeletionTimestamp = &metav1.Time{Time: metav1.Now().Time}
		m.Finalizers = []string{"example.com/keep-for-the-test"}
	})
	r, c := newReconcilerFor(t, cr)

	_, err := r.Reconcile(context.Background(), request())
	require.NoError(t, err)

	sts := &appsv1.StatefulSet{}
	err = c.Get(context.Background(), types.NamespacedName{Name: testName, Namespace: testNamespace}, sts)
	assert.True(t, apierrors.IsNotFound(err), "no object may be created for a resource that is going away")
}

func TestReconcile_CreatesEveryManagedObject(t *testing.T) {
	cr := newCR(func(m *mkov1.Mosquitto) { m.Spec.Replicas = 3 })
	r, c := newReconcilerFor(t, cr)

	_, err := r.Reconcile(context.Background(), request())
	require.NoError(t, err)

	ctx := context.Background()
	key := func(name string) types.NamespacedName {
		return types.NamespacedName{Name: name, Namespace: testNamespace}
	}

	cm := &corev1.ConfigMap{}
	require.NoError(t, c.Get(ctx, key("broker-config"), cm))
	assert.Contains(t, cm.Data[builder.ConfigKey], "listener 1883")

	headless := &corev1.Service{}
	require.NoError(t, c.Get(ctx, key("broker-headless"), headless))
	assert.Equal(t, corev1.ClusterIPNone, headless.Spec.ClusterIP)

	clientSvc := &corev1.Service{}
	require.NoError(t, c.Get(ctx, key("broker"), clientSvc))
	assert.NotEqual(t, corev1.ClusterIPNone, clientSvc.Spec.ClusterIP)

	sts := &appsv1.StatefulSet{}
	require.NoError(t, c.Get(ctx, key("broker"), sts))
	assert.Equal(t, int32(3), *sts.Spec.Replicas)

	for _, obj := range []metav1.Object{cm, headless, clientSvc, sts} {
		assert.True(t, metav1.IsControlledBy(obj, cr),
			"%s must be owned by the resource, or nothing cleans it up", obj.GetName())
	}
}

func TestReconcile_IsIdempotent(t *testing.T) {
	r, c := newReconcilerFor(t, newCR())

	first := reconciled(t, r, c)
	sts := &appsv1.StatefulSet{}
	require.NoError(t, c.Get(context.Background(),
		types.NamespacedName{Name: testName, Namespace: testNamespace}, sts))
	versionAfterCreate := sts.ResourceVersion

	second := reconciled(t, r, c)
	require.NoError(t, c.Get(context.Background(),
		types.NamespacedName{Name: testName, Namespace: testNamespace}, sts))

	assert.Equal(t, versionAfterCreate, sts.ResourceVersion,
		"a second pass over an unchanged spec must not write the StatefulSet again")
	assert.Equal(t, first.ResourceVersion, second.ResourceVersion,
		"an unchanged status must not be written again")
}

func TestReconcile_ConvergesADriftedConfigMap(t *testing.T) {
	r, c := newReconcilerFor(t, newCR())
	ctx := context.Background()
	require.NoError(t, func() error { _, err := r.Reconcile(ctx, request()); return err }())

	key := types.NamespacedName{Name: "broker-config", Namespace: testNamespace}
	cm := &corev1.ConfigMap{}
	require.NoError(t, c.Get(ctx, key, cm))
	cm.Data[builder.ConfigKey] = "listener 1"
	require.NoError(t, c.Update(ctx, cm))

	_, err := r.Reconcile(ctx, request())
	require.NoError(t, err)

	require.NoError(t, c.Get(ctx, key, cm))
	assert.Contains(t, cm.Data[builder.ConfigKey], "listener 1883")
}

func TestReconcile_ConvergesADriftedService(t *testing.T) {
	r, c := newReconcilerFor(t, newCR())
	ctx := context.Background()
	require.NoError(t, func() error { _, err := r.Reconcile(ctx, request()); return err }())

	key := types.NamespacedName{Name: testName, Namespace: testNamespace}
	svc := &corev1.Service{}
	require.NoError(t, c.Get(ctx, key, svc))
	svc.Spec.Selector = map[string]string{"app": "something-else"}
	require.NoError(t, c.Update(ctx, svc))

	_, err := r.Reconcile(ctx, request())
	require.NoError(t, err)

	require.NoError(t, c.Get(ctx, key, svc))
	assert.Equal(t, common.SelectorLabels(newCR()), svc.Spec.Selector,
		"a Service selector is mutable, so only the operator rewriting it converges the traffic")
}

func TestReconcile_ScalesTheStatefulSet(t *testing.T) {
	cr := newCR()
	r, c := newReconcilerFor(t, cr)
	ctx := context.Background()
	require.NoError(t, func() error { _, err := r.Reconcile(ctx, request()); return err }())

	stored := &mkov1.Mosquitto{}
	require.NoError(t, c.Get(ctx, request().NamespacedName, stored))
	stored.Spec.Replicas = 3
	require.NoError(t, c.Update(ctx, stored))

	_, err := r.Reconcile(ctx, request())
	require.NoError(t, err)

	sts := &appsv1.StatefulSet{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: testName, Namespace: testNamespace}, sts))
	assert.Equal(t, int32(3), *sts.Spec.Replicas)
}

// TestReconcile_RefusesForeignObjects is the guard against adoption: every
// managed name is derived from the resource name, so a pre-existing object can
// hold one, and writing it would hand somebody else's workload or traffic to
// this operator.
//
// The message is pinned exactly, on the error and on the Ready condition: it is
// what a user reads in kubectl get, and ADR 0009 D5 keeps it precise - kind,
// namespace and name - with the existence oracle that precision gives accepted.
func TestReconcile_RefusesForeignObjects(t *testing.T) {
	tests := []struct {
		name        string
		foreign     client.Object
		wantMessage string
	}{
		{
			name:        "ConfigMap",
			wantMessage: "ConfigMap messaging/broker-config exists and is not owned by this Mosquitto",
			foreign: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: "broker-config", Namespace: testNamespace},
				Data:       map[string]string{"unrelated": "content"},
			},
		},
		{
			name:        "headless Service",
			wantMessage: "Service messaging/broker-headless exists and is not owned by this Mosquitto",
			foreign: &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{Name: "broker-headless", Namespace: testNamespace},
				Spec:       corev1.ServiceSpec{Selector: map[string]string{"app": "someone-else"}},
			},
		},
		{
			name:        "client Service",
			wantMessage: "Service messaging/broker exists and is not owned by this Mosquitto",
			foreign: &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{Name: testName, Namespace: testNamespace},
				Spec:       corev1.ServiceSpec{Selector: map[string]string{"app": "someone-else"}},
			},
		},
		{
			name:        "StatefulSet",
			wantMessage: "StatefulSet messaging/broker exists and is not owned by this Mosquitto",
			foreign: &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{Name: testName, Namespace: testNamespace},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, c := newReconcilerFor(t, newCR(), tt.foreign)

			_, err := r.Reconcile(context.Background(), request())

			require.Error(t, err)
			assert.Equal(t, tt.wantMessage, err.Error())

			stored := &mkov1.Mosquitto{}
			require.NoError(t, c.Get(context.Background(), request().NamespacedName, stored))
			assert.Equal(t, mkov1.PhaseFailed, stored.Status.Phase,
				"the refusal has to be visible on the resource, not only in the operator log")
			assert.Equal(t, metav1.ConditionFalse, readyCondition(t, stored).Status)
			assert.Equal(t, "ReconcileFailed", readyCondition(t, stored).Reason)
			assert.Equal(t, tt.wantMessage, readyCondition(t, stored).Message)
		})
	}
}

// TestReconcile_UnbuildableSpecFailsVisibly covers the one build error the
// builder can raise: a storage size the quantity parser rejects.
func TestReconcile_UnbuildableSpecFailsVisibly(t *testing.T) {
	cr := newCR(func(m *mkov1.Mosquitto) {
		m.Spec.Storage = &mkov1.MosquittoStorage{Size: "five gigabytes"}
	})
	r, c := newReconcilerFor(t, cr)

	_, err := r.Reconcile(context.Background(), request())

	require.Error(t, err)
	stored := &mkov1.Mosquitto{}
	require.NoError(t, c.Get(context.Background(), request().NamespacedName, stored))
	assert.Equal(t, mkov1.PhaseFailed, stored.Status.Phase)
	assert.Contains(t, readyCondition(t, stored).Message, "spec.storage.size")
}

func TestReconcile_StatusPhases(t *testing.T) {
	tests := []struct {
		name       string
		replicas   int32
		ready      int32
		wantPhase  string
		wantStatus metav1.ConditionStatus
		wantReason string
	}{
		{"nothing ready yet", 3, 0, mkov1.PhasePending, metav1.ConditionFalse, "NoReplicasReady"},
		{"partly ready", 3, 2, mkov1.PhaseProgressing, metav1.ConditionFalse, "ReplicasNotReady"},
		{"all ready", 3, 3, mkov1.PhaseReady, metav1.ConditionTrue, "AllReplicasReady"},
		{"single replica ready", 1, 1, mkov1.PhaseReady, metav1.ConditionTrue, "AllReplicasReady"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cr := newCR(func(m *mkov1.Mosquitto) { m.Spec.Replicas = tt.replicas })
			r, c := newReconcilerFor(t, cr)
			ctx := context.Background()

			// First pass creates the StatefulSet; the fake client reports no ready
			// pods, so the readiness is stamped on by hand before the second pass.
			require.NoError(t, func() error { _, err := r.Reconcile(ctx, request()); return err }())

			sts := &appsv1.StatefulSet{}
			key := types.NamespacedName{Name: testName, Namespace: testNamespace}
			require.NoError(t, c.Get(ctx, key, sts))
			sts.Status.ReadyReplicas = tt.ready
			require.NoError(t, c.Status().Update(ctx, sts))

			stored := reconciled(t, r, c)

			assert.Equal(t, tt.wantPhase, stored.Status.Phase)
			assert.Equal(t, tt.ready, stored.Status.ReadyReplicas)
			assert.Equal(t, int64(1), stored.Status.ObservedGeneration)

			cond := readyCondition(t, stored)
			assert.Equal(t, tt.wantStatus, cond.Status)
			assert.Equal(t, tt.wantReason, cond.Reason)
			assert.Equal(t, int64(1), cond.ObservedGeneration)
		})
	}
}

// TestUpdateStatus_MissingStatefulSetIsPending covers the window between the CR
// being accepted and its workload existing — and the case where somebody deletes
// the StatefulSet under a running operator.
func TestUpdateStatus_MissingStatefulSetIsPending(t *testing.T) {
	cr := newCR()
	r, c := newReconcilerFor(t, cr)

	require.NoError(t, r.updateStatus(context.Background(), cr))

	stored := &mkov1.Mosquitto{}
	require.NoError(t, c.Get(context.Background(), request().NamespacedName, stored))
	assert.Equal(t, mkov1.PhasePending, stored.Status.Phase)
	assert.Equal(t, int32(0), stored.Status.ReadyReplicas)
	assert.Equal(t, "StatefulSetNotFound", readyCondition(t, stored).Reason)
}

// TestObservedGenerationFollowsTheSpec is what tells a reader whether the status
// describes the spec they just applied or the one before it.
func TestObservedGenerationFollowsTheSpec(t *testing.T) {
	cr := newCR()
	r, c := newReconcilerFor(t, cr)
	ctx := context.Background()

	first := reconciled(t, r, c)
	assert.Equal(t, int64(1), first.Status.ObservedGeneration)

	first.Generation = 4
	first.Spec.Replicas = 2
	require.NoError(t, c.Update(ctx, first))

	second := reconciled(t, r, c)
	assert.Equal(t, int64(4), second.Status.ObservedGeneration)
	assert.Equal(t, int64(4), readyCondition(t, second).ObservedGeneration)
}

func TestStatusUnchanged(t *testing.T) {
	base := mkov1.MosquittoStatus{
		Phase:              mkov1.PhaseReady,
		ReadyReplicas:      3,
		ObservedGeneration: 2,
		Conditions: []metav1.Condition{{
			Type: mkov1.ConditionTypeReady, Status: metav1.ConditionTrue, Reason: "AllReplicasReady",
		}},
	}

	tests := []struct {
		name   string
		mutate func(*mkov1.MosquittoStatus)
		want   bool
	}{
		{"identical", func(*mkov1.MosquittoStatus) {}, true},
		{"phase", func(s *mkov1.MosquittoStatus) { s.Phase = mkov1.PhaseProgressing }, false},
		{"ready replicas", func(s *mkov1.MosquittoStatus) { s.ReadyReplicas = 2 }, false},
		{"observed generation", func(s *mkov1.MosquittoStatus) { s.ObservedGeneration = 3 }, false},
		{"condition status", func(s *mkov1.MosquittoStatus) { s.Conditions[0].Status = metav1.ConditionFalse }, false},
		{"condition reason", func(s *mkov1.MosquittoStatus) { s.Conditions[0].Reason = "Other" }, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			curr := *base.DeepCopy()
			tt.mutate(&curr)
			assert.Equal(t, tt.want, statusUnchanged(&base, &curr))
		})
	}
}

func TestEnsureOwned(t *testing.T) {
	cr := newCR()
	cr.UID = "owner-uid"

	owned := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
		Name:      "broker-config",
		Namespace: testNamespace,
		OwnerReferences: []metav1.OwnerReference{{
			APIVersion: mkov1.GroupVersion.String(),
			Kind:       "Mosquitto",
			Name:       cr.Name,
			UID:        cr.UID,
			Controller: func() *bool { b := true; return &b }(),
		}},
	}}
	foreign := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
		Name: "broker-config", Namespace: testNamespace,
	}}

	assert.NoError(t, ensureOwned(owned, cr, "ConfigMap"))
	assert.Error(t, ensureOwned(foreign, cr, "ConfigMap"))
}

func TestMaxConcurrentReconciles(t *testing.T) {
	tests := []struct {
		name string
		set  int
		want int
	}{
		{"unset falls back to the default", 0, DefaultMaxConcurrentReconciles},
		{"a negative value falls back too", -1, DefaultMaxConcurrentReconciles},
		{"an explicit value is kept", 2, 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, maxConcurrentReconciles(tt.set))
		})
	}

	assert.Greater(t, DefaultMaxConcurrentReconciles, 1,
		"one worker couples every resource in the cluster to the slowest pass")
}

// TestReconcile_UpdatesKeepForeignLabels is ADR 0009 D9: an update made for
// another reason keeps the labels other writers added, on every kind, instead of
// dropping them at random whenever the operator happens to write.
func TestReconcile_UpdatesKeepForeignLabels(t *testing.T) {
	r, c := newReconcilerFor(t, newCR())
	ctx := context.Background()
	require.NoError(t, func() error { _, err := r.Reconcile(ctx, request()); return err }())

	objects := []client.Object{
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "broker-config", Namespace: testNamespace}},
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "broker-headless", Namespace: testNamespace}},
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: testName, Namespace: testNamespace}},
		&appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: testName, Namespace: testNamespace}},
	}
	for _, obj := range objects {
		require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(obj), obj))
		labels := obj.GetLabels()
		labels["kustomize.toolkit.fluxcd.io/name"] = "mqtt"
		obj.SetLabels(labels)
		require.NoError(t, c.Update(ctx, obj))
	}

	// TLS moves the listener: new ConfigMap data, new Service ports, a new pod
	// template. Every one of the four objects is written for that reason.
	stored := &mkov1.Mosquitto{}
	require.NoError(t, c.Get(ctx, request().NamespacedName, stored))
	stored.Spec.TLS = &mkov1.MosquittoTLS{SecretName: "broker-tls"}
	require.NoError(t, c.Update(ctx, stored))
	_, err := r.Reconcile(ctx, request())
	require.NoError(t, err)

	for _, obj := range objects {
		require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(obj), obj))
		assert.Equal(t, "mqtt", obj.GetLabels()["kustomize.toolkit.fluxcd.io/name"],
			"%T %s lost a foreign label on an update made for another reason", obj, obj.GetName())
		assert.Equal(t, testName, obj.GetLabels()[common.LabelInstance], "%T %s", obj, obj.GetName())
	}
	cm := objects[0].(*corev1.ConfigMap)
	assert.Contains(t, cm.Data[builder.ConfigKey], "listener 8883", "the update itself still happened")
}

// TestReconcile_ReplicaChangeKeepsTheTemplateMetadataOthersAdded: a foreign
// template label and the annotation kubectl rollout restart writes survive a
// write caused only by spec.replicas. Dropping restartedAt there would roll every
// pod a second time.
func TestReconcile_ReplicaChangeKeepsTheTemplateMetadataOthersAdded(t *testing.T) {
	r, c := newReconcilerFor(t, newCR())
	ctx := context.Background()
	require.NoError(t, func() error { _, err := r.Reconcile(ctx, request()); return err }())

	key := types.NamespacedName{Name: testName, Namespace: testNamespace}
	sts := &appsv1.StatefulSet{}
	require.NoError(t, c.Get(ctx, key, sts))
	sts.Spec.Template.Labels["policy.example.com/scanned"] = "true"
	sts.Spec.Template.Annotations["kubectl.kubernetes.io/restartedAt"] = "2026-10-05T00:00:00Z"
	require.NoError(t, c.Update(ctx, sts))

	stored := &mkov1.Mosquitto{}
	require.NoError(t, c.Get(ctx, request().NamespacedName, stored))
	stored.Spec.Replicas = 2
	require.NoError(t, c.Update(ctx, stored))
	_, err := r.Reconcile(ctx, request())
	require.NoError(t, err)

	require.NoError(t, c.Get(ctx, key, sts))
	assert.Equal(t, int32(2), *sts.Spec.Replicas)
	assert.Equal(t, "true", sts.Spec.Template.Labels["policy.example.com/scanned"])
	assert.Equal(t, "2026-10-05T00:00:00Z", sts.Spec.Template.Annotations["kubectl.kubernetes.io/restartedAt"])
}

// TestReconcile_PodLabelsReachAndLeaveTheTemplate covers ADR 0012 D5 through
// the reconciler: a pod label set on the CR reaches the pod template, and one
// removed from the CR leaves it, while a foreign template label stays.
func TestReconcile_PodLabelsReachAndLeaveTheTemplate(t *testing.T) {
	r, c := newReconcilerFor(t, newCR(func(m *mkov1.Mosquitto) {
		m.Spec.PodLabels = map[string]string{"team": "iot", "network.example.com/allow-mqtt": "true"}
	}))
	ctx := context.Background()
	require.NoError(t, func() error { _, err := r.Reconcile(ctx, request()); return err }())

	key := types.NamespacedName{Name: testName, Namespace: testNamespace}
	sts := &appsv1.StatefulSet{}
	require.NoError(t, c.Get(ctx, key, sts))
	assert.Equal(t, "true", sts.Spec.Template.Labels["network.example.com/allow-mqtt"])
	sts.Spec.Template.Labels["policy.example.com/scanned"] = "true"
	require.NoError(t, c.Update(ctx, sts))

	stored := &mkov1.Mosquitto{}
	require.NoError(t, c.Get(ctx, request().NamespacedName, stored))
	stored.Spec.PodLabels = map[string]string{"team": "iot"}
	require.NoError(t, c.Update(ctx, stored))
	_, err := r.Reconcile(ctx, request())
	require.NoError(t, err)

	require.NoError(t, c.Get(ctx, key, sts))
	assert.NotContains(t, sts.Spec.Template.Labels, "network.example.com/allow-mqtt",
		"a label removed in Git must leave the pods: a NetworkPolicy may select on it")
	assert.Equal(t, "iot", sts.Spec.Template.Labels["team"])
	assert.Equal(t, "true", sts.Spec.Template.Labels["policy.example.com/scanned"])
}

func tlsSecret(labels map[string]string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "broker-tls", Namespace: testNamespace, Labels: labels},
		Type:       corev1.SecretTypeTLS,
		Data:       map[string][]byte{"tls.crt": []byte("c"), "tls.key": []byte("k")},
	}
}

func withTLSSecret(m *mkov1.Mosquitto) {
	m.Spec.TLS = &mkov1.MosquittoTLS{SecretName: "broker-tls"}
}

// TestReconcile_SecretSecurity is ADR 0014 D10 for the TLS Secret: with the
// switch on, a Secret without the consent label - or no Secret at all - is
// refused visibly and nothing is written; with the label, or with the switch
// off, the broker is built as before.
func TestReconcile_SecretSecurity(t *testing.T) {
	consenting := map[string]string{mkov1.ConsumableLabel: mkov1.ConsumableLabelValue}

	tests := []struct {
		name       string
		switchOn   bool
		secret     *corev1.Secret
		wantReason string
	}{
		{"on, labelled: accepted", true, tlsSecret(consenting), ""},
		{"on, unlabelled: refused", true, tlsSecret(nil), mkov1.ReasonSecretNotConsumable},
		{"on, label with another value: refused", true,
			tlsSecret(map[string]string{mkov1.ConsumableLabel: "yes"}), mkov1.ReasonSecretNotConsumable},
		{"on, no Secret: refused", true, nil, mkov1.ReasonSecretNotFound},
		{"off, unlabelled: accepted", false, tlsSecret(nil), ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			objs := []client.Object{newCR(withTLSSecret)}
			if tt.secret != nil {
				objs = append(objs, tt.secret)
			}
			r, c := newReconcilerFor(t, objs...)
			r.SecretSecurity = tt.switchOn
			ctx := context.Background()

			result, err := r.Reconcile(ctx, request())
			require.NoError(t, err)

			stored := &mkov1.Mosquitto{}
			require.NoError(t, c.Get(ctx, request().NamespacedName, stored))
			sts := &appsv1.StatefulSet{}
			stsErr := c.Get(ctx, types.NamespacedName{Name: testName, Namespace: testNamespace}, sts)

			if tt.wantReason == "" {
				require.NoError(t, stsErr, "an accepted Secret builds the broker")
				assert.NotEqual(t, mkov1.PhaseFailed, stored.Status.Phase)
				return
			}
			assert.True(t, apierrors.IsNotFound(stsErr), "a refused Secret must not be mounted into a new StatefulSet")
			assert.Equal(t, mkov1.PhaseFailed, stored.Status.Phase)
			assert.Equal(t, metav1.ConditionFalse, readyCondition(t, stored).Status)
			assert.Equal(t, tt.wantReason, readyCondition(t, stored).Reason)
			assert.Contains(t, readyCondition(t, stored).Message, mkov1.ConsumableLabel,
				"the message names the label, so the fix is readable from kubectl get")
			assert.Equal(t, secretRecheckInterval, result.RequeueAfter,
				"no Secret is watched, so a label added later is noticed by the requeue")
		})
	}
}

// TestReconcile_SecretSecurityLeavesARunningBrokerAlone: a Secret that loses its
// label refuses the next pass, and the StatefulSet stays as it was - the change
// that pass would have made is not applied.
func TestReconcile_SecretSecurityLeavesARunningBrokerAlone(t *testing.T) {
	secret := tlsSecret(map[string]string{mkov1.ConsumableLabel: mkov1.ConsumableLabelValue})
	r, c := newReconcilerFor(t, newCR(withTLSSecret), secret)
	r.SecretSecurity = true
	ctx := context.Background()
	require.NoError(t, func() error { _, err := r.Reconcile(ctx, request()); return err }())

	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(secret), secret))
	secret.Labels = nil
	require.NoError(t, c.Update(ctx, secret))
	stored := &mkov1.Mosquitto{}
	require.NoError(t, c.Get(ctx, request().NamespacedName, stored))
	stored.Spec.Replicas = 3
	require.NoError(t, c.Update(ctx, stored))

	_, err := r.Reconcile(ctx, request())
	require.NoError(t, err)

	sts := &appsv1.StatefulSet{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: testName, Namespace: testNamespace}, sts))
	assert.Equal(t, int32(1), *sts.Spec.Replicas, "the refused pass wrote nothing")
	require.NoError(t, c.Get(ctx, request().NamespacedName, stored))
	assert.Equal(t, mkov1.ReasonSecretNotConsumable, readyCondition(t, stored).Reason)
}
