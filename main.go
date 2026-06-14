package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"os"
	"time"

	"github.com/go-logr/logr"

	"github.com/krateoplatformops/core-provider/internal/controllers/compositiondefinitions"
	"github.com/krateoplatformops/core-provider/internal/tools/loghandler"
	"github.com/krateoplatformops/core-provider/internal/tools/pluralizer"
	"github.com/krateoplatformops/plumbing/env"
	"github.com/krateoplatformops/plumbing/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/config"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/krateoplatformops/core-provider/apis"
	"github.com/krateoplatformops/provider-runtime/pkg/controller"
	"github.com/krateoplatformops/provider-runtime/pkg/logging"
	"github.com/krateoplatformops/provider-runtime/pkg/ratelimiter"
	"github.com/krateoplatformops/provider-runtime/pkg/telemetry"

	"github.com/stoewer/go-strcase"
)

const (
	providerName              = "Core"
	defaultOtelExportInterval = 30 * time.Second
)

func main() {
	envVarPrefix := fmt.Sprintf("%s_PROVIDER", strcase.UpperSnakeCase(providerName))

	debug := flag.Bool("debug", env.Bool(fmt.Sprintf("%s_DEBUG", envVarPrefix), false), "Run with debug logging.")
	syncPeriod := flag.Duration("sync", env.Duration(fmt.Sprintf("%s_SYNC", envVarPrefix), time.Hour*1), "Controller manager sync period such as 300ms, 1.5h, or 2h45m")
	pollInterval := flag.Duration("poll", env.Duration(fmt.Sprintf("%s_POLL_INTERVAL", envVarPrefix), time.Minute*3), "Poll interval controls how often an individual resource should be checked for drift.")
	maxReconcileRate := flag.Int("max-reconcile-rate", env.Int(fmt.Sprintf("%s_MAX_RECONCILE_RATE", envVarPrefix), 5), "The global maximum rate per second at which resources may checked for drift from the desired state.")
	leaderElection := flag.Bool("leader-election", env.Bool(fmt.Sprintf("%s_LEADER_ELECTION", envVarPrefix), false), "Use leader election for the controller manager.")
	metricsEnabled := flag.Bool("otel-enabled", env.Bool("OTEL_ENABLED", false), "Enable OTLP metrics export for provider-runtime telemetry.")
	metricsServiceName := flag.String("otel-service-name", fmt.Sprintf("%s-provider", strcase.KebabCase(providerName)), "The service name attached to exported OTLP metrics.")
	metricsExportInterval := flag.Duration("otel-export-interval", env.Duration("OTEL_EXPORT_INTERVAL", defaultOtelExportInterval), "The interval used to export OTLP metrics.")
	maxErrorRetryInterval := flag.Duration("max-error-retry-interval", env.Duration(fmt.Sprintf("%s_MAX_ERROR_RETRY_INTERVAL", envVarPrefix), 1*time.Minute), "The maximum interval between retries when an error occurs. This should be less than the half of the poll interval.")
	minErrorRetryInterval := flag.Duration("min-error-retry-interval", env.Duration(fmt.Sprintf("%s_MIN_ERROR_RETRY_INTERVAL", envVarPrefix), 1*time.Second), "The minimum interval between retries when an error occurs. This should be less than max-error-retry-interval.")

	flag.Parse()

	log.Default().SetOutput(os.Stderr)

	logLevel := slog.LevelInfo
	if *debug {
		logLevel = slog.LevelDebug
	}

	// Emit logs as one JSON object per line (RFC3339Nano UTC `timestamp` plus a
	// `service` attribute) so they can be ingested by logs-ingester. See
	// docs/log-ingester-compatibility.md.
	log := logging.NewLogrLogger(logr.FromSlogHandler(loghandler.NewJSONHandler(logLevel, os.Stderr)))

	// Set the logger for controller-runtime. This only has to log in INFO level as all debug logs are handled by our logger above.
	ctrl.SetLogger(logr.FromSlogHandler(loghandler.NewJSONHandler(slog.LevelInfo, os.Stderr)))

	log.Debug("Starting",
		"sync-period", syncPeriod.String(),
		"poll-interval", pollInterval.String(),
		"max-reconcile-rate", *maxReconcileRate,
		"leader-election", *leaderElection,
		"max-error-retry-interval", maxErrorRetryInterval.String(),
		"min-error-retry-interval", minErrorRetryInterval.String(),
		"otel-enabled", *metricsEnabled,
		"otel-service-name", *metricsServiceName,
		"otel-export-interval", metricsExportInterval.String())

	telemetryEnabled := *metricsEnabled
	telemetryExportInterval := *metricsExportInterval

	telemetryMetrics, telemetryShutdown, err := telemetry.Setup(context.Background(), log, telemetry.Config{
		Enabled:        telemetryEnabled,
		ServiceName:    *metricsServiceName,
		ExportInterval: telemetryExportInterval,
	})
	if err != nil {
		log.Error(err, "Cannot initialize OpenTelemetry metrics")
		os.Exit(1)
	}
	defer func() {
		if err := telemetryShutdown(context.Background()); err != nil {
			log.Error(err, "Cannot shutdown OpenTelemetry metrics")
		}
	}()

	cfg, err := ctrl.GetConfig()
	if err != nil {
		log.Error(err, "Cannot get API server rest config")
		os.Exit(1)
	}

	// core-provider hosts no admission webhooks (None conversion + an in-apiserver
	// MutatingAdmissionPolicy), so there is no webhook server or serving certificate.
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		LeaderElection:   *leaderElection,
		LeaderElectionID: fmt.Sprintf("leader-election-%s-provider", strcase.KebabCase(providerName)),
		Cache: cache.Options{
			SyncPeriod: syncPeriod,
		},
		Metrics: metricsserver.Options{
			BindAddress: ":8080",
		},
		Controller: config.Controller{
			UsePriorityQueue: ptr.To(true),
		},
	})
	if err != nil {
		log.Error(err, "Cannot create controller manager")
		os.Exit(1)
	}

	o := controller.Options{
		Logger:                  log,
		MaxConcurrentReconciles: *maxReconcileRate,
		PollInterval:            *pollInterval,
		GlobalRateLimiter:       ratelimiter.NewGlobalExponential(*minErrorRetryInterval, *maxErrorRetryInterval),
		UsePriorityQueue:        ptr.To(true),
		QueueWaitRecorder:       telemetryMetrics,
	}

	if err := apis.AddToScheme(mgr.GetScheme()); err != nil {
		log.Error(err, "Cannot add APIs to scheme")
		os.Exit(1)
	}
	if err := compositiondefinitions.Setup(mgr, compositiondefinitions.Options{
		ControllerOptions: o,
		Metrics:           telemetryMetrics,
		Pluralizer:        pluralizer.New(false),
	}); err != nil {
		log.Error(err, "Cannot setup controllers")
		os.Exit(1)
	}
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		log.Error(err, "Cannot start controller manager")
		os.Exit(1)
	}
}
