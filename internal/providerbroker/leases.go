package providerbroker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Lease mode publishes one short-lived access lease per provider to a single
// Secret in the broker's own namespace. The broker has no rights in project
// namespaces: the project controller copies a lease only into the projects
// that opted into that provider (spec.providers), and withdraws it there when
// a project opts out. One owner login therefore serves every opted-in project
// without its refresh material ever leaving the owner.

// LeaseProviders are the providers lease mode can renew (Claude's owner holds
// a long-lived subscription token, so it is read, never renewed).
var LeaseProviders = []string{"grok", "cursor", "codex", "claude"}

const (
	// LeasesLabel binds the journal and the lease Secret to this broker; its
	// value is the lease Secret's name. The broker never adopts other Secrets.
	LeasesLabel = "providers.agentwork.dev/leases"
	// LeasesKey holds the grants: {"grok":{"enabled":true,"access":{...}},...}.
	LeasesKey = "provider-access"
	// cursorSubjectKey pins the Cursor account on first use. A different
	// account is refused until an operator deletes this journal key.
	cursorSubjectKey = "cursor-subject"
	// codexAccountKey pins the ChatGPT account the same way.
	codexAccountKey = "codex-account"
)

// journalBound checks that the renewal journal belongs to this broker.
func (b *Broker) journalBound(journal *corev1.Secret) bool {
	if b.Config.Leases != "" {
		return journal.Labels[LeasesLabel] == b.Config.Leases
	}
	return journal.Labels["providers.agentwork.dev/workspace-uid"] == b.Config.UID
}

// boundSecret returns the named Secret in the broker namespace, creating it
// empty on first use. An existing Secret without this broker's label is
// refused rather than adopted.
func (b *Broker) boundSecret(ctx context.Context, name string) (*corev1.Secret, error) {
	key := types.NamespacedName{Namespace: b.Config.Control, Name: name}
	var secret corev1.Secret
	err := b.Client.Get(ctx, key, &secret)
	if apierrors.IsNotFound(err) {
		secret = corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: b.Config.Control, Labels: map[string]string{LeasesLabel: b.Config.Leases}}, Data: map[string][]byte{}}
		if err = b.Client.Create(ctx, &secret); err != nil && !apierrors.IsAlreadyExists(err) {
			return nil, errors.New("broker state unavailable")
		}
		err = b.Client.Get(ctx, key, &secret)
	}
	if err != nil {
		return nil, errors.New("broker state unavailable")
	}
	if secret.Labels[LeasesLabel] != b.Config.Leases {
		return nil, errors.New("broker state binding changed")
	}
	return &secret, nil
}

// pinCursor records the Cursor account on first use and refuses another one.
func (b *Broker) pinCursor(ctx context.Context, subject string) error {
	return b.pinAccount(ctx, cursorSubjectKey, "Cursor", subject)
}

// pinAccount records a provider's account on first use under key and refuses
// another one until an operator deletes that journal key.
func (b *Broker) pinAccount(ctx context.Context, key, label, subject string) error {
	if subject == "" {
		return errors.New(label + " account unknown")
	}
	journal, err := b.boundSecret(ctx, b.Config.Journal)
	if err != nil {
		return err
	}
	switch pinned := string(journal.Data[key]); pinned {
	case subject:
		return nil
	case "":
		before := journal.DeepCopy()
		if journal.Data == nil {
			journal.Data = map[string][]byte{}
		}
		journal.Data[key] = []byte(subject)
		if b.Client.Patch(ctx, journal, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})) != nil {
			return errors.New(label + " account pin pending")
		}
		return nil
	default:
		return errors.New(label + " enrollment account changed")
	}
}

// currentLeases reads the published grants. Entries this broker would not
// publish are dropped, so a damaged Secret cannot carry anything forward.
func currentLeases(raw []byte) map[string]Grant {
	out := map[string]Grant{}
	var grants map[string]Grant
	if json.Unmarshal(raw, &grants) != nil {
		return out
	}
	for _, name := range LeaseProviders {
		g, ok := grants[name]
		if !ok || !g.Enabled || g.Access == nil || !validAccess(*g.Access) || (name == "codex") != (g.Access.Account != "") || name != "codex" && g.Access.Plan != "" {
			continue
		}
		out[name] = Grant{Enabled: true, Access: &Access{Token: g.Access.Token, Expires: g.Access.Expires, Issuer: g.Access.Issuer, Account: g.Access.Account, Plan: g.Access.Plan}}
	}
	return out
}

// TickLeases renews every configured owner and publishes five-minute leases.
// An owner outage leaves that provider's previous lease to run out while the
// other provider renews. Providers that are not configured publish disabled.
func (b *Broker) TickLeases(ctx context.Context) error {
	if _, err := b.boundSecret(ctx, b.Config.Journal); err != nil {
		return err
	}
	leases, err := b.boundSecret(ctx, b.Config.Leases)
	if err != nil {
		return err
	}
	previous := currentLeases(leases.Data[LeasesKey])
	next := map[string]Grant{}
	now := b.now()
	var failures []error
	for _, name := range LeaseProviders {
		if !slices.Contains(b.Config.Providers, name) {
			next[name] = Grant{Enabled: false}
			continue
		}
		access, err := b.owner(ctx, name)
		if err == nil && name == "cursor" {
			err = b.pinCursor(ctx, access.Subject)
		}
		if err == nil && name == "codex" {
			err = b.pinAccount(ctx, codexAccountKey, "Codex", access.Account)
		}
		if err != nil || !validAccess(access) || access.Expires <= float64(now.Add(time.Minute).Unix()) {
			failures = append(failures, errors.New(name+" credential owner renewal unavailable"))
			if g, ok := previous[name]; ok && g.Access.Expires > float64(now.Unix()) {
				next[name] = g
			} else {
				next[name] = Grant{Enabled: false}
			}
			continue
		}
		lease := Access{Token: access.Token, Expires: math.Min(access.Expires, float64(now.Add(5*time.Minute).Unix())), Issuer: access.Issuer, Account: access.Account, Plan: access.Plan}
		next[name] = Grant{Enabled: true, Access: &lease}
	}
	raw, _ := json.Marshal(next)
	if !bytes.Equal(raw, leases.Data[LeasesKey]) {
		latest, err := b.boundSecret(ctx, b.Config.Leases)
		if err != nil {
			return err
		}
		before := latest.DeepCopy()
		if latest.Data == nil {
			latest.Data = map[string][]byte{}
		}
		latest.Data[LeasesKey] = raw
		if b.Client.Patch(ctx, latest, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})) != nil {
			return errors.New("provider lease publication failed")
		}
	}
	return errors.Join(failures...)
}
