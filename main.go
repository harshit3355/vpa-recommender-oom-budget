// Command vpa-recommender-oom-budget is an alternative Vertical Pod Autoscaler
// recommender that sizes container memory for an OOM-kill budget. VPAs opt in
// with spec.recommenders: [{name: oom-budget}].
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	promapi "github.com/prometheus/client_golang/api"
	promv1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	vpaclient "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/client/clientset/versioned"

	"github.com/harshit3355/vpa-recommender-oom-budget/internal/budget"
	"github.com/harshit3355/vpa-recommender-oom-budget/internal/controller"
)

func main() {
	cfg := budget.Default
	name := flag.String("recommender-name", "oom-budget", "name VPAs use in spec.recommenders")
	promURL := flag.String("prometheus-url", "http://prometheus.monitoring.svc:9090", "Prometheus that scrapes cAdvisor (container_memory_working_set_bytes)")
	kubeconfig := flag.String("kubeconfig", "", "kubeconfig path; in-cluster config when empty")
	interval := flag.Duration("interval", time.Minute, "how often to recompute")
	step := flag.Duration("window", time.Hour, "length of one peak window")
	stateNS := flag.String("state-namespace", envOr("POD_NAMESPACE", "kube-system"), "namespace of the OOM history ConfigMap")
	stateName := flag.String("state-configmap", "vpa-oom-budget-state", "name of the OOM history ConfigMap")
	flag.Float64Var(&cfg.BudgetPerDay, "budget", cfg.BudgetPerDay, "allowed OOM kills per container-day")
	flag.IntVar(&cfg.BaseWindows, "base-windows", cfg.BaseWindows, "windows whose highest peak is the base")
	flag.IntVar(&cfg.HistoryWindows, "history-windows", cfg.HistoryWindows, "windows of history for calibration")
	flag.IntVar(&cfg.MinWindows, "min-windows", cfg.MinWindows, "observed windows a container needs before it is sized")
	flag.Parse()
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	rc, err := restConfig(*kubeconfig)
	if err != nil {
		log.Error("kubernetes config", "err", err)
		os.Exit(1)
	}
	pc, err := promapi.NewClient(promapi.Config{Address: *promURL})
	if err != nil {
		log.Error("prometheus client", "err", err)
		os.Exit(1)
	}
	cfg.WindowsPerDay = float64(24*time.Hour) / float64(*step)
	kube := kubernetes.NewForConfigOrDie(rc)
	c := &controller.Controller{
		Kube: kube,
		VPA:  vpaclient.NewForConfigOrDie(rc),
		Prom: promv1.NewAPI(pc),
		Name: *name, Cfg: cfg, Step: *step, Log: log, Now: time.Now,
		OOMs: &controller.OOMStore{Kube: kube, Namespace: *stateNS, Name: *stateName},
	}
	log.Info("starting", "recommender", *name, "budget_per_container_day", cfg.BudgetPerDay, "window", step.String())

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	t := time.NewTicker(*interval)
	defer t.Stop()
	for {
		if err := c.RunOnce(ctx); err != nil {
			log.Error("run failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func restConfig(path string) (*rest.Config, error) {
	if path == "" {
		return rest.InClusterConfig()
	}
	return clientcmd.BuildConfigFromFlags("", path)
}
