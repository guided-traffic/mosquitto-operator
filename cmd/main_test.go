package main

import (
	"flag"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
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
	}))

	assert.Equal(t, ":9090", f.metricsAddr)
	assert.Equal(t, ":9091", f.probeAddr)
	assert.True(t, f.enableLeaderElection)
	assert.Equal(t, 8, f.maxConcurrentReconciles)
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

	r := newReconciler(mgr, &operatorFlags{maxConcurrentReconciles: 6})

	assert.NotNil(t, r.Client, "without a client the reconciler can neither read nor write objects")
	assert.Same(t, scheme, r.Scheme, "the scheme must be the manager's, or SetControllerReference fails")
	assert.Equal(t, 6, r.MaxConcurrentReconciles,
		"the flag is only worth having if it reaches the reconciler")
}

// TestSetupWithManagerRegistersTheController covers the watch wiring: a typo in
// an Owns() type shows up here rather than as a controller that never wakes.
func TestSetupWithManagerRegistersTheController(t *testing.T) {
	mgr, err := ctrl.NewManager(&rest.Config{Host: "http://127.0.0.1:1"}, ctrl.Options{Scheme: scheme})
	require.NoError(t, err)

	require.NoError(t, newReconciler(mgr, &operatorFlags{}).SetupWithManager(mgr))
}
