package proxy

import (
	"context"
	"testing"
)

func TestMetricsListenerIsLoopbackOnly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for _, addr := range []string{"0.0.0.0:9465", ":9465", "10.0.0.1:9465", ControlAddress, "localhost:9465"} {
		if err := serveMetrics(ctx, addr); err == nil {
			t.Errorf("metrics listener accepted %q", addr)
		}
	}
	if err := serveMetrics(ctx, "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
}
