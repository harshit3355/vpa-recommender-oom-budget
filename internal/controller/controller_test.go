package controller

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	promapi "github.com/prometheus/client_golang/api"
	promv1 "github.com/prometheus/client_golang/api/prometheus/v1"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubefake "k8s.io/client-go/kubernetes/fake"

	vpav1 "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/apis/autoscaling.k8s.io/v1"
	vpafake "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/client/clientset/versioned/fake"

	"github.com/harshit3355/vpa-recommender-oom-budget/internal/budget"
)

const mi = 1 << 20

// fakeProm answers every range query with a peak of 50-54 MiB per window.
func fakeProm(t *testing.T) promv1.API {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		start, _ := strconv.ParseFloat(r.Form.Get("start"), 64)
		end, _ := strconv.ParseFloat(r.Form.Get("end"), 64)
		step, _ := strconv.ParseFloat(r.Form.Get("step"), 64)
		var vals []string
		for i, ts := 0, start; ts <= end; i, ts = i+1, ts+step {
			vals = append(vals, fmt.Sprintf(`[%.0f,"%d"]`, ts, (50+i%5)*mi))
		}
		io.WriteString(w, `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{},"values":[`+strings.Join(vals, ",")+`]}]}}`)
	}))
	t.Cleanup(srv.Close)
	c, err := promapi.NewClient(promapi.Config{Address: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	return promv1.NewAPI(c)
}

func deployment(name string, containers ...string) *appsv1.Deployment {
	var cs []corev1.Container
	for _, c := range containers {
		cs = append(cs, corev1.Container{Name: c})
	}
	labels := map[string]string{"app": name}
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns"},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: corev1.PodSpec{Containers: cs}},
		},
	}
}

func vpa(name, target, recommender string, policy *vpav1.PodResourcePolicy) *vpav1.VerticalPodAutoscaler {
	v := &vpav1.VerticalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns"},
		Spec: vpav1.VerticalPodAutoscalerSpec{
			TargetRef:      &autoscalingv1.CrossVersionObjectReference{APIVersion: "apps/v1", Kind: "Deployment", Name: target},
			ResourcePolicy: policy,
		},
	}
	if recommender != "" {
		v.Spec.Recommenders = []*vpav1.VerticalPodAutoscalerRecommenderSelector{{Name: recommender}}
	}
	return v
}

func TestRunOnce(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	off := vpav1.ContainerScalingModeOff
	oomPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "web-6d4cf56db6-x7k2p", Namespace: "ns", Labels: map[string]string{"app": "web"}},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Resources: corev1.ResourceRequirements{
			Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("100Mi")}}}}},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{Name: "app", LastTerminationState: corev1.ContainerState{
			Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled", FinishedAt: metav1.NewTime(now.Add(-30 * time.Minute))}}}}},
	}
	kube := kubefake.NewClientset(deployment("web", "app", "sidecar"), deployment("api", "app"), oomPod)
	vpas := vpafake.NewSimpleClientset(
		vpa("web", "web", "oom-budget", &vpav1.PodResourcePolicy{ContainerPolicies: []vpav1.ContainerResourcePolicy{
			{ContainerName: "sidecar", Mode: &off}}}),
		vpa("api", "api", "oom-budget", &vpav1.PodResourcePolicy{ContainerPolicies: []vpav1.ContainerResourcePolicy{
			{ContainerName: "*", MaxAllowed: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("40Mi")}}}}),
		vpa("theirs", "api", "default", nil),
		vpa("plain", "api", "", nil),
	)
	c := &Controller{
		Kube: kube, VPA: vpas, Prom: fakeProm(t), Name: "oom-budget", Cfg: budget.Default,
		Step: time.Hour, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return now },
		OOMs: &OOMStore{Kube: kube, Namespace: "kube-system", Name: "state"},
	}
	ctx := context.Background()
	if err := c.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	get := func(name string) *vpav1.VerticalPodAutoscaler {
		v, err := vpas.AutoscalingV1().VerticalPodAutoscalers("ns").Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	mem := func(l corev1.ResourceList) float64 { q := l[corev1.ResourceMemory]; return q.AsApproximateFloat64() }

	web := get("web").Status.Recommendation
	if web == nil || len(web.ContainerRecommendations) != 1 || web.ContainerRecommendations[0].ContainerName != "app" {
		t.Fatalf("web: want one recommendation for container app (sidecar is Off), got %+v", web)
	}
	if got := mem(web.ContainerRecommendations[0].Target); got < 120*mi {
		t.Errorf("web target %.0f MiB after an OOM at 100 MiB, want >= 120 MiB", got/mi)
	}
	if conds := get("web").Status.Conditions; len(conds) != 1 || conds[0].Type != vpav1.RecommendationProvided {
		t.Errorf("web conditions = %+v", conds)
	}

	api := get("api").Status.Recommendation
	if api == nil || len(api.ContainerRecommendations) != 1 {
		t.Fatalf("api: got %+v", api)
	}
	r := api.ContainerRecommendations[0]
	if got := mem(r.Target); got != 40*mi {
		t.Errorf("api target %.1f MiB, want maxAllowed 40 MiB", got/mi)
	}
	if got := mem(r.UncappedTarget); got < 54*mi {
		t.Errorf("api uncapped target %.1f MiB, want >= the 54 MiB peak", got/mi)
	}

	for _, n := range []string{"theirs", "plain"} {
		if s := get(n).Status; s.Recommendation != nil || len(s.Conditions) != 0 {
			t.Errorf("%s was not ours but got status %+v", n, s)
		}
	}

	// The VPA updater evicts the OOM-killed pod and a new recommender
	// process starts: the OOM must still count, read back from the ConfigMap.
	if err := kube.CoreV1().Pods("ns").Delete(ctx, oomPod.Name, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	c.OOMs = &OOMStore{Kube: kube, Namespace: "kube-system", Name: "state"}
	if err := c.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if got := mem(get("web").Status.Recommendation.ContainerRecommendations[0].Target); got < 120*mi {
		t.Errorf("web target %.0f MiB after the OOM-killed pod is gone, want >= 120 MiB", got/mi)
	}
}
