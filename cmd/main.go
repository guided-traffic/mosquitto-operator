// Package main is the entry point for the Mosquitto operator.
package main

import (
	"flag"
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	mkov1 "github.com/guided-traffic/mosquitto-operator/api/v1"
	"github.com/guided-traffic/mosquitto-operator/internal/controller"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")

	// Build information, set via ldflags.
	version   = "dev"
	commit    = "unknown"
	buildTime = "unknown"
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(mkov1.AddToScheme(scheme))
}

// operatorFlags holds the command line options of the operator.
type operatorFlags struct {
	metricsAddr             string
	probeAddr               string
	enableLeaderElection    bool
	maxConcurrentReconciles int
}

// bindOperatorFlags declares the operator flags on fs and returns the struct
// they write into once fs.Parse has run.
func bindOperatorFlags(fs *flag.FlagSet) *operatorFlags {
	f := &operatorFlags{}

	fs.StringVar(&f.metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	fs.StringVar(&f.probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	fs.BoolVar(&f.enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	fs.IntVar(&f.maxConcurrentReconciles, "max-concurrent-reconciles", controller.DefaultMaxConcurrentReconciles,
		"How many Mosquitto resources are reconciled at the same time. Passes for the same "+
			"resource stay serialised at any value.")

	return f
}

// bindZapFlags declares controller-runtime's logging flags on fs and returns the
// options they write into. The deployment passes --zap-log-level, so the flags
// have to exist even though nothing in this package reads their values.
func bindZapFlags(fs *flag.FlagSet) *zap.Options {
	opts := &zap.Options{Development: true}
	opts.BindFlags(fs)
	return opts
}

// managerOptions builds the controller-runtime manager options from the parsed flags.
func managerOptions(f *operatorFlags) ctrl.Options {
	return ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: f.metricsAddr},
		HealthProbeBindAddress: f.probeAddr,
		LeaderElection:         f.enableLeaderElection,
		LeaderElectionID:       "mosquitto-operator.mko.gtrfc.com",
	}
}

// newReconciler builds the Mosquitto reconciler from the manager and the parsed flags.
func newReconciler(mgr ctrl.Manager, f *operatorFlags) *controller.MosquittoReconciler {
	return &controller.MosquittoReconciler{
		Client:                  mgr.GetClient(),
		Scheme:                  mgr.GetScheme(),
		MaxConcurrentReconciles: f.maxConcurrentReconciles,
	}
}

func main() {
	flags := bindOperatorFlags(flag.CommandLine)
	zapOpts := bindZapFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(zapOpts)))

	setupLog.Info("starting mosquitto-operator",
		"version", version,
		"commit", commit,
		"buildTime", buildTime,
	)

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), managerOptions(flags))
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	if err := newReconciler(mgr, flags).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Mosquitto")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
