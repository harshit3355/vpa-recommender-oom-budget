#!/usr/bin/env bash
# End-to-end test on a throwaway kind cluster with the real VPA 1.8.1 (updater,
# admission controller and default recommender from the official Helm chart),
# metrics-server, Prometheus scraping cAdvisor, and this recommender:
#   1. sizing: memhog holds 40 MiB but starts with 160 MiB; the VPA updater and
#      admission controller must resize its pods to the oom-budget target;
#   2. OOM: memhog then needs 100 MiB; its pods get OOM-killed, the recommender
#      must raise the target after the kills, and the pods must end up running
#      with enough memory and no further restarts;
#   3. coexistence: the default VPA recommender must keep writing the plain VPA
#      and never the memhog one.
# Time is compressed: 20-second peak windows instead of hours, so the fresh
# Prometheus holds enough windows for calibration after about 10 minutes. The
# budget of 0.18 per day gives the same risk per window as 0.001 per day with
# hourly windows (0.001 x 4,320 / 24).
# Needs docker, kind, kubectl, helm and go. KEEP_CLUSTER=1 keeps the cluster.
set -euo pipefail
cd "$(dirname "$0")/.."

CLUSTER=${CLUSTER:-oom-budget-e2e}
OUT=${OUT:-reports/e2e}
VPA_VERSION=1.8.1
VPA_CHART=https://github.com/kubernetes/autoscaler/releases/download/vertical-pod-autoscaler-chart-0.17.0/vertical-pod-autoscaler-0.17.0.tgz
METRICS_SERVER=v0.9.1
work=$(mktemp -d)
mkdir -p "$OUT"
log() { printf '[%s] %s\n' "$(date -u +%H:%M:%S)" "$*" | tee -a "$OUT/log.txt"; }
cleanup() {
    local rc=$?
    [[ -n "${watcher:-}" ]] && kill "$watcher" 2>/dev/null || true
    if ((rc != 0)) && kubectl cluster-info >/dev/null 2>&1; then
        log "FAILED (exit $rc); diagnostics:"
        kubectl get pods -A -o wide || true
        kubectl get vpa -o yaml | tail -80 || true
        kubectl -n kube-system logs deploy/vpa-oom-budget-recommender --tail 60 || true
        kubectl -n kube-system logs -l app.kubernetes.io/component=updater --tail 40 || true
    fi
    if [[ "${KEEP_CLUSTER:-0}" != 1 ]]; then kind delete cluster --name "$CLUSTER" >/dev/null 2>&1 || true; fi
    rm -rf "$work"
}
trap cleanup EXIT

log "building the recommender and memhog images"
mkdir -p "$work/dist"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o "$work/dist/vpa-recommender-oom-budget-linux-amd64" .
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o "$work/memhog" ./e2e/memhog
docker build -q --build-arg TARGETARCH=amd64 -t vpa-oom-budget:e2e -f Dockerfile "$work"
docker build -q -t memhog:e2e -f - "$work" <<'EOF'
FROM scratch
COPY memhog /memhog
USER 65532
ENTRYPOINT ["/memhog"]
EOF

log "creating kind cluster $CLUSTER"
kind create cluster --name "$CLUSTER" --wait 180s
kind load docker-image --name "$CLUSTER" vpa-oom-budget:e2e memhog:e2e
# Preload third-party images from the host: registry access from inside kind
# fails on some WSL setups. If loading fails, the node pulls them itself.
for img in prom/prometheus:v3.5.0 registry.k8s.io/metrics-server/metrics-server:$METRICS_SERVER \
    registry.k8s.io/autoscaling/vpa-recommender:$VPA_VERSION registry.k8s.io/autoscaling/vpa-updater:$VPA_VERSION \
    registry.k8s.io/autoscaling/vpa-admission-controller:$VPA_VERSION registry.k8s.io/ingress-nginx/kube-webhook-certgen:v20231011-8b53cabe0; do
    docker pull -q "$img" >/dev/null && kind load docker-image --name "$CLUSTER" "$img" >/dev/null 2>&1 ||
        log "could not preload $img; the node will pull it"
done

log "installing Prometheus, metrics-server and VPA $VPA_VERSION"
kubectl apply -f e2e/manifests/prometheus.yaml
kubectl apply -f "https://github.com/kubernetes-sigs/metrics-server/releases/download/$METRICS_SERVER/components.yaml"
kubectl -n kube-system patch deploy metrics-server --type json -p '[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--kubelet-insecure-tls"}]'
helm install vpa "$VPA_CHART" -n kube-system --wait --timeout 10m \
    --set admissionController.replicas=1 --set recommender.replicas=1 --set updater.replicas=1 \
    --set 'updater.extraArgs={--updater-interval=15s,--in-place-skip-disruption-budget=true}'

log "installing the oom-budget recommender (20-second windows)"
kubectl apply -f deploy/recommender.yaml
kubectl -n kube-system patch deploy vpa-oom-budget-recommender --type json -p '[
  {"op": "replace", "path": "/spec/template/spec/containers/0/image", "value": "vpa-oom-budget:e2e"},
  {"op": "add", "path": "/spec/template/spec/containers/0/imagePullPolicy", "value": "IfNotPresent"},
  {"op": "replace", "path": "/spec/template/spec/containers/0/args", "value": [
    "--prometheus-url=http://prometheus.monitoring.svc:9090", "--budget=0.18",
    "--window=20s", "--base-windows=45", "--history-windows=45", "--min-windows=5", "--interval=15s"]}]'
for d in kube-system/vpa-oom-budget-recommender kube-system/metrics-server monitoring/prometheus; do
    kubectl -n "${d%/*}" rollout status "deploy/${d#*/}" --timeout 300s
done

log "deploying memhog (holds 40 MiB, starts at 160 MiB) and plain"
kubectl apply -f e2e/manifests/app.yaml
kubectl rollout status deploy/memhog --timeout 300s

mib() { numfmt --from=iec-i --to-unit=Mi --round=nearest 2>/dev/null || echo 0; }
rec_mib() { kubectl get vpa memhog -o jsonpath="{.status.recommendation.containerRecommendations[0].$1.memory}" | mib; }
target_mib() { rec_mib target; }
pods() { kubectl get pods -l app=memhog -o jsonpath='{range .items[*]}{.metadata.name} {.status.phase} ready={.status.containerStatuses[0].ready} restarts={.status.containerStatuses[0].restartCount} mem={.spec.containers[0].resources.requests.memory} last={.status.containerStatuses[0].lastState.terminated.reason}{"\n"}{end}'; }
(while true; do
    printf '%s target=%sMi msg="%s"\n' "$(date -u +%H:%M:%S)" "$(target_mib)" \
        "$(kubectl get vpa memhog -o jsonpath='{.status.conditions[0].message}')"
    pods | sed 's/^/    /'
    sleep 15
done) > "$OUT/timeline.txt" 2>&1 &
watcher=$!

# wait_until <seconds> <description> <command...>
wait_until() {
    local deadline=$((SECONDS + $1)) what=$2
    shift 2
    while ((SECONDS < deadline)); do
        if "$@"; then log "ok: $what"; return 0; fi
        sleep 10
    done
    log "timed out: $what"
    return 1
}
# both memhog pods are running and ready, request at least $1 MiB, and sit
# inside the recommendation's [lowerBound, upperBound], which is what the VPA
# updater enforces (the target itself moves a little every cycle)
sized() {
    local lo hi lines m
    lo=$(rec_mib lowerBound); hi=$(rec_mib upperBound)
    ((hi >= $1)) || return 1
    lines=$(pods)
    [[ $(wc -l <<<"$lines") -eq 2 ]] || return 1
    while read -r _ phase ready _ mem _; do
        m=$(mib <<<"${mem#mem=}")
        [[ $phase == Running && $ready == ready=true ]] && ((m >= $1 && m >= lo && m <= hi)) || return 1
    done <<<"$lines"
}
pod_mib() { kubectl get pods -l app=memhog -o jsonpath='{.items[0].spec.containers[0].resources.requests.memory}' | mib; }
restarts() { kubectl get pods -l app=memhog -o jsonpath='{range .items[*]}{.status.containerStatuses[0].restartCount}{" "}{end}'; }

log "case 1: pods are resized from 160 MiB to the oom-budget target"
wait_until 900 "recommendation written by oom-budget" bash -c '[[ $(kubectl get vpa memhog -o jsonpath="{.status.conditions[0].message}") == *oom-budget* ]]'
wait_until 900 "pods run at the target" sized 41
first=$(pod_mib)
log "pods resized to ${first} MiB (they hold 40 MiB; target $(target_mib) MiB)"
((first < 160)) || { log "FAIL: pods at ${first} MiB did not shrink below 160 MiB"; exit 1; }

log "case 2: memhog now needs 100 MiB; OOM kills must raise the target"
kubectl set env deploy/memhog MEM_MIB=100
wait_until 600 "an OOM kill is recorded" bash -c "kubectl -n kube-system get configmap vpa-oom-budget-state -o jsonpath='{.data.ooms\.json}' 2>/dev/null | grep -q memhog"
wait_until 1500 "pods run at a target of at least 101 MiB" sized 101
before=$(restarts)
sleep 120
after=$(restarts)
[[ "$before" == "$after" ]] || { log "FAIL: restarts changed from '$before' to '$after' after sizing"; exit 1; }
final=$(pod_mib)
log "pods resized to ${final} MiB after the OOM kills; no restarts in the last 2 minutes"

log "case 3: the default recommender writes plain and leaves memhog alone"
wait_until 600 "plain has a recommendation from the default recommender" bash -c \
    '[[ -n $(kubectl get vpa plain -o jsonpath="{.status.recommendation.containerRecommendations[0].target.memory}") ]]'
msg=$(kubectl get vpa memhog -o jsonpath='{.status.conditions[0].message}')
[[ $msg == *oom-budget* ]] || { log "FAIL: memhog status was overwritten: $msg"; exit 1; }
plain=$(kubectl get vpa plain -o jsonpath='{.status.recommendation.containerRecommendations[0].target.memory}')

kill "$watcher" 2>/dev/null || true
kubectl get vpa -o yaml > "$OUT/vpas.yaml"
kubectl -n kube-system get configmap vpa-oom-budget-state -o jsonpath='{.data.ooms\.json}' > "$OUT/oom-history.json"
kubectl -n kube-system logs deploy/vpa-oom-budget-recommender > "$OUT/recommender-log.jsonl"
oomCount=$(grep -o '"key"' "$OUT/oom-history.json" | wc -l)
{
    echo "# End-to-end result"
    echo
    echo "kind cluster, VPA $VPA_VERSION (Helm chart 0.17.0: updater, admission controller, default recommender), metrics-server $METRICS_SERVER, Prometheus v3.5.0 scraping cAdvisor. Recommender flags: budget 0.18 OOM kills per container-day with 20-second windows (the per-window risk of 0.001 per day with hourly windows), base and history 45 windows (15 minutes). Generated by \`e2e/run.sh\`."
    echo
    echo "| Case | Result |"
    echo "|---|---|"
    echo "| 1. memhog holds 40 MiB, starts at 160 MiB | VPA updater and admission controller resized both pods to ${first} MiB, inside the oom-budget recommendation's bounds |"
    echo "| 2. memhog needs 100 MiB | ${oomCount} OOM kills recorded; pods resized to ${final} MiB; both running, no restarts for 2 minutes |"
    echo "| 3. default recommender on the same cluster | wrote plain (target ${plain}); memhog's status still from oom-budget |"
    echo
    echo "Per-step timeline: \`timeline.txt\`. Recommender log: \`recommender-log.jsonl\`. OOM history ConfigMap: \`oom-history.json\`."
} > "$OUT/summary.md"
cat "$OUT/summary.md"
log "PASS"
