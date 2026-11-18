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

Replay of the public Google 2011 Borg trace: 21 days of 5-minute memory peaks of long-running tasks, the first 7 days for warm-up. Every recommender sets each container's memory limit once an hour and again right after an OOM kill; a peak above the limit is an OOM kill, and a recommender only ever sees usage capped at its own limit. Held-out set: 3,179 tasks, 44,398 scored container-days ([full tables](reports/benchmark.md); how the held-out set was used, including a second run, is [below](#how-the-design-was-chosen-and-how-the-held-out-set-was-used)).

**You get the OOM rate you ask for.**

| budget asked, OOM kills per container-day | 0.1 | 0.03 | 0.01 | 0.003 | 0.001 |
|---|---:|---:|---:|---:|---:|
| OOM kills that happened | 0.062 | 0.027 | 0.0101 | 0.0030 | 0.0007 |
| memory allocated / peak usage | 1.39 | 1.43 | 1.72 | 2.39 | 3.63 |

**It does not save memory.** At the same OOM rate it needs about what the baselines need:

| baseline | its OOM kills per 1,000 container-days | its memory / peak | this recommender at the same OOM rate |
|---|---:|---:|---:|
| VPA 1.8.1 as shipped (p90 + 15%, 250 MiB floor) | 22.2 | 1.461 | 1.491 (+2.0%) |
| KRR `simple`, its default +15% | 16.8 | 1.520 | 1.573 (+3.5%) |
| KRR, interpolated at the budget's own rate for 0.01 and 0.003 per day | 10.1 and 3.0 | | −0.9% and −1.9% |

What it adds is the knob. With VPA or KRR you find the margin that gives an acceptable OOM rate by trial, on your own fleet, and it drifts as the fleet changes; here the rate is the input and the margin is recomputed every hour. For example, moving from VPA's default to a budget of 0.01 per day cut OOM kills by 54% (22.2 to 10.1 per 1,000 container-days) for 18% more memory (1.46 to 1.72 × peak), a trade you choose instead of discover.

Negative results:

- **No memory saving at a matched OOM rate** (above). Against VPA with its floor off, interpolated between its 12 swept settings, the report shows −3% to −9%, but that grid is coarse and its frontier is bumpy, so I don't count it as a saving.
- **Strict budgets are expensive.** 0.001 per day needs 3.6× the peak memory, and at the 0.1 end the realized rate (0.062) is safer than asked, which costs memory.
- **Calibration must be pooled.** Calibrating each container on its own week of surprises missed the budget by 26× (0.01 per day) to 174× (0.001 per day).
- **Per-container scales lost** (next section).

## How it works

Every hour, for every container it manages, the recommender sets memory to

```
limit = base × (1 + q)
```

- **base** is the container's highest hourly peak over the last 14 days. An hour in which it was OOM-killed at limit L counts as a peak of 1.2 × L, because the real peak was above the limit and was never observed (VPA uses the same 1.2 bump).
- **q** is one margin shared by the whole fleet, and it is the only thing the budget sets. For every past hour of every managed container the recommender computes the *surprise* `(peak − base) / base` that the rule would have faced, pools them, and picks q so that a surprise above q has probability `budget / 24` per hour. When the budget is smaller than the pool can resolve (fewer than 30 pooled surprises above q), the tail is extrapolated with an exponential fit to the top 30 surprises.

So it is KRR's rule (a long maximum plus a buffer) with the buffer computed from the OOM rate you ask for, on your own fleet, every hour, instead of picked by hand. In conformal-prediction terms it is split conformal prediction with the calibration set pooled across containers. Pooling is what makes small budgets usable: one container has 168 hourly surprises a week, a fleet of 1,000 has 168,000, so the 1-in-24,000-hour quantile that a budget of 0.001 per day needs is read from data instead of guessed.

The recommendation's lower bound is the size that would carry 10× the budget's risk and the upper bound the size for a tenth of it, so the VPA updater only evicts pods that are either too risky or wasteful enough to be worth a restart.

OOM kills are read from pod status (`lastState.terminated.reason: OOMKilled`) and remembered in a ConfigMap, because the VPA updater evicts exactly the pods that report them.

### How the design was chosen, and how the held-out set was used

The benchmark has two disjoint samples of trace tasks: the **design set** (FNV-1a hash of job/task ≡ 0 mod 16) for every design decision, and the **held-out set** (≡ 1 mod 16) for the results above. In order:

1. **Design search, design set.** The first design was more ambitious and lost: a 24-hour base and a per-container scale (the 10th to 90th percentile spread of peaks), so that spiky containers would get wide margins and flat ones narrow margins. At a budget of 0.001 per day it needed 16× the peak memory. A step-change-robust scale (median change between consecutive hours) needed 4.8×, a plain percentage of a 7-day peak 3.7×. In the kind e2e the spread scale also turned one step from 40 to 100 MiB into a 905 MiB recommendation, because the step itself inflated the spread. The 7-day percentage design was chosen.
2. **Held-out run 1** ([report](reports/heldout-run1-zeros-as-data.md)). VPA with its floor off showed over 1,000 OOM kills per 1,000 container-days whatever its margin. Chasing that anomaly showed that the trace reports 0 for unmeasured windows (12.5% of windows; 13% of tasks are mostly zeros), and that VPA 1.8.1's model ignores every later peak of a 24-hour window that opens with a 0 sample (`ContainerState.addMemorySample`). A running container's working set is never 0, so zero readings are now missing data for every recommender, and tasks with less than 90% real readings are dropped.
3. **Design set again, with the fixed data.** The 7-day design now needed 6% to 21% more memory than KRR at the same OOM rate. KRR's maximum spans 14 days; a 14-day base, tested on the design set only, closed the gap and became the default ([design grid](reports/design/benchmark.md)).
4. **Held-out run 2** ([report](reports/benchmark.md)) is what the Results section shows.

So the held-out set was evaluated twice, and both changes, although each was decided on design-set numbers, trace back to an anomaly first seen in held-out run 1. Run 2 is therefore not fully out of sample. On run 2's data the 7-day design of run 1 needs 1.82× peak memory at 0.01 per day against 1.72× for the 14-day base, and about the same at 0.001 per day (3.66× vs 3.63×); both are rows in the report.

## Install

Needs VPA 1.x (the CRD with `spec.recommenders`; tested with 1.8.1) and a Prometheus that scrapes the kubelets' cAdvisor endpoint (`container_memory_working_set_bytes`), which kube-prometheus-stack does by default. The base looks back 14 days, so Prometheus should keep at least that much (its default retention is 15 days); with less, the base is shorter and the margin grows to compensate.

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
| `--base-windows` / `--history-windows` | `336` / `168` | windows for the base maximum (14 days) and for calibration (7 days) |
| `--min-windows` | `12` | observed windows a container needs before it is sized |
| `--interval` | `1m` | how often to recompute |
| `--state-namespace` / `--state-configmap` | pod namespace / `vpa-oom-budget-state` | where OOM kills are remembered |

A container is sized once it has 12 hours of history, and the fleet needs at least 31 scored windows in its pool. With existing Prometheus history both are immediate; on a fresh Prometheus with one workload the first recommendation takes about two days.

## Tests

- `go test ./...`: the tail fit against an exponential distribution, calibration on a synthetic fleet (1% asked for, 0.6 to 1.6% accepted), OOM handling, the controller against fake Kubernetes, VPA and Prometheus APIs (ownership by `spec.recommenders`, `minAllowed`/`maxAllowed`, `mode: Off`, OOM history surviving pod eviction), and a replay smoke test of the benchmark.
- `e2e/run.sh` (CI job `e2e`): a kind cluster with the real VPA 1.8.1 updater, admission controller and default recommender from the official Helm chart, metrics-server and Prometheus. Time is compressed to 20-second windows, with a budget of 0.18 per day (the same risk per window as 0.001 per day with hourly windows). In CI ([summary](reports/e2e-ci.md)): pods holding 40 MiB were resized from 160 MiB to 42 MiB by the VPA updater and admission controller; when the workload grew to 100 MiB, 7 OOM kills and 61 seconds later both pods ran at 136 MiB with no further restarts; the default recommender kept writing its own VPA and never touched the oom-budget one.

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

The benchmark uses the Google cluster-usage trace `clusterdata-2011-2` (Reiss, Wilkes and Hellerstein, *Google cluster-usage traces: format + schema*, 2011), licensed [CC-BY 4.0](https://creativecommons.org/licenses/by/4.0/). The two files in `data/` are derived from its `task_usage` table by `cmd/tracegen`: the `maximum memory usage` field (peak per 5-minute window) of tasks that were running when the trace starts and have data in at least 90% of the first 21 days, quantized to 1/65,534 of each task's maximum. Each of the 365 parts read is checked against the bucket's `SHA256SUM`, and the output is byte-reproducible:

```bash
go run ./cmd/tracegen -candidate-residue 0 -out data/google2011-mem.bin.gz        # design set, 3,666 tasks
go run ./cmd/tracegen -candidate-residue 1 -out data/google2011-mem-test.bin.gz   # held-out set, 3,673 tasks
(cd data && sha256sum -c SHA256SUMS)
go run ./cmd/bench                                                                  # reports/benchmark.{md,json}
go run ./cmd/bench -data data/google2011-mem.bin.gz -set "design set" -grid -out reports/design
```

The benchmark treats zero readings as missing and then keeps tasks with at least 90% real readings (3,164 and 3,179 tasks). Trace memory is normalized to the largest machine; the benchmark takes that machine as 64 GiB, which only matters for VPA's 250 MiB floor, 100 MB minimum OOM bump and 10 MB first histogram bucket. Borg tasks are not Kubernetes pods: the replay assumes a pod's limit can be changed every hour (as with VPA's in-place updates) and that an OOM-killed task's later usage does not depend on the kill.

## License

Apache 2.0.
