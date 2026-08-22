// Command operator runs the AgentSession controller-manager.
package main

import (
	"flag"
	"os"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	agentsv1alpha1 "github.com/prav-j/dark-factory/api/v1alpha1"
	"github.com/prav-j/dark-factory/internal/operator"
)

func main() {
	var metricsAddr string
	var sandboxImage string
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8083", "metrics bind address")
	flag.StringVar(&sandboxImage, "sandbox-image",
		os.Getenv("SANDBOX_IMAGE"), "session sandbox image")
	flag.Parse()

	logger := zap.New()
	ctrl.SetLogger(logger)

	if sandboxImage == "" {
		logger.Error(nil, "SANDBOX_IMAGE or -sandbox-image is required")
		os.Exit(1)
	}

	scheme, err := agentsv1alpha1.NewScheme()
	if err != nil {
		logger.Error(err, "unable to build scheme")
		os.Exit(1)
	}
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress: metricsAddr,
		},
	})
	if err != nil {
		logger.Error(err, "unable to start manager")
		os.Exit(1)
	}

	if err := operator.NewReconciler(mgr.GetClient(), sandboxImage).SetupWithManager(mgr); err != nil {
		logger.Error(err, "unable to create controller")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		logger.Error(err, "health check setup failed")
		os.Exit(1)
	}

	logger.Info("starting operator")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		logger.Error(err, "problem running operator")
		os.Exit(1)
	}
}
