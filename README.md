# vpa-recommender-oom-budget

**The Vertical Pod Autoscaler sizes memory at the 90th percentile of daily peaks plus 15%, whatever OOM-kill rate that turns out to give. This recommender lets you state the OOM rate instead ("at most 1 OOM kill per 1,000 container-days") and sizes each container to meet it.**

It is an alternative recommender for the [Kubernetes Vertical Pod Autoscaler](https://github.com/kubernetes/autoscaler/tree/master/vertical-pod-autoscaler): VPAs opt in with `spec.recommenders`, the recommender writes their `status.recommendation`, and the stock VPA updater and admission controller apply it. VPAs that don't name it stay with the default recommender.

```yaml
apiVersion: autoscaling.k8s.io/v1
kind: VerticalPodAutoscaler
metadata:
  name: api
spec:
  recommenders:
    - name: oom-budget
  targetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: api
  updatePolicy:
    updateMode: Recreate
  resourcePolicy:
    containerPolicies:
      - containerName: "*"
        controlledResources: [memory]   # memory only; pair it with an HPA on CPU
```

## Results

The held-out benchmark is being generated. Design-set results (the design search) are in [`reports/design/benchmark.md`](reports/design/benchmark.md).

## How it works

Every hour, for every container it manages, the recommender sets memory to

```
limit = base × (1 + q)
```

- **base** is the container's highest hourly peak over the last 7 days. An hour in which it was OOM-killed at limit L counts as a peak of 1.2 × L, because the real peak was above the limit and was never observed (VPA uses the same 1.2 bump).
- **q** is one margin shared by the whole fleet, and it is the only thing the budget sets. For every past hour of every managed container the recommender computes the *surprise* `(peak − base) / base` that the rule would have faced, pools them, and picks q so that a surprise above q has probability `budget / 24` per hour. When the budget is smaller than the pool can resolve (fewer than 30 pooled surprises above q), the tail is extrapolated with an exponential fit to the top 30 surprises.

So it is KRR's rule (a long maximum plus a buffer) with the buffer computed from the OOM rate you ask for, on your own fleet, every hour, instead of picked by hand. In conformal-prediction terms it is split conformal prediction with the calibration set pooled across containers. Pooling is what makes small budgets usable: one container has 168 hourly surprises a week, a fleet of 1,000 has 168,000, so the 1-in-24,000-hour quantile that a budget of 0.001 per day needs is read from data instead of guessed.

The recommendation's lower bound is the size that would carry 10× the budget's risk and the upper bound the size for a tenth of it, so the VPA updater only evicts pods that are either too risky or wasteful enough to be worth a restart.

OOM kills are read from pod status (`lastState.terminated.reason: OOMKilled`) and remembered in a ConfigMap, because the VPA updater evicts exactly the pods that report them.

## Install

Needs VPA 1.x (the CRD with `spec.recommenders`; tested with 1.8.1) and a Prometheus that scrapes the kubelets' cAdvisor endpoint (`container_memory_working_set_bytes`), which kube-prometheus-stack does by default.

```bash
kubectl apply -f deploy/recommender.yaml
```

Then set `--prometheus-url` in the Deployment to your Prometheus. Flags:

| flag | default | meaning |
|---|---|---|
| `--budget` | `0.001` | allowed OOM kills per container-day |
| `--prometheus-url` | `http://prometheus.monitoring.svc:9090` | Prometheus with cAdvisor metrics |
| `--recommender-name` | `oom-budget` | name VPAs use in `spec.recommenders` |
| `--window` | `1h` | length of one peak window |
| `--base-windows` / `--history-windows` | `168` / `168` | windows for the base maximum and for calibration |
| `--min-windows` | `12` | observed windows a container needs before it is sized |
| `--interval` | `1m` | how often to recompute |
| `--state-namespace` / `--state-configmap` | pod namespace / `vpa-oom-budget-state` | where OOM kills are remembered |

A container is sized once it has 12 hours of history, and the fleet needs at least 31 scored windows in its pool. With existing Prometheus history both are immediate; on a fresh Prometheus with one workload the first recommendation takes about two days.

## Tests

- `go test ./...`: the tail fit against an exponential distribution, calibration on a synthetic fleet (1% asked for, 0.6 to 1.6% accepted), OOM handling, the controller against fake Kubernetes, VPA and Prometheus APIs (ownership by `spec.recommenders`, `minAllowed`/`maxAllowed`, `mode: Off`, OOM history surviving pod eviction), and a replay smoke test of the benchmark.
- `e2e/run.sh` (CI job `e2e`): a kind cluster with the real VPA 1.8.1 updater, admission controller and default recommender from the official Helm chart, metrics-server and Prometheus. Time is compressed to 20-second windows.

## Limits

- **Memory only.** The recommender writes no CPU recommendation, and a VPA can name only one recommender, so CPU is left to something else (`controlledResources: [memory]` plus an HPA on CPU is the pattern VPA's own docs suggest for combining the two).
- **The budget is a fleet average, not a per-container promise.** One margin is calibrated on the pooled surprises of all managed containers. A container that grows faster than the fleet (leaks, step changes) gets more than its share of OOM kills, and a calm one gets more memory than it needs.
- **No exchangeability, no guarantee.** Conformal coverage holds for exchangeable data; memory usage drifts, so the calibration is measured in the benchmark, not proven.
- **Limit = request.** The budget is computed for a container whose memory limit equals its request. With a larger limit, VPA scales the limit in proportion and the OOM risk is lower than the budget; node-pressure evictions, which depend on the request, are not modelled.
- **Hourly reaction.** The recommendation follows hourly windows. A spike that arrives within the first minutes of a container's life (JVM warm-up, cache fill) is handled only by the OOM bump after the first kill.
- **Pod matching by name.** History is matched by the pod-name pattern of the target's controller (`<deployment>-<hash>-<id>`, `<statefulset>-<n>`, `<daemonset>-<id>`), so it survives rollouts; workloads managed by other controllers are not supported.
- **Prometheus load.** Every cycle runs one range query over the full history per managed container. That is fine for hundreds of containers; for thousands, finished windows should be cached.
- **No cold start.** Before 31 windows of history exist across the fleet there is no recommendation at all.

## Prior art

The statistics are standard; what this repository adds is wiring them into a VPA recommender and measuring the result against the VPA's own code.

- **VPA's default recommender** ([source](https://github.com/kubernetes/autoscaler/tree/master/vertical-pod-autoscaler/pkg/recommender)): memory = 90th percentile of per-day peaks in a decaying histogram (24-hour half-life), +15%, bumped by 20% or 100 MB after an OOM. The benchmark runs this code, imported as a Go module, not a reimplementation.
- **Autopilot** (Rzadca et al., *Autopilot: workload autoscaling at Google*, EuroSys 2020): Borg's production recommender, which picks among moving-window recommenders with an ML cost model. It is internal to Google; this repository uses its public trace instead.
- **Robusta KRR** ([robusta-dev/krr](https://github.com/robusta-dev/krr)): a CLI that recommends memory as the 14-day maximum + 15%, with an optional bump to the highest OOM-killed limit + 25%. Its `simple` strategy is reimplemented in the benchmark.
- **Other alternative VPA recommenders**: [openshift/predictive-vpa-recommenders](https://github.com/openshift/predictive-vpa-recommenders) (time-series forecasting, last updated April 2023) and [sustainable-computing-io/clever](https://github.com/sustainable-computing-io/clever) (energy, archived). Neither targets an OOM rate. [Goldilocks](https://github.com/FairwindsOps/goldilocks) shows VPA recommendations; it does not compute its own.
- **Conformal prediction**: Vovk, Gammerman and Shafer, *Algorithmic Learning in a Random World* (2005); split conformal prediction, Lei et al., *Distribution-Free Predictive Inference for Regression*, JASA 2018; normalized nonconformity scores, Papadopoulos, Vovk and Gammerman, JAIR 2011. Its coverage guarantee assumes exchangeable data, which memory time series are not; the benchmark measures how far that matters. Adaptive conformal inference (Gibbs and Candès, NeurIPS 2021) is the known fix under drift and is not implemented here.
- **Exponential tail fit**: peaks over threshold with shape 0 (Coles, *An Introduction to Statistical Modeling of Extreme Values*, 2001).

## Data

The benchmark uses the Google cluster-usage trace `clusterdata-2011-2` (Reiss, Wilkes and Hellerstein, *Google cluster-usage traces: format + schema*, 2011), licensed [CC-BY 4.0](https://creativecommons.org/licenses/by/4.0/). `data/google2011-mem.bin.gz` is derived from its `task_usage` table by `cmd/tracegen`: the `maximum memory usage` field of a sample of tasks, quantized to 1/65,534 of each task's maximum.

## License

Apache 2.0.
