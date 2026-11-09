package hubspoke

import (
	"context"
	"errors"
	"fmt"
	"strings"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// Ledger states for one spoke enrollment. Pairing URLs are never stored here.
const (
	StateAbsent   = "absent"
	StatePending  = "pending"
	StateEnrolled = "enrolled"
	StateRevoked  = "revoked"
)

// LedgerRecord is the hubspoke enrollment ack for one workspace and kind.
type LedgerRecord struct {
	Kind  string
	State string
	ID    string
	Name  string
	// Projects are absolute spoke paths already registered with the enroller
	// (primary /workspace/repo plus /workspace/projects/<name> enrollments).
	Projects []string
}

// ConfigMapName is hubspoke-<uid-without-dashes> in the control namespace.
func ConfigMapName(uid string) string {
	return "hubspoke-" + strings.ReplaceAll(uid, "-", "")
}

// DecodeLedger reads kind.state / kind.id / kind.name keys.
func DecodeLedger(data map[string]string) map[string]LedgerRecord {
	out := map[string]LedgerRecord{}
	if data == nil {
		return out
	}
	for k, v := range data {
		kind, field, ok := strings.Cut(k, ".")
		if !ok || kind == "" || kind == "purpose" {
			continue
		}
		rec := out[kind]
		rec.Kind = kind
		switch field {
		case "state":
			rec.State = v
		case "id":
			rec.ID = v
		case "name":
			rec.Name = v
		case "projects":
			rec.Projects = splitProjects(v)
		}
		out[kind] = rec
	}
	return out
}

func splitProjects(v string) []string {
	if v == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(v, "\n") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func joinProjects(paths []string) string {
	var clean []string
	seen := map[string]bool{}
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		clean = append(clean, p)
	}
	return strings.Join(clean, "\n")
}

func encodeLedger(records map[string]LedgerRecord) map[string]string {
	data := map[string]string{"purpose": "hubspoke-enrollment"}
	for kind, rec := range records {
		if rec.State != "" {
			data[kind+".state"] = rec.State
		}
		if rec.ID != "" {
			data[kind+".id"] = rec.ID
		}
		if rec.Name != "" {
			data[kind+".name"] = rec.Name
		}
		if joined := joinProjects(rec.Projects); joined != "" {
			data[kind+".projects"] = joined
		}
	}
	return data
}

// SetState writes one kind's enrollment state. It never stores pairing URLs.
func SetState(ctx context.Context, c client.Client, controlNS, uid, kind string, rec LedgerRecord) error {
	if c == nil || controlNS == "" || uid == "" || kind == "" {
		return errors.New("hubspoke ledger write requires client, namespace, uid and kind")
	}
	if rec.State != StatePending && rec.State != StateEnrolled && rec.State != StateRevoked && rec.State != StateAbsent {
		return fmt.Errorf("invalid hubspoke state %q", rec.State)
	}
	rec.Kind = kind
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: ConfigMapName(uid), Namespace: controlNS}}
	_, err := controllerutil.CreateOrUpdate(ctx, c, cm, func() error {
		if cm.Labels == nil {
			cm.Labels = map[string]string{}
		}
		cm.Labels[api.OwnerLabel] = uid
		records := DecodeLedger(cm.Data)
		records[kind] = rec
		cm.Data = encodeLedger(records)
		return nil
	})
	return err
}

// GetState returns the record for kind, or absent.
func GetState(ctx context.Context, c client.Reader, controlNS, uid, kind string) (LedgerRecord, error) {
	if c == nil || controlNS == "" || uid == "" {
		return LedgerRecord{}, errors.New("hubspoke ledger read unavailable")
	}
	cm := &corev1.ConfigMap{}
	err := c.Get(ctx, client.ObjectKey{Namespace: controlNS, Name: ConfigMapName(uid)}, cm)
	if apierrors.IsNotFound(err) {
		return LedgerRecord{Kind: kind, State: StateAbsent}, nil
	}
	if err != nil {
		return LedgerRecord{}, err
	}
	if cm.Labels[api.OwnerLabel] != uid {
		return LedgerRecord{}, errors.New("hubspoke ledger ownership mismatch")
	}
	rec, ok := DecodeLedger(cm.Data)[kind]
	if !ok || rec.State == "" {
		return LedgerRecord{Kind: kind, State: StateAbsent}, nil
	}
	return rec, nil
}
