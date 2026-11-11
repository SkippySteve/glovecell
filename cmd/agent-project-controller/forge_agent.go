package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/forgeagent"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/forgejo"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
)

// forgeAgentCLI runs the forge agent: the only process that holds the Forgejo
// administrative token (FORGEJO_TOKEN_FILE). It serves Discover, Grant and
// Revoke for workspace identities to exactly one caller, FORGE_AGENT_CALLER
// (system:serviceaccount:<namespace>:<name>), checked by TokenReview.
func forgeAgentCLI(ctx context.Context) error {
	token, err := secret("FORGEJO_TOKEN_FILE")
	if err != nil {
		return err
	}
	host, err := forgejo.New(os.Getenv("FORGEJO_ORIGIN"), os.Getenv("FORGEJO_OWNER"), token)
	if err != nil {
		return err
	}
	caller := strings.TrimSpace(os.Getenv("FORGE_AGENT_CALLER"))
	if !strings.HasPrefix(caller, "system:serviceaccount:") || strings.Count(caller, ":") != 3 {
		return errors.New("FORGE_AGENT_CALLER must be system:serviceaccount:<namespace>:<name>")
	}
	kube, err := kubernetes.NewForConfig(ctrl.GetConfigOrDie())
	if err != nil {
		return err
	}
	server := &http.Server{
		Addr:              env("LISTEN_ADDR", ":8443"),
		Handler:           &forgeagent.Server{Host: host, Owner: host.Owner, Caller: caller, Reviewer: forgeagent.TokenReviewer{Client: kube, Audience: forgeagent.Audience}},
		ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 90 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10,
	}
	go func() {
		<-ctx.Done()
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(c)
	}()
	slog.Info("forge agent listening", "addr", server.Addr, "owner", host.Owner)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// forgeAgentHost is the controller's Forgejo host when FORGE_AGENT_URL is set:
// the controller then holds no Forgejo administrative token at all.
func forgeAgentHost() (*forgeagent.Client, error) {
	if os.Getenv("FORGEJO_TOKEN_FILE") != "" {
		return nil, errors.New("FORGE_AGENT_URL and FORGEJO_TOKEN_FILE are exclusive: the administrative token belongs to the forge agent only")
	}
	return forgeagent.NewClient(os.Getenv("FORGE_AGENT_URL"), os.Getenv("FORGE_AGENT_TOKEN_FILE"), os.Getenv("FORGEJO_ORIGIN"), os.Getenv("FORGEJO_OWNER"))
}
