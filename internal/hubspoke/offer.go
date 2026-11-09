package hubspoke

import (
	"bytes"
	"context"
	"fmt"
	"io"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// OfferFromLogs returns an OfferFrom that reads the worker container logs and
// parses a runtime pairing offer. Log bytes are scanned in memory and never
// written; only parse errors (without the URL) are returned.
func OfferFromLogs(cfg *rest.Config, runtime Runtime, container string) func(context.Context, *api.ProjectWorkspace, *corev1.Pod) (Offer, error) {
	if container == "" {
		container = "workspace"
	}
	return func(ctx context.Context, _ *api.ProjectWorkspace, pod *corev1.Pod) (Offer, error) {
		if cfg == nil {
			return Offer{}, fmt.Errorf("rest config required")
		}
		cs, err := kubernetes.NewForConfig(cfg)
		if err != nil {
			return Offer{}, err
		}
		opts := &corev1.PodLogOptions{Container: container, TailLines: int64p(200)}
		stream, err := cs.CoreV1().Pods(pod.Namespace).GetLogs(pod.Name, opts).Stream(ctx)
		if err != nil {
			return Offer{}, fmt.Errorf("spoke logs: %w", err)
		}
		defer stream.Close()
		var buf bytes.Buffer
		if _, err := io.Copy(&buf, io.LimitReader(stream, 1<<20)); err != nil {
			return Offer{}, fmt.Errorf("spoke logs: %w", err)
		}
		return FindReadyOffer(runtime, buf.Bytes())
	}
}

func int64p(n int64) *int64 { return &n }
