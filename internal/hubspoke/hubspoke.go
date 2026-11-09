// Package hubspoke connects phones and desktops to project sandboxes through
// one central hub, while agents run only inside each spoke.
//
// A Runtime is the agent UI server that runs inside a spoke next to its agents
// (Orca's orcad today; T3 and Herdr later). An Enroller is how the hub gains
// access to that spoke so phones reach it through the hub without a new
// pairing scan per sandbox.
//
// Security model (qualified against a local Docker spike, 2026-11-08):
//
//   - Agents and files stay in the spoke (gVisor, least privilege).
//   - The hub runs no project agents; it only enrolls spokes and relays phones.
//   - Pairing offers are one-time credentials and must not be logged.
//   - New spokes appear on an already-paired phone once the hub enrolls and
//     connects them (Orca desktop relay); a running hub must Connect after Add.
package hubspoke

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Offer is a one-time enrollment credential from a spoke Runtime. Treat it
// like a password: never write it to logs, status, Secrets that are not the
// hub's private store, or chat.
type Offer struct {
	Kind     string
	URL      string
	Endpoint string
	DeviceID string
	Scope    string
	Expires  time.Time
}

// Enrolled is one spoke the hub has saved.
type Enrolled struct {
	ID   string
	Name string
}

// Runtime is the server that runs inside a spoke beside the agents.
type Runtime interface {
	Kind() string
	// ParseOffer extracts a runtime-scope enrollment credential from the
	// spoke's ready JSON (for example orcad's orca_server_ready line).
	ParseOffer(readyJSON []byte) (Offer, error)
}

// Enroller is how the hub gains access to a spoke.
type Enroller interface {
	Kind() string
	Enroll(ctx context.Context, name string, offer Offer) (Enrolled, error)
	// Connect opens a live session to a saved spoke. Orca's desktop relay
	// only lists connected servers to phones; Add alone is not enough.
	Connect(ctx context.Context, id string) error
	RegisterProject(ctx context.Context, id, path string) error
	Revoke(ctx context.Context, nameOrID string) error
	List(ctx context.Context) ([]Enrolled, error)
}

// Runner runs argv and returns combined stdout (for JSON CLIs).
type Runner func(ctx context.Context, argv []string) (stdout string, err error)

// ErrNotImplemented marks a Runtime or Enroller that is designed in but not
// verified yet (T3 linked environments, Herdr).
var ErrNotImplemented = errors.New("hubspoke kind not implemented")

// ValidateOffer refuses empty or non-runtime offers before they reach an Enroller.
func ValidateOffer(o Offer) error {
	if o.URL == "" {
		return errors.New("pairing offer URL required")
	}
	if o.Scope != "" && o.Scope != "runtime" {
		return fmt.Errorf("pairing offer scope %q is not runtime", o.Scope)
	}
	return nil
}
