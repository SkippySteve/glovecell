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

// State is where one spoke enrollment stands. Pairing URLs are never stored
// in the ledger.
type State string

const (
	StateAbsent   State = "absent"
	StatePending  State = "pending"
	StateEnrolled State = "enrolled"
	StateRevoked  State = "revoked"
)

// Valid reports whether s is one of the four ledger states.
func (s State) Valid() bool {
	switch s {
	case StateAbsent, StatePending, StateEnrolled, StateRevoked:
		return true
	}
	return false
}

// transitions are the moves the enrollment flow (watch.go) makes. Pending is
// "enrollment under way"; enrolled may stay enrolled while projects are
// added; revoked is final, so a revoked spoke never re-enrolls on its own.
var transitions = map[State][]State{
	StateAbsent:   {StatePending},
	StatePending:  {StatePending, StateEnrolled, StateRevoked},
	StateEnrolled: {StateEnrolled, StatePending, StateRevoked},
	StateRevoked:  {},
}

// To returns r moved to next. It keeps the hub identity (ID, Name) and the
// registered project paths, so a step that fails part-way never forgets what
// the hub already holds. A move the flow never makes is an error.
func (r LedgerRecord) To(next State) (LedgerRecord, error) {
	from := r.State
	if from == "" {
		from = StateAbsent
	}
	for _, allowed := range transitions[from] {
		if allowed == next {
			r.State = next
			r.Projects = append([]string(nil), r.Projects...)
			return r, nil
		}
	}
	return r, fmt.Errorf("hubspoke %s cannot move from %s to %s", r.Kind, from, next)
}

// LedgerRecord is the hubspoke enrollment ack for one workspace and kind.
type LedgerRecord struct {
	Kind  string
	State State
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
			rec.State = State(v)
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
			data[kind+".state"] = string(rec.State)
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
	if !rec.State.Valid() {
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
