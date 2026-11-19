package main

import (
	"flag"
	"os"
	"time"

	maintenancev1 "github.com/smartdrain/smartdrain/api/v1alpha1"
	"github.com/smartdrain/smartdrain/internal/controller"
	"github.com/smartdrain/smartdrain/internal/maintenance"
	"github.com/smartdrain/smartdrain/internal/observability"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

// main 装配标准 manager；业务协议位于 internal，不放在启动入口。
func main() {
	var metricsAddr, probeAddr, namespace string
	var leader, allowDelete bool
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "Metrics address")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "Health probe address")
	flag.StringVar(&namespace, "namespace", "smartdrain-system", "Lease namespace")
	flag.BoolVar(&leader, "leader-elect", true, "Use manager leader election")
	flag.BoolVar(&allowDelete, "allow-direct-delete", false, "Enable explicitly requested drain-compatible Delete actions")
	options := zap.Options{Development: false}
	options.BindFlags(flag.CommandLine)
	flag.Parse()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&options)))
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(maintenancev1.AddToScheme(scheme))
	config := ctrl.GetConfigOrDie()
	config.Timeout = 15 * time.Second
	config.UserAgent = "smartdrain/v0.1.0"
	mgr, err := ctrl.NewManager(config, ctrl.Options{Scheme: scheme, Metrics: metricsserver.Options{BindAddress: metricsAddr}, HealthProbeBindAddress: probeAddr, LeaderElection: leader, LeaderElectionID: "smartdrain-manager", LeaderElectionNamespace: namespace})
	if err != nil {
		ctrl.Log.Error(err, "create manager")
		os.Exit(1)
	}
	cs, err := kubernetes.NewForConfig(config)
	if err != nil {
		ctrl.Log.Error(err, "create clientset")
		os.Exit(1)
	}
	resources, err := cs.Discovery().ServerResourcesForGroupVersion("v1")
	if err != nil {
		ctrl.Log.Error(err, "discover Eviction support")
		os.Exit(1)
	}
	supported := false
	for _, resource := range resources.APIResources {
		if resource.Name == "pods/eviction" && resource.Group == "policy" && resource.Version == "v1" {
			supported = true
		}
	}
	r := &controller.NodeMaintenanceReconciler{Client: mgr.GetClient(), APIReader: mgr.GetAPIReader(), Scheme: scheme, Namespace: namespace, Executor: maintenance.KubernetesExecutor{Client: cs}, Recorder: mgr.GetEventRecorderFor("smartdrain"), AllowDelete: allowDelete, EvictionSupported: supported}
	metrics.Registry.MustRegister(observability.NewTaskCollector(mgr.GetClient()))
	if err := r.SetupWithManager(mgr); err != nil {
		ctrl.Log.Error(err, "setup controller")
		os.Exit(1)
	}
	utilruntime.Must(mgr.AddHealthzCheck("healthz", healthz.Ping))
	utilruntime.Must(mgr.AddReadyzCheck("readyz", healthz.Ping))
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		ctrl.Log.Error(err, "manager stopped")
		os.Exit(1)
	}
}
