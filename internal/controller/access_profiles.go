package controller

// Access levels (docs/access-levels.md): a cell names a cluster-scoped
// AccessProfile and may hold time-limited AccessGrants.
//
// Profile rules are read only and travel with the deployment's reference
// rules as labelled rules ("profile:<name>"), so a profile switch or a profile
// edit applies while the cell runs: network policy first, then the proxy
// policy, then a proxy restart (syncReferenceRules).
//
// Grants are resolved on every reconcile. The project proxy can enforce LAN
// grants today; internet windows and package grants need proxy support it does
// not have yet, so they are refused (never applied) rather than approximated.
// A grant also needs a profile that allows its type, an expiry within the
// profile's limit, and a LAN address the deployment allows. Expired grants
// stop applying at once and are deleted.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"slices"
	"sort"
	"time"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/network"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/proxy"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ProfileSource labels the rules a profile contributes.
func ProfileSource(name string) string { return "profile:" + name }

// ProfileRules expands a profile into labelled, read-only proxy rules:
// registries first, then its own rules.
func ProfileRules(p *api.AccessProfile) ([]api.HTTPSRule, error) {
	if p == nil {
		return nil, nil
	}
	rules, err := proxy.RegistryRules(p.Spec.Registries)
	if err != nil {
		return nil, fmt.Errorf("access profile %s: %w", p.Name, err)
	}
	rules = append(rules, p.Spec.HTTPS...)
	for i := range rules {
		rules[i].Source = ProfileSource(p.Name)
	}
	return rules, nil
}

// errProfileMissing blocks a cell whose profile does not exist: its rules
// cannot be known, so nothing beyond them is assumed.
var errProfileMissing = errors.New("access profile not found")

// accessProfile loads the cell's profile; nil when the cell names none (cells
// from before access profiles keep only their own rules).
func (r *Reconciler) accessProfile(ctx context.Context, w *api.ProjectWorkspace) (*api.AccessProfile, error) {
	if w.Spec.AccessProfileName == "" {
		return nil, nil
	}
	p := &api.AccessProfile{}
	if err := r.Get(ctx, types.NamespacedName{Name: w.Spec.AccessProfileName}, p); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("%w: %s; create it or choose another level", errProfileMissing, w.Spec.AccessProfileName)
		}
		return nil, err
	}
	return p, nil
}

func (r *Reconciler) profileRules(ctx context.Context, w *api.ProjectWorkspace) ([]api.HTTPSRule, error) {
	p, err := r.accessProfile(ctx, w)
	if err != nil {
		return nil, err
	}
	return ProfileRules(p)
}

// GrantDecision is the controller's verdict on one grant.
type GrantDecision struct {
	Grant   *api.AccessGrant
	State   api.AccessGrantState
	Message string
}

// maxDuration returns the profile's limit for a grant type, or 0 for none set.
func maxDuration(p *api.AccessProfile, t api.AccessGrantType) time.Duration {
	if p == nil || p.Spec.MaxGrantDuration == nil {
		return 0
	}
	return p.Spec.MaxGrantDuration[t].Duration
}

// ResolveGrants decides every grant of a cell and returns the LAN hosts the
// live ones add to spec.lanHosts. It never widens beyond what each check
// allows: a refused grant contributes nothing.
func ResolveGrants(w *api.ProjectWorkspace, p *api.AccessProfile, grants []api.AccessGrant, lan network.LANConfig, nodes []netip.Addr, now time.Time) ([]api.LANHost, []GrantDecision) {
	sorted := slices.Clone(grants)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i].CreationTimestamp.Time, sorted[j].CreationTimestamp.Time
		if a.Equal(b) {
			return sorted[i].Name < sorted[j].Name
		}
		return a.Before(b)
	})
	hosts := slices.Clone(w.Spec.LANHosts)
	var decisions []GrantDecision
	refuse := func(g *api.AccessGrant, msg string) {
		decisions = append(decisions, GrantDecision{Grant: g, State: api.AccessGrantRefused, Message: msg})
	}
	for i := range sorted {
		g := &sorted[i]
		switch {
		case g.Spec.Cell != w.Name:
			refuse(g, "grant names another cell")
			continue
		case len(g.OwnerReferences) == 0 || !slices.ContainsFunc(g.OwnerReferences, func(o metav1.OwnerReference) bool { return o.UID == w.UID }):
			refuse(g, "grant is not owned by this cell")
			continue
		case !g.Live(now):
			decisions = append(decisions, GrantDecision{Grant: g, State: api.AccessGrantExpired, Message: "expired"})
			continue
		case p == nil:
			refuse(g, "the cell has no access profile; choose a level first")
			continue
		case !p.Allows(g.Spec.Type):
			refuse(g, fmt.Sprintf("access profile %s does not allow %s grants", p.Name, g.Spec.Type))
			continue
		}
		// The limit counts from creation (from now for an object without one).
		start := g.CreationTimestamp.Time
		if start.IsZero() {
			start = now
		}
		if limit := maxDuration(p, g.Spec.Type); limit <= 0 || g.Spec.ExpiresAt.Sub(start) > limit {
			refuse(g, fmt.Sprintf("expiry is beyond the %s limit of access profile %s", g.Spec.Type, p.Name))
			continue
		}
		switch g.Spec.Type {
		case api.AccessGrantLAN:
			if g.Spec.LAN == nil {
				refuse(g, "lan grant without a host")
				continue
			}
			next, err := mergeLAN(hosts, *g.Spec.LAN)
			if err == nil {
				err = lan.Check(next, nodes)
			}
			if err != nil {
				refuse(g, err.Error())
				continue
			}
			hosts = next
			decisions = append(decisions, GrantDecision{Grant: g, State: api.AccessGrantActive, Message: "applied to the lan-hosts policy"})
		case api.AccessGrantInternet:
			refuse(g, "the project proxy cannot open time-limited internet windows yet; nothing was opened")
		case api.AccessGrantPackage:
			refuse(g, "the project proxy cannot enforce per-package approvals yet; nothing was allowed")
		default:
			refuse(g, "unknown grant type")
		}
	}
	return hosts, decisions
}

// mergeLAN adds a grant to the hosts: a new address, or more ports on one
// already listed.
func mergeLAN(hosts []api.LANHost, g api.LANGrant) ([]api.LANHost, error) {
	out := slices.Clone(hosts)
	for i, h := range out {
		if h.Address != g.Address {
			continue
		}
		ports := slices.Clone(h.Ports)
		for _, p := range g.Ports {
			if !slices.Contains(ports, p) {
				ports = append(ports, p)
			}
		}
		slices.Sort(ports)
		if len(ports) > network.MaxLANPorts {
			return nil, fmt.Errorf("LAN host %s would have more than %d ports", g.Address, network.MaxLANPorts)
		}
		out[i] = api.LANHost{Address: h.Address, Ports: ports, Note: h.Note, AddedBy: h.AddedBy, AddedAt: h.AddedAt}
		return out, nil
	}
	ports := slices.Clone(g.Ports)
	slices.Sort(ports)
	return append(out, api.LANHost{Address: g.Address, Ports: ports, Note: g.Note}), nil
}

// cellGrants lists the grants that name this cell.
func (r *Reconciler) cellGrants(ctx context.Context, w *api.ProjectWorkspace) ([]api.AccessGrant, error) {
	var list api.AccessGrantList
	if err := r.List(ctx, &list, client.InNamespace(w.Namespace), client.MatchingLabels{api.CellLabel: w.Name}); err != nil {
		return nil, err
	}
	return list.Items, nil
}

// effectiveLAN returns spec.lanHosts plus the cell's live LAN grants, and
// the decision on every grant (applyLANHosts records them).
func (r *Reconciler) effectiveLAN(ctx context.Context, w *api.ProjectWorkspace) ([]api.LANHost, []GrantDecision, error) {
	grants, err := r.cellGrants(ctx, w)
	if err != nil {
		return nil, nil, err
	}
	if len(grants) == 0 {
		return w.Spec.LANHosts, nil, nil
	}
	p, err := r.accessProfile(ctx, w)
	if err != nil && !errors.Is(err, errProfileMissing) {
		return nil, nil, err
	}
	var nodes []netip.Addr
	if _, ok := r.Network.(network.LANPolicy); ok && r.LAN.Enabled() {
		if nodes, err = network.NodeAddresses(ctx, r.Client); err != nil {
			return nil, nil, err
		}
	}
	hosts, decisions := ResolveGrants(w, p, grants, r.LAN, nodes, time.Now())
	return hosts, decisions, nil
}

func (r *Reconciler) recordGrant(ctx context.Context, w *api.ProjectWorkspace, d GrantDecision) error {
	g := d.Grant
	if d.State == api.AccessGrantExpired {
		slog.InfoContext(ctx, "access grant expired", "workspace_uid", string(w.UID), "grant", g.Name, "type", string(g.Spec.Type))
		sum := sha256.Sum256([]byte(g.Name + string(g.UID)))
		if err := r.auditDetails(ctx, w, "grant-expired-"+hex.EncodeToString(sum[:6]), map[string]string{"grant": g.Name, "type": string(g.Spec.Type), "message": fmt.Sprintf("Access grant %s (%s) expired and was removed", g.Name, g.Spec.Type)}); err != nil {
			return err
		}
		if err := r.Delete(ctx, g); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		return nil
	}
	if g.Status.State == d.State && g.Status.Message == d.Message && g.Status.ObservedGeneration == g.Generation {
		return nil
	}
	g.Status = api.AccessGrantStatus{State: d.State, Message: d.Message, ObservedGeneration: g.Generation}
	if err := r.Status().Update(ctx, g); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}
