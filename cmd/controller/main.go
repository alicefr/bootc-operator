// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	bootcv1alpha1 "github.com/bootc-dev/bootc-operator/api/v1alpha1"
	"github.com/bootc-dev/bootc-operator/internal/config"
	"github.com/bootc-dev/bootc-operator/internal/controller"
	"github.com/bootc-dev/bootc-operator/internal/pullsecret"
	"github.com/bootc-dev/bootc-operator/internal/registry"
	"github.com/bootc-dev/bootc-operator/internal/version"
)

const namespacePath = "/var/run/secrets/kubernetes.io/serviceaccount/namespace"

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func detectNamespace() string {
	if ns := os.Getenv("POD_NAMESPACE"); ns != "" {
		return ns
	}
	if data, err := os.ReadFile(namespacePath); err == nil {
		return strings.TrimSpace(string(data))
	}
	return ""
}

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(bootcv1alpha1.AddToScheme(scheme))
}

func main() {
	var enableLeaderElection bool
	var showVersion bool
	var probeAddr string
	flag.StringVar(
		&probeAddr,
		"health-probe-bind-address",
		":8081",
		"The address the probe endpoint binds to.",
	)
	// These flags are read through config.SettingsFromFlags.
	flag.Duration(
		config.TagResolutionIntervalFlag,
		time.Duration(bootcv1alpha1.DefaultTagResolutionPeriodSeconds)*time.Second,
		"How often to re-resolve tag-based image refs.",
	)
	flag.Bool(
		config.AllowInsecureRegistryFlag,
		false,
		"Allow falling back to HTTP when resolving tag-based image refs against registries that do not serve TLS.",
	)
	flag.String(
		config.PullSecretNamespacesFlag,
		"",
		"Comma-separated extra namespaces that pools may reference pull secrets from. "+
			"The operator namespace is always allowed.",
	)
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.BoolVar(&showVersion, "version", false, "Print version and exit.")
	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()
	if showVersion {
		fmt.Println(version.String())
		return
	}
	explicit := config.ExplicitFlags(flag.CommandLine)

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	setupLog.Info(
		"starting",
		"component",
		"controller",
		"version",
		version.Version,
		"commit",
		version.GitCommit,
	)

	ctx := ctrl.SetupSignalHandler()
	restConfig := ctrl.GetConfigOrDie()
	operatorConfig, err := config.Load(ctx, restConfig, scheme)
	if err != nil {
		setupLog.Error(err, "Failed to load operator configuration")
		os.Exit(1)
	}
	// The CLI values are what applies when the configuration does not set a
	// field, including after it is deleted.
	fallback, err := config.SettingsFromFlags(flag.CommandLine)
	if err != nil {
		setupLog.Error(err, "Failed to read CLI settings")
		os.Exit(1)
	}
	operatorNamespace := detectNamespace()
	if operatorNamespace == "" {
		setupLog.Error(nil, "Operator namespace unknown; set POD_NAMESPACE")
		os.Exit(1)
	}
	if _, err := controller.AllowedPullSecretNamespaces(
		fallback.PullSecretNamespaces,
		operatorNamespace,
	); err != nil {
		setupLog.Error(err, "Invalid pull secret namespace configuration")
		os.Exit(1)
	}
	settings := config.Resolve(fallback, operatorConfig)
	pullSecretNamespaces, err := controller.AllowedPullSecretNamespaces(
		settings.PullSecretNamespaces,
		operatorNamespace,
	)
	if err != nil {
		setupLog.Error(err, "Invalid pull secret namespace configuration")
		os.Exit(1)
	}
	setupLog.Info("Pull secret namespaces", "namespaces", pullSecretNamespaces)

	config.LogSource(
		setupLog,
		operatorConfig,
		explicit,
		config.AllowInsecureRegistryFlag,
		config.TagResolutionIntervalFlag,
		config.PullSecretNamespacesFlag,
	)

	mgr, err := ctrl.NewManager(restConfig, ctrl.Options{
		Scheme:                 scheme,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "bootc-operator.bootc.dev",
		Cache: cache.Options{
			// Only the aggregated Secret is read through the manager. The
			// source secrets in the allowed namespaces are read through
			// the SourceCache.
			ByObject: controller.SecretCacheByObject([]string{operatorNamespace}),
		},
	})
	if err != nil {
		setupLog.Error(err, "Failed to start manager")
		os.Exit(1)
	}

	if err := pullsecret.IndexPoolPullSecretRef(
		context.Background(),
		mgr.GetFieldIndexer(),
	); err != nil {
		setupLog.Error(err, "Failed to index pools by pull secret")
		os.Exit(1)
	}

	sourceSecrets, err := pullsecret.NewSourceCache(
		mgr,
		pullSecretNamespaces,
		ctrl.Log.WithName("pullsecret"),
	)
	if err != nil {
		setupLog.Error(err, "Failed to create pull secret cache")
		os.Exit(1)
	}

	kubeClient, err := kubernetes.NewForConfig(mgr.GetConfig())
	if err != nil {
		setupLog.Error(err, "Failed to create Kubernetes clientset")
		os.Exit(1)
	}

	poolReconciler := &controller.BootcNodePoolReconciler{
		Client:                mgr.GetClient(),
		Scheme:                mgr.GetScheme(),
		KubeClient:            kubeClient,
		EventNamespace:        operatorNamespace,
		TagResolver:           registry.NewGGCRResolver(settings.AllowInsecureRegistry),
		TagResolutionInterval: settings.TagResolutionInterval,
		SourceSecrets:         sourceSecrets,
	}
	if err := poolReconciler.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "bootcnodepool")
		os.Exit(1)
	}

	configWatcher := config.NewWatcher(
		fallback,
		operatorConfig,
		mgr.GetCache(),
		ctrl.Log.WithName("config"),
	)
	configStatus := &controller.ConfigStatusReconciler{
		Client:     mgr.GetClient(),
		Config:     configWatcher,
		Namespaces: sourceSecrets,
	}
	if err := configStatus.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "bootcoperatorconfig")
		os.Exit(1)
	}
	configWatcher.OnChange(func(prev, next config.State) {
		configStatus.Notify()
		poolReconciler.ApplySettings(prev.Settings, next.Settings)
		if slices.Equal(prev.Settings.PullSecretNamespaces, next.Settings.PullSecretNamespaces) {
			return
		}
		namespaces, err := controller.AllowedPullSecretNamespaces(
			next.Settings.PullSecretNamespaces,
			operatorNamespace,
		)
		if err != nil {
			setupLog.Error(err, "Keeping the current pull secret namespaces")
			return
		}
		setupLog.Info("Pull secret namespaces", "namespaces", namespaces)
		sourceSecrets.SetNamespaces(namespaces)
	})
	if err := mgr.Add(configWatcher); err != nil {
		setupLog.Error(err, "Failed to add operator configuration watcher")
		os.Exit(1)
	}

	if err := (&pullsecret.PullSecretAggregator{
		Client:    mgr.GetClient(),
		Secrets:   sourceSecrets,
		Namespace: operatorNamespace,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "pullsecretaggregator")
		os.Exit(1)
	}

	// +kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "Failed to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "Failed to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("Starting manager")
	if err := mgr.Start(ctx); err != nil {
		setupLog.Error(err, "Failed to run manager")
		os.Exit(1)
	}
}
