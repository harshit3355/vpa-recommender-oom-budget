package controller

import (
	"context"
	"encoding/json"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// OOMEvent is one OOM kill of a VPA-managed container.
type OOMEvent struct {
	Key   string    `json:"key"` // namespace/vpa/container
	Pod   string    `json:"pod"` // pod UID
	At    time.Time `json:"at"`
	Limit float64   `json:"limit"` // memory limit (bytes) the container was killed at
}

// OOMStore remembers OOM kills after their pods are gone (the VPA updater
// evicts exactly those pods), so a restart or an eviction does not make the
// recommender forget why it sized a container up. It lives in one ConfigMap.
type OOMStore struct {
	Kube      kubernetes.Interface
	Namespace string
	Name      string
	events    []OOMEvent
	loaded    bool
	dirty     bool
}

const oomKey = "ooms.json"

func (s *OOMStore) load(ctx context.Context) error {
	if s.loaded {
		return nil
	}
	cm, err := s.Kube.CoreV1().ConfigMaps(s.Namespace).Get(ctx, s.Name, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
	case err != nil:
		return err
	default:
		if err := json.Unmarshal([]byte(cm.Data[oomKey]), &s.events); err != nil {
			return err
		}
	}
	s.loaded = true
	return nil
}

func (s *OOMStore) add(e OOMEvent) {
	for _, o := range s.events {
		if o.Key == e.Key && o.Pod == e.Pod && o.At.Equal(e.At) {
			return
		}
	}
	s.events = append(s.events, e)
	s.dirty = true
}

func (s *OOMStore) forKey(key string) []OOMEvent {
	var out []OOMEvent
	for _, e := range s.events {
		if e.Key == key {
			out = append(out, e)
		}
	}
	return out
}

// save drops events older than keepAfter and writes the ConfigMap if anything changed.
func (s *OOMStore) save(ctx context.Context, keepAfter time.Time) error {
	kept := s.events[:0]
	for _, e := range s.events {
		if e.At.After(keepAfter) {
			kept = append(kept, e)
		} else {
			s.dirty = true
		}
	}
	s.events = kept
	if !s.dirty {
		return nil
	}
	body, err := json.Marshal(s.events)
	if err != nil {
		return err
	}
	cms := s.Kube.CoreV1().ConfigMaps(s.Namespace)
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: s.Name, Namespace: s.Namespace}, Data: map[string]string{oomKey: string(body)}}
	if _, err = cms.Update(ctx, cm, metav1.UpdateOptions{}); apierrors.IsNotFound(err) {
		_, err = cms.Create(ctx, cm, metav1.CreateOptions{})
	}
	if err == nil {
		s.dirty = false
	}
	return err
}
