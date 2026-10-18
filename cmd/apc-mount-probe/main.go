// Command apc-mount-probe mounts one sandbox repo for live qualification.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/registration"
	"k8s.io/client-go/tools/clientcmd"
)

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "usage: apc-mount-probe <namespace> <pod> <mount>")
		os.Exit(2)
	}
	ns, pod, mount := os.Args[1], os.Args[2], os.Args[3]
	cfg, err := clientcmd.BuildConfigFromFlags("", clientcmd.RecommendedHomeFile)
	if err != nil {
		panic(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	remote, closer, err := registration.StartKubectlHelper(ctx, cfg, ns, pod)
	if err != nil {
		panic(err)
	}
	defer closer()
	server, err := registration.MountRemote(mount, "apc:"+ns, "/workspace/repo", remote)
	if err != nil {
		panic(err)
	}
	fmt.Println("ready", mount)
	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGINT, syscall.SIGTERM)
	select {
	case <-c:
	case <-time.After(10 * time.Minute):
	}
	_ = server.Unmount()
}
