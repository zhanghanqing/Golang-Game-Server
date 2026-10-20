// Command operator runs the GameCluster controller.
//
//	operator --metrics-bind-address=:8080 --health-probe-bind-address=:8081 --leader-elect
package main

import (
	"flag"
	"os"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	gsv1 "gameserver/operator/api/v1alpha1"
	"gameserver/operator/internal/controller"
)

func main() {
	var (
		metricsAddr  string
		probeAddr    string
		leaderElect  bool
		adminPort    int
		pollInterval time.Duration
	)
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "address of the Prometheus metrics endpoint (0 disables it)")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "address of /healthz and /readyz")
	flag.BoolVar(&leaderElect, "leader-elect", false, "run several replicas with one active leader")
	flag.IntVar(&adminPort, "game-admin-port", 9100, "admin port of game nodes (/game/status, /game/drain)")
	flag.DurationVar(&pollInterval, "poll-interval", 5*time.Second, "how often player counts are refreshed")
	opts := zap.Options{}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	setupLog := ctrl.Log.WithName("setup")

	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(gsv1.AddToScheme(scheme))

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         leaderElect,
		LeaderElectionID:       "gameserver-operator.gameserver.dev",
		// The process exits right after the manager stops, so handing the
		// lease over immediately is safe and makes rollouts faster.
		LeaderElectionReleaseOnCancel: true,
	})
	if err != nil {
		setupLog.Error(err, "create manager")
		os.Exit(1)
	}

	r := &controller.GameClusterReconciler{
		Client:       mgr.GetClient(),
		Scheme:       mgr.GetScheme(),
		Recorder:     mgr.GetEventRecorder("gameserver-operator"),
		Nodes:        controller.NewHTTPNodeClient(adminPort),
		PollInterval: pollInterval,
	}
	if err := r.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "setup controller")
		os.Exit(1)
	}
	utilruntime.Must(mgr.AddHealthzCheck("healthz", healthz.Ping))
	utilruntime.Must(mgr.AddReadyzCheck("readyz", healthz.Ping))

	setupLog.Info("starting operator")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "manager exited")
		os.Exit(1)
	}
}
