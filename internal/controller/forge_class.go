package controller

import (
	"context"
	"errors"
	"fmt"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/forgejo"
)

// forWorkspace returns the reconciler as seen by one workspace: Host, GitHost,
// ForgejoOrigin and Owner come from its ForgeClass (spec.forgeClassName).
// An empty class name keeps the configured forge unchanged, so existing
// workspaces behave exactly as before. When the class does not resolve, the
// returned view's forge refuses every call with that error, so nothing can
// fall back to the configured forge.
func (r *Reconciler) forWorkspace(ctx context.Context, w *api.ProjectWorkspace) (*Reconciler, error) {
	name := w.Spec.ForgeClassName
	if name == "" {
		return r, nil
	}
	view := *r
	view.Host = unresolvedForge{fmt.Errorf("ForgeClass %q requested, but this controller resolves no forge classes", name)}
	if r.Forges == nil {
		return &view, view.Host.(unresolvedForge).err
	}
	f, err := r.Forges.Resolve(ctx, name)
	if err == nil && (f.Forge == nil || f.Origin == "" || f.Owner == "") {
		err = errors.New("forge class resolved incompletely")
	}
	if err != nil {
		view.Host = unresolvedForge{err}
		return &view, err
	}
	view.Host = f.Forge
	view.Config.GitHost = f.Driver
	view.Config.ForgejoOrigin = f.Origin
	view.Config.Owner = f.Owner
	return &view, nil
}

// unresolvedForge stands in for a ForgeClass that does not resolve.
type unresolvedForge struct{ err error }

func (u unresolvedForge) Discover(context.Context) ([]forgejo.Repository, error) { return nil, u.err }
func (u unresolvedForge) Grant(context.Context, string, forgejo.Credentials) (string, error) {
	return "", u.err
}
func (u unresolvedForge) Revoke(context.Context, string, forgejo.Credentials) error { return u.err }
