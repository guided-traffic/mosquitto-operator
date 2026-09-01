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
func TestReconcile_RefusesForeignObjects(t *testing.T) {
	tests := []struct {
		name    string
		foreign client.Object
	}{
		{
			name: "ConfigMap",
			foreign: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: "broker-config", Namespace: testNamespace},
				Data:       map[string]string{"unrelated": "content"},
			},
		},
		{
			name: "headless Service",
			foreign: &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{Name: "broker-headless", Namespace: testNamespace},
				Spec:       corev1.ServiceSpec{Selector: map[string]string{"app": "someone-else"}},
			},
		},
		{
			name: "client Service",
			foreign: &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{Name: testName, Namespace: testNamespace},
				Spec:       corev1.ServiceSpec{Selector: map[string]string{"app": "someone-else"}},
			},
		},
		{
			name: "StatefulSet",
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
			assert.Contains(t, err.Error(), "not owned by this Mosquitto")

			stored := &mkov1.Mosquitto{}
			require.NoError(t, c.Get(context.Background(), request().NamespacedName, stored))
			assert.Equal(t, mkov1.PhaseFailed, stored.Status.Phase,
				"the refusal has to be visible on the resource, not only in the operator log")
			assert.Equal(t, metav1.ConditionFalse, readyCondition(t, stored).Status)
			assert.Equal(t, "ReconcileFailed", readyCondition(t, stored).Reason)
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
