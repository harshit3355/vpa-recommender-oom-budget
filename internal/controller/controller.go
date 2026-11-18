// Package controller is a VPA recommender: it writes memory recommendations
// into the status of every VerticalPodAutoscaler whose spec.recommenders
// names it, and leaves every other VPA to its own recommender. The VPA
// updater and admission controller then apply them as usual.
package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"regexp"
	"time"

	promv1 "github.com/prometheus/client_golang/api/prometheus/v1"
	prommodel "github.com/prometheus/common/model"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"

	vpav1 "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/apis/autoscaling.k8s.io/v1"
	vpaclient "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/client/clientset/versioned"

	"github.com/harshit3355/vpa-recommender-oom-budget/internal/budget"
)

// Controller computes and writes recommendations.
type Controller struct {
	Kube kubernetes.Interface
	VPA  vpaclient.Interface
	Prom promv1.API
	Name string        // recommender name VPAs refer to in spec.recommenders
	Cfg  budget.Config // BaseWindows and HistoryWindows count Step-long windows
	Step time.Duration // length of one peak window (1h; shorter in tests)
	Log  *slog.Logger
	Now  func() time.Time
	OOMs *OOMStore
}

type container struct {
	vpa   *vpav1.VerticalPodAutoscaler
	name  string
	hours []budget.Hour
}

// RunOnce recomputes every managed VPA.
func (c *Controller) RunOnce(ctx context.Context) error {
	list, err := c.VPA.AutoscalingV1().VerticalPodAutoscalers("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list VPAs: %w", err)
	}
	if err := c.OOMs.load(ctx); err != nil {
		return fmt.Errorf("load OOM history: %w", err)
	}
	now := c.Now().Truncate(c.Step)
	var conts []container
	for i := range list.Items {
		v := &list.Items[i]
		if !c.manages(v) {
			continue
		}
		cs, err := c.history(ctx, v, now)
		if err != nil {
			c.Log.Warn("skipping VPA", "vpa", v.Namespace+"/"+v.Name, "err", err)
			continue
		}
		conts = append(conts, cs...)
	}
	window := time.Duration(c.Cfg.BaseWindows+c.Cfg.HistoryWindows+1) * c.Step
	if err := c.OOMs.save(ctx, now.Add(-window)); err != nil {
		c.Log.Warn("saving OOM history failed", "err", err)
	}

	var scores []float64
	for _, k := range conts {
		scores = append(scores, c.Cfg.Scores(k.hours)...)
	}
	pool := budget.NewPool(scores)
	if len(conts) > 0 && len(pool) <= c.Cfg.MinTailPoints {
		c.Log.Info("waiting for calibration data", "scored_windows", len(pool), "needed", c.Cfg.MinTailPoints+1)
	}

	recs := map[*vpav1.VerticalPodAutoscaler][]vpav1.RecommendedContainerResources{}
	for _, k := range conts {
		r, ok := c.Cfg.Recommend(k.hours, pool)
		if !ok {
			continue
		}
		recs[k.vpa] = append(recs[k.vpa], c.capped(k.vpa, k.name, r))
	}
	for v, rs := range recs {
		if err := c.writeStatus(ctx, v, rs, now); err != nil {
			c.Log.Warn("status update failed", "vpa", v.Namespace+"/"+v.Name, "err", err)
			continue
		}
		c.Log.Info("recommendation written", "vpa", v.Namespace+"/"+v.Name, "containers", len(rs), "pool", len(pool))
	}
	return nil
}

func (c *Controller) manages(v *vpav1.VerticalPodAutoscaler) bool {
	for _, r := range v.Spec.Recommenders {
		if r != nil && r.Name == c.Name {
			return true
		}
	}
	return false
}

// history returns the per-window memory peaks and OOM kills of every
// container of the VPA's target, oldest first, ending with the window that
// ends at now.
func (c *Controller) history(ctx context.Context, v *vpav1.VerticalPodAutoscaler, now time.Time) ([]container, error) {
	ref := v.Spec.TargetRef
	if ref == nil {
		return nil, fmt.Errorf("no targetRef")
	}
	tmpl, sel, podRE, err := c.target(ctx, v.Namespace, ref.Kind, ref.Name)
	if err != nil {
		return nil, err
	}
	pods, err := c.Kube.CoreV1().Pods(v.Namespace).List(ctx, metav1.ListOptions{LabelSelector: sel})
	if err != nil {
		return nil, fmt.Errorf("list pods: %w", err)
	}
	n := c.Cfg.BaseWindows + c.Cfg.HistoryWindows + 1
	first := now.Add(-time.Duration(n-1) * c.Step)
	var out []container
	for _, ct := range tmpl.Containers {
		if !c.controlsMemory(v, ct.Name) {
			continue
		}
		// ponytail: one full-history range query per container per cycle; fine for
		// hundreds of containers, cache finished windows if Prometheus load matters.
		q := fmt.Sprintf(`max(max_over_time(container_memory_working_set_bytes{namespace=%q,pod=~%q,container=%q}[%s]))`,
			v.Namespace, podRE, ct.Name, prommodel.Duration(c.Step))
		val, _, err := c.Prom.QueryRange(ctx, q, promv1.Range{Start: first, End: now, Step: c.Step})
		if err != nil {
			return nil, fmt.Errorf("prometheus: %w", err)
		}
		hours := make([]budget.Hour, n)
		for i := range hours {
			hours[i].Peak = math.NaN()
		}
		if m, ok := val.(prommodel.Matrix); ok {
			for _, s := range m {
				for _, p := range s.Values {
					if i := int(p.Timestamp.Time().Sub(first) / c.Step); i >= 0 && i < n {
						hours[i].Peak = math.Max(nanToZero(hours[i].Peak), float64(p.Value))
					}
				}
			}
		}
		key := v.Namespace + "/" + v.Name + "/" + ct.Name
		for _, p := range pods.Items {
			for _, st := range p.Status.ContainerStatuses {
				t := st.LastTerminationState.Terminated
				if st.Name == ct.Name && t != nil && t.Reason == "OOMKilled" {
					c.OOMs.add(OOMEvent{Key: key, Pod: string(p.UID), At: t.FinishedAt.UTC(), Limit: memoryLimit(p.Spec.Containers, ct.Name)})
				}
			}
		}
		for _, e := range c.OOMs.forKey(key) {
			i := int(math.Ceil(float64(e.At.Sub(first)) / float64(c.Step)))
			if i < 0 {
				continue
			}
			i = min(i, n-1) // an OOM in the current, unfinished window counts now
			hours[i].OOMLimit = math.Max(hours[i].OOMLimit, e.Limit)
		}
		out = append(out, container{vpa: v, name: ct.Name, hours: hours})
	}
	return out, nil
}

func nanToZero(v float64) float64 {
	if math.IsNaN(v) {
		return 0
	}
	return v
}

func memoryLimit(cs []corev1.Container, name string) float64 {
	for _, ct := range cs {
		if ct.Name != name {
			continue
		}
		if q, ok := ct.Resources.Limits[corev1.ResourceMemory]; ok {
			return q.AsApproximateFloat64()
		}
		if q, ok := ct.Resources.Requests[corev1.ResourceMemory]; ok {
			return q.AsApproximateFloat64()
		}
	}
	return 0
}

// target resolves the pod template, label selector and pod-name pattern of
// the workload. Pod names are matched by the pattern each controller uses, so
// history survives rollouts (a Deployment's ReplicaSet hash changes).
func (c *Controller) target(ctx context.Context, ns, kind, name string) (*corev1.PodSpec, string, string, error) {
	apps := c.Kube.AppsV1()
	q := regexp.QuoteMeta(name)
	var spec corev1.PodSpec
	var sel *metav1.LabelSelector
	var re string
	switch kind {
	case "Deployment":
		o, err := apps.Deployments(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, "", "", err
		}
		spec, sel, re = o.Spec.Template.Spec, o.Spec.Selector, q+"-[a-z0-9]+-[a-z0-9]+"
	case "StatefulSet":
		o, err := apps.StatefulSets(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, "", "", err
		}
		spec, sel, re = o.Spec.Template.Spec, o.Spec.Selector, q+"-[0-9]+"
	case "DaemonSet":
		o, err := apps.DaemonSets(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, "", "", err
		}
		spec, sel, re = o.Spec.Template.Spec, o.Spec.Selector, q+"-[a-z0-9]+"
	case "ReplicaSet":
		o, err := apps.ReplicaSets(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, "", "", err
		}
		spec, sel, re = o.Spec.Template.Spec, o.Spec.Selector, q+"-[a-z0-9]+"
	default:
		return nil, "", "", fmt.Errorf("unsupported targetRef kind %q", kind)
	}
	s, err := metav1.LabelSelectorAsSelector(sel)
	if err != nil {
		return nil, "", "", err
	}
	return &spec, s.String(), re, nil
}

func (c *Controller) policy(v *vpav1.VerticalPodAutoscaler, name string) *vpav1.ContainerResourcePolicy {
	if v.Spec.ResourcePolicy == nil {
		return nil
	}
	var wildcard *vpav1.ContainerResourcePolicy
	for i := range v.Spec.ResourcePolicy.ContainerPolicies {
		p := &v.Spec.ResourcePolicy.ContainerPolicies[i]
		switch p.ContainerName {
		case name:
			return p
		case vpav1.DefaultContainerResourcePolicy:
			wildcard = p
		}
	}
	return wildcard
}

func (c *Controller) controlsMemory(v *vpav1.VerticalPodAutoscaler, name string) bool {
	p := c.policy(v, name)
	if p == nil {
		return true
	}
	if p.Mode != nil && *p.Mode == vpav1.ContainerScalingModeOff {
		return false
	}
	if p.ControlledResources == nil {
		return true
	}
	for _, r := range *p.ControlledResources {
		if r == corev1.ResourceMemory {
			return true
		}
	}
	return false
}

// capped turns a recommendation into VPA's format, applying the container
// policy's minAllowed and maxAllowed memory as the VPA recommender does.
func (c *Controller) capped(v *vpav1.VerticalPodAutoscaler, name string, r budget.Rec) vpav1.RecommendedContainerResources {
	lo, hi := 0.0, math.Inf(1)
	if p := c.policy(v, name); p != nil {
		if q, ok := p.MinAllowed[corev1.ResourceMemory]; ok {
			lo = q.AsApproximateFloat64()
		}
		if q, ok := p.MaxAllowed[corev1.ResourceMemory]; ok {
			hi = q.AsApproximateFloat64()
		}
	}
	mem := func(b float64) corev1.ResourceList {
		return corev1.ResourceList{corev1.ResourceMemory: *resource.NewQuantity(int64(math.Ceil(b)), resource.BinarySI)}
	}
	clamp := func(b float64) float64 { return math.Min(math.Max(b, lo), hi) }
	return vpav1.RecommendedContainerResources{
		ContainerName:  name,
		Target:         mem(clamp(r.Target)),
		LowerBound:     mem(clamp(r.Lower)),
		UpperBound:     mem(clamp(r.Upper)),
		UncappedTarget: mem(r.Target),
	}
}

func (c *Controller) writeStatus(ctx context.Context, v *vpav1.VerticalPodAutoscaler, rs []vpav1.RecommendedContainerResources, now time.Time) error {
	cond := vpav1.VerticalPodAutoscalerCondition{
		Type:               vpav1.RecommendationProvided,
		Status:             corev1.ConditionTrue,
		LastTransitionTime: metav1.NewTime(now),
		Message:            fmt.Sprintf("memory sized by %s for %g OOM kills per container-day", c.Name, c.Cfg.BudgetPerDay),
	}
	for _, old := range v.Status.Conditions {
		if old.Type == cond.Type && old.Status == cond.Status {
			cond.LastTransitionTime = old.LastTransitionTime
		}
	}
	body, err := json.Marshal(map[string]any{"status": vpav1.VerticalPodAutoscalerStatus{
		Recommendation: &vpav1.RecommendedPodResources{ContainerRecommendations: rs},
		Conditions:     []vpav1.VerticalPodAutoscalerCondition{cond},
	}})
	if err != nil {
		return err
	}
	_, err = c.VPA.AutoscalingV1().VerticalPodAutoscalers(v.Namespace).Patch(ctx, v.Name, types.MergePatchType, body, metav1.PatchOptions{}, "status")
	return err
}
