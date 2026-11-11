package controller

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/proxy"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

// Per-cell provider tokens (PROVIDER_CELL_TOKENS).
//
// Without them every opted-in cell receives the same real provider access
// token, so one copied lease works outside for every cell until it expires.
// With them, the worker receives a token that is unique to its cell and useless
// at the provider; only the cell's proxy holds the real token, and it swaps the
// cell token for the real one on each admitted provider request. A copied cell
// token works nowhere but through that cell's proxy, while that cell's grant is
// live, and changes whenever the real token does.
//
// The cell token is an HMAC of the provider and the real token, keyed by a key
// derived from the cell's own inspection CA key (ledger ca.key), so it is
// stable across reconciles and restarts, differs per cell, and needs no new
// stored secret. A JWT keeps its header and claims (the CLI may read the
// expiry) with the signature replaced; anything else becomes an opaque token.

// CellTokenProviders lists the providers that may use cell tokens.
var CellTokenProviders = map[string]bool{"grok": true, "cursor": true}

const cellTokenPrefix = "apc-cell-"

// ParseCellTokenProviders reads PROVIDER_CELL_TOKENS (comma-separated).
func ParseCellTokenProviders(raw string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, name := range strings.Split(raw, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if !CellTokenProviders[name] {
			return nil, fmt.Errorf("PROVIDER_CELL_TOKENS: %q is not a provider that supports cell tokens (grok, cursor)", name)
		}
		out[name] = true
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func cellTokenKey(caKey []byte) ([]byte, error) {
	if len(caKey) == 0 {
		return nil, errors.New("cell inspection key unavailable")
	}
	m := hmac.New(sha256.New, caKey)
	m.Write([]byte("glovecell provider cell token v1"))
	return m.Sum(nil), nil
}

// cellToken returns the cell's stand-in for a real provider token.
func cellToken(key []byte, provider, real string) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(provider))
	m.Write([]byte{0})
	m.Write([]byte(real))
	sig := base64.RawURLEncoding.EncodeToString(m.Sum(nil))
	if parts := strings.Split(real, "."); len(parts) == 3 && parts[0] != "" && parts[1] != "" {
		return parts[0] + "." + parts[1] + "." + sig
	}
	return cellTokenPrefix + sig
}

// cellTokenKeyFor reads the cell's ledger and derives its key.
func (r *Reconciler) cellTokenKeyFor(ctx context.Context, w *api.ProjectWorkspace) ([]byte, error) {
	s := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: r.Config.Namespace, Name: identity(w)}, s); err != nil {
		return nil, err
	}
	if s.Labels[api.OwnerLabel] != string(w.UID) {
		return nil, errors.New("project ledger is not owned by this project")
	}
	return cellTokenKey(s.Data["ca.key"])
}

// withCellTokens splits the brokered grants into what the worker sees (cell
// tokens for the listed providers) and what the proxy needs (the real token
// plus the cell token it must accept). A provider whose key cannot be derived
// is disabled for both: it fails closed, never back to the shared token.
func (r *Reconciler) withCellTokens(ctx context.Context, w *api.ProjectWorkspace, grants map[string]brokeredGrant) (worker, gateway map[string]brokeredGrant) {
	worker, gateway = map[string]brokeredGrant{}, map[string]brokeredGrant{}
	var key []byte
	var keyErr error
	for name, g := range grants {
		if !r.Config.CellTokens[name] || !g.Enabled || g.Access == nil {
			worker[name], gateway[name] = g, g
			continue
		}
		if key == nil && keyErr == nil {
			key, keyErr = r.cellTokenKeyFor(ctx, w)
		}
		if keyErr != nil {
			worker[name], gateway[name] = brokeredGrant{Enabled: false}, brokeredGrant{Enabled: false}
			continue
		}
		token := cellToken(key, name, g.Access.Token)
		workerAccess := *g.Access
		workerAccess.Token, workerAccess.CellToken = token, ""
		gatewayAccess := *g.Access
		gatewayAccess.CellToken = token
		worker[name] = brokeredGrant{Enabled: true, Access: &workerAccess}
		gateway[name] = brokeredGrant{Enabled: true, Access: &gatewayAccess}
	}
	return worker, gateway
}

// gatewayDoc is the worker's provider-access document with the brokered
// entries replaced by the proxy's versions (real token plus cell token).
func gatewayDoc(workerDoc []byte, gateway map[string]brokeredGrant) ([]byte, error) {
	if len(workerDoc) == 0 {
		return workerDoc, nil
	}
	doc := map[string]json.RawMessage{}
	if err := json.Unmarshal(workerDoc, &doc); err != nil {
		return nil, err
	}
	for name := range proxy.BrokeredProviders {
		if g, ok := gateway[name]; ok {
			doc[name], _ = json.Marshal(g)
		}
	}
	return json.Marshal(doc)
}
