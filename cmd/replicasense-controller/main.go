package main

import (
	"context"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/canberkturan/keda-replica-sense/internal/config"
	"github.com/canberkturan/keda-replica-sense/internal/controller"
	"github.com/canberkturan/keda-replica-sense/internal/observability"
	"github.com/canberkturan/keda-replica-sense/internal/postgres"
	kedav1alpha1 "github.com/kedacore/keda/v2/apis/keda/v1alpha1"
)

func main() {
	ctrl.SetLogger(zap.New(zap.UseDevMode(false)))
	log := ctrl.Log.WithName("replicasense-controller")

	configuration, err := config.LoadController(os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	pool, err := pgxpool.New(context.Background(), configuration.DatabaseURL)
	if err != nil {
		log.Error(err, "create PostgreSQL pool")
		os.Exit(1)
	}
	defer pool.Close()
	if err := pool.Ping(context.Background()); err != nil {
		log.Error(err, "connect to PostgreSQL")
		os.Exit(1)
	}

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		log.Error(err, "add Kubernetes types to scheme")
		os.Exit(1)
	}
	if err := kedav1alpha1.AddToScheme(scheme); err != nil {
		log.Error(err, "add KEDA types to scheme")
		os.Exit(1)
	}

	restConfig, err := ctrl.GetConfig()
	if err != nil {
		log.Error(err, "load Kubernetes configuration")
		os.Exit(1)
	}
	manager, err := ctrl.NewManager(restConfig, ctrl.Options{
		Scheme:                  scheme,
		Metrics:                 metricsserver.Options{BindAddress: configuration.MetricsBindAddress},
		HealthProbeBindAddress:  configuration.HealthProbeBindAddress,
		LeaderElection:          configuration.LeaderElection,
		LeaderElectionID:        configuration.LeaderElectionID,
		LeaderElectionNamespace: configuration.LeaderElectionNamespace,
	})
	if err != nil {
		log.Error(err, "create manager")
		os.Exit(1)
	}

	triggerMetrics := observability.NewScaledObjectTriggerMetrics(ctrlmetrics.Registry)
	reconciler := controller.ScaledObjectReconciler{
		Client: manager.GetClient(),
		Processor: controller.Processor{
			ParserOptions: configuration.Parser,
			Sink:          controller.RepositorySink{Repository: postgres.NewWorkloadRepository(pool)},
			TriggerConfig: triggerMetrics,
		},
	}
	if err := reconciler.SetupWithManager(manager); err != nil {
		log.Error(err, "set up ScaledObject controller")
		os.Exit(1)
	}
	if err := manager.Add(controller.TrainerJobCleaner{
		Reader:    manager.GetAPIReader(),
		Writer:    manager.GetClient(),
		Namespace: configuration.SystemNamespace,
		Interval:  configuration.TrainerJobCleanupInterval,
		Log:       log.WithName("trainer-job-cleanup"),
	}); err != nil {
		log.Error(err, "set up trainer Job cleanup")
		os.Exit(1)
	}
	if err := manager.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		log.Error(err, "add health check")
		os.Exit(1)
	}
	if err := manager.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		log.Error(err, "add readiness check")
		os.Exit(1)
	}

	log.Info("starting controller", "clusterID", configuration.Parser.ClusterID)
	if err := manager.Start(ctrl.SetupSignalHandler()); err != nil {
		log.Error(err, "run manager")
		os.Exit(1)
	}
}
