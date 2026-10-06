// Package main is the entry point for the Mosquitto operator.
package main

import (
	"flag"
	"os"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	mkov1 "github.com/guided-traffic/mosquitto-operator/api/v1"
	"github.com/guided-traffic/mosquitto-operator/internal/controller"
	"github.com/guided-traffic/mosquitto-operator/internal/reloader"
)

// defaultReloaderImage is the image auth-init and the reloader run when
// --reloader-image is not passed: the published image of this very build. A
// development build ("dev") has no published image, so the chart always passes
// the flag.
const defaultReloaderImage = "guidedtraffic/mosquitto-operator"

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
	secretSecurity          bool
	reloaderImage           string
	secretNamespaces        string
}

// namespaces returns --secret-namespaces as a list; empty means every
// namespace.
func (f *operatorFlags) namespaces() []string {
	var out []string
	for _, ns := range strings.Split(f.secretNamespaces, ",") {
		if ns = strings.TrimSpace(ns); ns != "" {
			out = append(out, ns)
		}
	}
	return out
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
	fs.BoolVar(&f.secretSecurity, "secret-security", false,
		"Mount a TLS Secret only when it carries the label "+mkov1.ConsumableLabel+"="+
			mkov1.ConsumableLabelValue+". Needs get on secrets. With false, any Secret of a "+
			"Mosquitto's namespace may be named, so whoever may write a Mosquitto may read the "+
			"Secrets of its namespace.")
	fs.StringVar(&f.reloaderImage, "reloader-image", defaultReloaderImage+":"+version,
		"The image of the auth-init and reloader containers in every broker pod: this operator's own image. "+
			"Every change of it rolls every broker.")
	fs.StringVar(&f.secretNamespaces, "secret-namespaces", "",
		"Comma-separated namespaces the operator may read and write Secrets in, matching the Roles the "+
			"install path granted. Empty: every namespace, through the ClusterRole. A Mosquitto in another "+
			"namespace is refused.")

	return f
}

// bindZapFlags declares controller-runtime's logging flags on fs and returns the
// options they write into. Neither install path passes one today; `make run`
// passes --zap-log-level=debug, and an operator who adds a --zap-* argument to
// the deployment needs the flag to exist, even though nothing in this package
// reads the values.
func bindZapFlags(fs *flag.FlagSet) *zap.Options {
	opts := &zap.Options{Development: true}
	opts.BindFlags(fs)
	return opts
}

// managerOptions builds the controller-runtime manager options from the parsed flags.
//
// Secrets are cached with their data stripped (controller.StripSecret), so the
// operator's memory holds no credential of the cluster, and with
// --secret-namespaces only in those namespaces, matching the grant (ADR 0014 D7).
func managerOptions(f *operatorFlags) ctrl.Options {
	secrets := cache.ByObject{Transform: controller.StripSecret}
	if namespaces := f.namespaces(); len(namespaces) > 0 {
		secrets.Namespaces = make(map[string]cache.Config, len(namespaces))
		for _, ns := range namespaces {
			secrets.Namespaces[ns] = cache.Config{}
		}
	}
	return ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: f.metricsAddr},
		HealthProbeBindAddress: f.probeAddr,
		LeaderElection:         f.enableLeaderElection,
		LeaderElectionID:       "mosquitto-operator.mko.gtrfc.com",
		Cache:                  cache.Options{ByObject: map[client.Object]cache.ByObject{&corev1.Secret{}: secrets}},
	}
}

// newReconciler builds the Mosquitto reconciler from the manager and the parsed flags.
func newReconciler(mgr ctrl.Manager, f *operatorFlags) *controller.MosquittoReconciler {
	return &controller.MosquittoReconciler{
		Client:                  mgr.GetClient(),
		Scheme:                  mgr.GetScheme(),
		MaxConcurrentReconciles: f.maxConcurrentReconciles,
		SecretSecurity:          f.secretSecurity,
		// Uncached: the cache holds Secrets without their data, so every read of
		// a Secret's data goes through this reader.
		APIReader:        mgr.GetAPIReader(),
		ReloaderImage:    f.reloaderImage,
		SecretNamespaces: f.namespaces(),
	}
}

func main() {
	// The second entry point: "manager reload" is auth-init and the reloader
	// sidecar of every broker pod (ADR 0014 D6). It shares the image, not the
	// operator's flags.
	if len(os.Args) > 1 && os.Args[1] == "reload" {
		os.Exit(reloader.Main(os.Args[2:], os.Stderr))
	}

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
