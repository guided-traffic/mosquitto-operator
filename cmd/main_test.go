package main

import (
	"flag"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"

	mkov1 "github.com/guided-traffic/mosquitto-operator/api/v1"
	"github.com/guided-traffic/mosquitto-operator/internal/controller"
)

// newTestFlagSet returns a FlagSet that reports parse errors instead of calling
// os.Exit, so a test can drive bindOperatorFlags without killing the test binary.
func newTestFlagSet() *flag.FlagSet {
	fs := flag.NewFlagSet("mosquitto-operator", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

// TestSchemeRegistersEveryTypeTheOperatorTouches guards the package init: the
// manager cache resolves these GVKs, and a missing AddToScheme surfaces only at
// runtime as "no kind is registered".
func TestSchemeRegistersEveryTypeTheOperatorTouches(t *testing.T) {
	tests := []struct {
		name string
		gvk  schema.GroupVersionKind
	}{
		{"Mosquitto CRD", mkov1.GroupVersion.WithKind("Mosquitto")},
		{"Mosquitto CRD list", mkov1.GroupVersion.WithKind("MosquittoList")},
		{"core service", corev1.SchemeGroupVersion.WithKind("Service")},
		{"core configmap", corev1.SchemeGroupVersion.WithKind("ConfigMap")},
		{"apps statefulset", appsv1.SchemeGroupVersion.WithKind("StatefulSet")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.True(t, scheme.Recognizes(tt.gvk), "scheme does not recognize %s", tt.gvk)
		})
	}
}

// TestBindOperatorFlags_Defaults pins the values the Helm chart and the E2E
// harness rely on when they pass no flag at all.
func TestBindOperatorFlags_Defaults(t *testing.T) {
	fs := newTestFlagSet()
	f := bindOperatorFlags(fs)
	require.NoError(t, fs.Parse(nil))

	assert.Equal(t, ":8080", f.metricsAddr)
	assert.Equal(t, ":8081", f.probeAddr)
	assert.False(t, f.enableLeaderElection,
		"leader election defaults to off so a single-replica deployment needs no lease RBAC")
	assert.Equal(t, controller.DefaultMaxConcurrentReconciles, f.maxConcurrentReconciles,
		"an operator started without the flag must not fall back to a single worker")
	assert.False(t, f.secretSecurity, "ADR 0014 D10: the owner chose false as the default")
	assert.Equal(t, "guidedtraffic/mosquitto-operator:dev", f.reloaderImage,
		"without the flag the reloader runs the published image of this build")
	assert.Empty(t, f.namespaces(), "ADR 0014 D7: the default grant is every namespace")
}

// TestBindOperatorFlags_AllFlagsParsed is the guard behind the chart: these are
// the exact flag names the deployment template passes.
func TestBindOperatorFlags_AllFlagsParsed(t *testing.T) {
	fs := newTestFlagSet()
	f := bindOperatorFlags(fs)

	require.NoError(t, fs.Parse([]string{
		"--metrics-bind-address=:9090",
		"--health-probe-bind-address=:9091",
		"--leader-elect=true",
		"--max-concurrent-reconciles=8",
		"--secret-security=false",
		"--secret-security=true",
		"--reloader-image=registry.example.com/mko:1.2.3",
		"--secret-namespaces= home, ,iot ",
	}))

	assert.Equal(t, ":9090", f.metricsAddr)
	assert.Equal(t, ":9091", f.probeAddr)
	assert.True(t, f.enableLeaderElection)
	assert.Equal(t, 8, f.maxConcurrentReconciles)
	assert.True(t, f.secretSecurity,
		"the last occurrence wins: the kustomize component appends --secret-security=true after the default")
	assert.Equal(t, "registry.example.com/mko:1.2.3", f.reloaderImage)
	assert.Equal(t, []string{"home", "iot"}, f.namespaces(), "blanks and empty entries are dropped")
}

// TestManagerOptions_SecretCache: the cache holds Secrets without their data,
// and with --secret-namespaces only in those namespaces (ADR 0014 D7).
func TestManagerOptions_SecretCache(t *testing.T) {
	everywhere := managerOptions(&operatorFlags{}).Cache.ByObject
	require.Len(t, everywhere, 1)
	for obj, byObject := range everywhere {
		assert.IsType(t, &corev1.Secret{}, obj)
		assert.NotNil(t, byObject.Transform, "Secrets are cached stripped of their data")
		assert.Nil(t, byObject.Namespaces, "every namespace without --secret-namespaces")
	}

	for _, byObject := range managerOptions(&operatorFlags{secretNamespaces: "home,iot"}).Cache.ByObject {
		assert.Len(t, byObject.Namespaces, 2)
		assert.Contains(t, byObject.Namespaces, "home")
		assert.Contains(t, byObject.Namespaces, "iot")
	}
}

// TestZapFlagsAreBound covers the other half of the chart's argument list: the
// logging flags come from controller-runtime, not from bindOperatorFlags.
func TestZapFlagsAreBound(t *testing.T) {
	fs := newTestFlagSet()
	bindOperatorFlags(fs)
	bindZapFlags(fs)

	require.NoError(t, fs.Parse([]string{"--zap-log-level=debug", "--zap-devel=false"}))

	assert.NotNil(t, fs.Lookup("zap-log-level"))
	assert.NotNil(t, fs.Lookup("zap-encoder"))
	assert.NotNil(t, fs.Lookup("zap-stacktrace-level"))
}

func TestManagerOptions(t *testing.T) {
	tests := []struct {
		name         string
		args         []string
		wantMetrics  string
		wantProbe    string
		wantElection bool
	}{
		{"defaults", nil, ":8080", ":8081", false},
		{"leader election on", []string{"--leader-elect=true"}, ":8080", ":8081", true},
		{"metrics disabled the controller-runtime way",
			[]string{"--metrics-bind-address=0"}, "0", ":8081", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs := newTestFlagSet()
			f := bindOperatorFlags(fs)
			require.NoError(t, fs.Parse(tt.args))

			opts := managerOptions(f)

			assert.Equal(t, tt.wantMetrics, opts.Metrics.BindAddress)
			assert.Equal(t, tt.wantProbe, opts.HealthProbeBindAddress)
			assert.Equal(t, tt.wantElection, opts.LeaderElection)
			assert.Equal(t, "mosquitto-operator.mko.gtrfc.com", opts.LeaderElectionID,
				"the lease name is what keeps two operator deployments from fighting; changing it "+
					"lets an old and a new one both act")
			assert.Same(t, scheme, opts.Scheme)
		})
	}
}

// TestNewReconciler checks that every flag the manager needs actually reaches
// the reconciler. The manager is built against an address nothing listens on:
// nothing is started here, only wired.
func TestNewReconciler(t *testing.T) {
	mgr, err := ctrl.NewManager(&rest.Config{Host: "http://127.0.0.1:1"}, ctrl.Options{Scheme: scheme})
	require.NoError(t, err)

	r := newReconciler(mgr, &operatorFlags{maxConcurrentReconciles: 6, secretSecurity: true,
		reloaderImage: "mko:test", secretNamespaces: "home"})

	assert.NotNil(t, r.Client, "without a client the reconciler can neither read nor write objects")
	assert.Same(t, scheme, r.Scheme, "the scheme must be the manager's, or SetControllerReference fails")
	assert.Equal(t, 6, r.MaxConcurrentReconciles,
		"the flag is only worth having if it reaches the reconciler")
	assert.True(t, r.SecretSecurity)
	assert.Equal(t, "mko:test", r.ReloaderImage)
	assert.Equal(t, []string{"home"}, r.SecretNamespaces)
	assert.Same(t, mgr.GetAPIReader(), r.APIReader,
		"the Secret check reads uncached, or it needs list and watch on every Secret")
}

// staticMapper answers the REST mapping of every kind the operator watches
// without a discovery call, so the field indexes register against a manager
// whose API server does not exist.
func staticMapper(*rest.Config, *http.Client) (meta.RESTMapper, error) {
	mapper := meta.NewDefaultRESTMapper(nil)
	for _, gvk := range []schema.GroupVersionKind{
		mkov1.GroupVersion.WithKind("Mosquitto"),
		mkov1.GroupVersion.WithKind("MosquittoUser"),
		corev1.SchemeGroupVersion.WithKind("ConfigMap"),
		corev1.SchemeGroupVersion.WithKind("Service"),
		corev1.SchemeGroupVersion.WithKind("Secret"),
		appsv1.SchemeGroupVersion.WithKind("StatefulSet"),
	} {
		mapper.Add(gvk, meta.RESTScopeNamespace)
	}
	return mapper, nil
}

// TestSetupWithManagerRegistersTheController covers the watch wiring: a typo in
// an Owns() type or an index shows up here rather than as a controller that
// never wakes.
func TestSetupWithManagerRegistersTheController(t *testing.T) {
	mgr, err := ctrl.NewManager(&rest.Config{Host: "http://127.0.0.1:1"},
		ctrl.Options{Scheme: scheme, MapperProvider: staticMapper})
	require.NoError(t, err)

	require.NoError(t, newReconciler(mgr, &operatorFlags{}).SetupWithManager(mgr))
}
