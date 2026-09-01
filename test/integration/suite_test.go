//go:build integration

// Package integration runs the real reconciler against a real API server
// (envtest), which is the tier that can answer what unit tests cannot: whether
// the objects the builder produces are actually accepted by the API server,
// whether the owner references the reconciler writes are accepted as written,
// and whether the CRD's validation and defaulting behave as the markers claim.
//
// What this tier deliberately cannot answer, because envtest starts only the API
// server and etcd:
//   - Garbage collection. There is no kube-controller-manager, so no garbage
//     collector runs and deleting a Mosquitto removes nothing here. That the owner
//     references actually cause the managed objects to be collected is asserted in
//     test/e2e (see the "deleting the CR removes everything it owns" subtest).
//   - Anything that depends on a running broker: there is no kubelet, so no pod
//     ever starts. That belongs in test/e2e too.
package integration

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	mkov1 "github.com/guided-traffic/mosquitto-operator/api/v1"
	"github.com/guided-traffic/mosquitto-operator/internal/controller"
)

// Shared test infrastructure. One envtest control plane and one controller
// manager are started for the whole package: controller-runtime refuses to
// register two controllers of the same name in one process, so a per-test
// manager would fail on the second test.
var (
	testCtx    context.Context
	testCancel context.CancelFunc
	k8sClient  client.Client
	testEnv    *envtest.Environment

	// namespaceCounter gives every test its own namespace. The manager reconciles
	// everything in the cluster at once, so tests that shared a namespace would
	// see each other's objects.
	namespaceCounter atomic.Int64
)

// eventuallyTimeout and eventuallyInterval bound the waits for a reconcile pass
// to land. Without a kubelet in the way these are fast; the budget only has to
// cover the work queue and the informer.
const (
	eventuallyTimeout  = 30 * time.Second
	eventuallyInterval = 200 * time.Millisecond
)

// TestMain starts envtest, registers the schemes, runs the controller manager and
// then the tests.
func TestMain(m *testing.M) {
	log.SetLogger(zap.New(zap.UseDevMode(true)))

	testEnv = &envtest.Environment{
		CRDDirectoryPaths: []string{"../../config/crd/bases"},
		// A missing CRD directory would otherwise surface as "no matches for
		// kind Mosquitto" in every single test, which reads like an API defect
		// rather than a missing `make manifests`.
		ErrorIfCRDPathMissing: true,
	}

	cfg, err := testEnv.Start()
	if err != nil {
		panic("failed to start envtest: " + err.Error())
	}

	if err := mkov1.AddToScheme(scheme.Scheme); err != nil {
		panic("failed to register the mko scheme: " + err.Error())
	}
	if err := appsv1.AddToScheme(scheme.Scheme); err != nil {
		panic("failed to register the apps scheme: " + err.Error())
	}

	// The metrics listener is switched off: the suite asserts nothing about it,
	// and controller-runtime would otherwise take :8080 on every interface for
	// the lifetime of the test binary.
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:  scheme.Scheme,
		Metrics: metricsserver.Options{BindAddress: "0"},
	})
	if err != nil {
		panic("failed to create the manager: " + err.Error())
	}

	reconciler := &controller.MosquittoReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}
	if err := reconciler.SetupWithManager(mgr); err != nil {
		panic("failed to set up the controller: " + err.Error())
	}

	testCtx, testCancel = context.WithCancel(context.Background())

	go func() {
		if err := mgr.Start(testCtx); err != nil {
			panic("manager exited with an error: " + err.Error())
		}
	}()

	if !mgr.GetCache().WaitForCacheSync(testCtx) {
		panic("cache did not sync")
	}

	k8sClient = mgr.GetClient()

	code := m.Run()

	testCancel()
	if err := testEnv.Stop(); err != nil {
		panic("failed to stop envtest: " + err.Error())
	}

	os.Exit(code)
}

// newNamespace creates a namespace nobody else in this package writes to.
//
// It is never deleted: envtest has no namespace controller, so a Terminating
// namespace would never finish and the wait would be the whole test budget. The
// control plane is thrown away when the suite ends.
func newNamespace(t *testing.T) string {
	t.Helper()

	name := fmt.Sprintf("mko-int-%d", namespaceCounter.Add(1))
	require.NoError(t, k8sClient.Create(testCtx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: name},
	}), "failed to create namespace %s", name)
	return name
}

// createMosquitto writes a Mosquitto and returns it as the API server stored it,
// so CRD defaulting is visible to the caller.
func createMosquitto(t *testing.T, namespace, name string, spec mkov1.MosquittoSpec) *mkov1.Mosquitto {
	t.Helper()

	m := &mkov1.Mosquitto{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec:       spec,
	}
	require.NoError(t, k8sClient.Create(testCtx, m), "failed to create Mosquitto %s/%s", namespace, name)
	return m
}

// eventuallyGet waits until an object exists and copies it into out.
func eventuallyGet(t *testing.T, namespace, name string, out client.Object) {
	t.Helper()

	key := types.NamespacedName{Namespace: namespace, Name: name}
	require.Eventually(t, func() bool {
		return k8sClient.Get(testCtx, key, out) == nil
	}, eventuallyTimeout, eventuallyInterval, "object %s was not created", key)
}

// getMosquitto reads a Mosquitto back.
//
// The wait is not decoration: k8sClient reads through the manager's cache, and an
// object created a moment ago reaches that cache only with the watch event. A
// plain Get right after a Create loses that race often enough to be flaky.
func getMosquitto(t *testing.T, namespace, name string) *mkov1.Mosquitto {
	t.Helper()

	m := &mkov1.Mosquitto{}
	eventuallyGet(t, namespace, name, m)
	return m
}

// waitForPhase waits until a Mosquitto reports the expected phase.
func waitForPhase(t *testing.T, namespace, name, expected string) *mkov1.Mosquitto {
	t.Helper()

	m := &mkov1.Mosquitto{}
	key := types.NamespacedName{Namespace: namespace, Name: name}
	require.Eventually(t, func() bool {
		if err := k8sClient.Get(testCtx, key, m); err != nil {
			return false
		}
		return m.Status.Phase == expected
	}, eventuallyTimeout, eventuallyInterval,
		"Mosquitto %s did not reach phase %s", key, expected)
	return m
}

// waitForStatefulSet waits for the broker StatefulSet of one Mosquitto.
func waitForStatefulSet(t *testing.T, namespace, name string) *appsv1.StatefulSet {
	t.Helper()

	sts := &appsv1.StatefulSet{}
	eventuallyGet(t, namespace, name, sts)
	return sts
}

// isControlledBy reports whether an object carries a controller reference to the
// named Mosquitto. Garbage collection is driven by exactly this reference, and it
// is the only reason the suite can create objects it never deletes.
func isControlledBy(obj metav1.Object, name string) bool {
	owner := metav1.GetControllerOf(obj)
	return owner != nil &&
		owner.Kind == "Mosquitto" &&
		owner.APIVersion == mkov1.GroupVersion.String() &&
		owner.Name == name
}
