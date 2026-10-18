package registration

import (
	"fmt"
	"strings"
)

// ClientConfig names a control-host client binary for register/revoke.
type ClientConfig struct {
	Kind   string
	Binary string
}

// SyncRegister marks pending, runs the client register plan, then records the ack.
func SyncRegister(ledger *Ledger, kind, binary, mountPath, title string, run Runner) error {
	a, ok := LookupAdapter(kind)
	if !ok {
		return fmt.Errorf("unknown client kind %q", kind)
	}
	return SyncRegisterAdapter(ledger, a, binary, mountPath, title, run)
}

// SyncRegisterAdapter registers through an explicit Adapter.
func SyncRegisterAdapter(ledger *Ledger, a Adapter, binary, mountPath, title string, run Runner) error {
	if ledger == nil {
		return fmt.Errorf("ledger required")
	}
	if a == nil {
		return fmt.Errorf("adapter required")
	}
	kind := a.Kind()
	if existing, ok := ledger.Clients[kind]; ok && existing.State == StateRegistered && existing.Path == mountPath && existing.ID != "" {
		return nil
	}
	if err := ledger.SetClient(kind, StatePending, "", mountPath); err != nil {
		return err
	}
	plan, err := a.RegisterPlan(binary, mountPath, title)
	if err != nil {
		return err
	}
	stdout, stderr, runErr := run(plan.Argv)
	id, already, err := a.ParseRegister(stdout, stderr, runErr)
	if err != nil {
		return err
	}
	if already && id == "" {
		// Keep a stable revoke handle when the tool reports already-present without an id.
		id = mountPath
	}
	return ledger.SetClient(kind, StateRegistered, id, mountPath)
}

// SyncRevoke runs revoke and records revoked on success (including already gone).
func SyncRevoke(ledger *Ledger, kind, binary string, run Runner) error {
	a, ok := LookupAdapter(kind)
	if !ok {
		return fmt.Errorf("unknown client kind %q", kind)
	}
	return SyncRevokeAdapter(ledger, a, binary, run)
}

// SyncRevokeAdapter revokes through an explicit Adapter.
func SyncRevokeAdapter(ledger *Ledger, a Adapter, binary string, run Runner) error {
	if ledger == nil {
		return fmt.Errorf("ledger required")
	}
	if a == nil {
		return fmt.Errorf("adapter required")
	}
	kind := a.Kind()
	rec := ledger.Clients[kind]
	if rec.State == StateAbsent || rec.State == StateRevoked {
		return ledger.SetClient(kind, StateRevoked, rec.ID, rec.Path)
	}
	id := rec.ID
	if id == "" {
		id = rec.Path
	}
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("client %q has no registration id", kind)
	}
	if err := ledger.SetClient(kind, StatePending, id, rec.Path); err != nil {
		return err
	}
	plan, err := a.RevokePlan(binary, id)
	if err != nil {
		return err
	}
	stdout, stderr, runErr := run(plan.Argv)
	if err := a.ParseRevoke(stdout, stderr, runErr); err != nil {
		return err
	}
	return ledger.SetClient(kind, StateRevoked, id, rec.Path)
}
