package hubspoke

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// WatchOptions configures the hub enrollment loop.
type WatchOptions struct {
	ControlNamespace string
	Kind             string // "orca"
	ProjectPath      string
	Tick             time.Duration
	// OfferFrom returns the current runtime-scope offer for a Ready spoke.
	// Implementations must not log the offer.
	OfferFrom func(ctx context.Context, w *api.ProjectWorkspace, pod *corev1.Pod) (Offer, error)
	// ClassName, when set, only enrolls workspaces that select this
	// OrchestratorClass name. Empty enrolls every Ready workspace when the
	// adapter kind matches the deployment default path (orca class or none
	// while testing). Prefer setting it in production.
	ClassName string
}

// RunWatch enrolls Ready project sandboxes with the hub Enroller and revokes
// when they leave Ready. It owns the hubspoke ledger ConfigMaps.
func RunWatch(ctx context.Context, c client.Client, enroller Enroller, runtime Runtime, opts WatchOptions) error {
	if opts.ControlNamespace == "" {
		return errors.New("control namespace required")
	}
	if enroller == nil || runtime == nil {
		return errors.New("enroller and runtime required")
	}
	if opts.Kind == "" {
		opts.Kind = enroller.Kind()
	}
	if opts.ProjectPath == "" {
		opts.ProjectPath = "/workspace/repo"
	}
	if opts.Tick <= 0 {
		opts.Tick = 15 * time.Second
	}
	if opts.OfferFrom == nil {
		return errors.New("OfferFrom required")
	}
	ticker := time.NewTicker(opts.Tick)
	defer ticker.Stop()
	for {
		if err := syncOnce(ctx, c, enroller, runtime, opts); err != nil {
			slog.Error("hubspoke sync", "error", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func syncOnce(ctx context.Context, c client.Client, enroller Enroller, runtime Runtime, opts WatchOptions) error {
	var list api.ProjectWorkspaceList
	if err := c.List(ctx, &list, client.InNamespace(opts.ControlNamespace)); err != nil {
		return err
	}
	want := map[string]*api.ProjectWorkspace{}
	for i := range list.Items {
		w := &list.Items[i]
		if !eligible(w, opts) {
			continue
		}
		want[string(w.UID)] = w
	}
	// Revoke enrolled spokes that are no longer eligible.
	enrolled, err := enroller.List(ctx)
	if err != nil {
		return err
	}
	byName := map[string]Enrolled{}
	for _, e := range enrolled {
		byName[e.Name] = e
	}
	for _, w := range list.Items {
		uid := string(w.UID)
		if _, ok := want[uid]; ok {
			continue
		}
		rec, err := GetState(ctx, c, opts.ControlNamespace, uid, opts.Kind)
		if err != nil {
			return err
		}
		if rec.State != StateEnrolled && rec.State != StatePending {
			continue
		}
		name := rec.Name
		if name == "" {
			name = w.Name
		}
		if err := enroller.Revoke(ctx, name); err != nil {
			slog.Warn("hubspoke revoke", "workspace", w.Name, "error", err)
			continue
		}
		if err := SetState(ctx, c, opts.ControlNamespace, uid, opts.Kind, LedgerRecord{State: StateRevoked, Name: name, ID: rec.ID}); err != nil {
			return err
		}
		slog.Info("revoked spoke", "workspace", w.Name)
	}
	for uid, w := range want {
		if err := ensureSpoke(ctx, c, enroller, runtime, opts, w, byName); err != nil {
			slog.Warn("hubspoke enroll", "workspace", w.Name, "error", err)
			_ = SetState(ctx, c, opts.ControlNamespace, uid, opts.Kind, LedgerRecord{State: StatePending, Name: w.Name})
		}
	}
	return nil
}

func eligible(w *api.ProjectWorkspace, opts WatchOptions) bool {
	if w.DeletionTimestamp != nil || w.Spec.DesiredState != "Running" {
		return false
	}
	// Enroll as soon as the workspace is Running and still present. Waiting
	// for Phase Ready would deadlock with Ensure if Ensure waited on the
	// ledger; the hub instead needs a live worker that prints a pairing offer.
	if w.Status.Phase == "Removing" || w.Status.Phase == "Removed" {
		return false
	}
	if opts.ClassName != "" {
		return w.Spec.OrchestratorClassName == opts.ClassName
	}
	// Without a class filter, only enroll workspaces that name the orca class
	// or leave the class empty (operator is migrating). Never enroll an
	// explicit non-orca class.
	switch w.Spec.OrchestratorClassName {
	case "", "orca":
		return true
	default:
		return false
	}
}

func ensureSpoke(ctx context.Context, c client.Client, enroller Enroller, runtime Runtime, opts WatchOptions, w *api.ProjectWorkspace, byName map[string]Enrolled) error {
	uid := string(w.UID)
	rec, err := GetState(ctx, c, opts.ControlNamespace, uid, opts.Kind)
	if err != nil {
		return err
	}
	if rec.State == StateEnrolled && rec.ID != "" {
		return nil
	}
	var pods corev1.PodList
	ns := "apc-" + strings.ReplaceAll(uid, "-", "")
	if err := c.List(ctx, &pods, client.InNamespace(ns), client.MatchingLabels{api.OwnerLabel: uid, "projects.agentwork.dev/role": "worker"}); err != nil {
		return err
	}
	if len(pods.Items) != 1 {
		return fmt.Errorf("need exactly one worker pod, found %d", len(pods.Items))
	}
	pod := pods.Items[0].DeepCopy()
	if err := SetState(ctx, c, opts.ControlNamespace, uid, opts.Kind, LedgerRecord{State: StatePending, Name: w.Name}); err != nil {
		return err
	}
	offer, err := opts.OfferFrom(ctx, w, pod)
	if err != nil {
		return err
	}
	_ = runtime // offer already parsed by OfferFrom; kept for interface clarity
	saved, ok := byName[w.Name]
	var enrolled Enrolled
	if ok {
		enrolled = saved
	} else {
		enrolled, err = enroller.Enroll(ctx, w.Name, offer)
		if err != nil {
			return err
		}
	}
	if err := enroller.Connect(ctx, enrolled.ID); err != nil {
		return err
	}
	if err := enroller.RegisterProject(ctx, enrolled.ID, opts.ProjectPath); err != nil {
		return err
	}
	if err := SetState(ctx, c, opts.ControlNamespace, uid, opts.Kind, LedgerRecord{
		State: StateEnrolled,
		ID:    enrolled.ID,
		Name:  enrolled.Name,
	}); err != nil {
		return err
	}
	slog.Info("enrolled spoke", "workspace", w.Name, "environment", enrolled.ID)
	return nil
}
