package registration

import (
	"errors"
	"strings"
	"testing"
)

func TestSyncRegisterAndRevokeT3(t *testing.T) {
	l := NewLedger()
	var calls [][]string
	run := func(argv []string) (string, string, error) {
		calls = append(calls, append([]string{}, argv...))
		joined := strings.Join(argv, " ")
		if strings.Contains(joined, "project add") {
			return "Added project 7dc24d95-2241-4e16-92f4-b8eb0f6004ce (demo) at /mnt/demo\n", "", nil
		}
		if strings.Contains(joined, "project remove") {
			return "Removed project 7dc24d95-2241-4e16-92f4-b8eb0f6004ce (demo).\n", "", nil
		}
		return "", "unexpected", errors.New("exit 1")
	}
	if err := SyncRegister(l, "t3", "t3", "/mnt/demo", "demo", run); err != nil {
		t.Fatal(err)
	}
	if l.Clients["t3"].State != StateRegistered || l.Clients["t3"].ID == "" {
		t.Fatalf("%+v", l.Clients["t3"])
	}
	if err := SyncRevoke(l, "t3", "t3", run); err != nil {
		t.Fatal(err)
	}
	if l.Clients["t3"].State != StateRevoked {
		t.Fatal(l.Clients["t3"].State)
	}
	if blocked, _ := l.RemovalBlocked(); blocked {
		t.Fatal("revoked still blocks")
	}
}

func TestSyncRegisterSkipsIdenticalRegistration(t *testing.T) {
	l := NewLedger()
	_ = l.SetClient("t3", StateRegistered, "7dc24d95-2241-4e16-92f4-b8eb0f6004ce", "/mnt/demo")
	calls := 0
	if err := SyncRegister(l, "t3", "t3", "/mnt/demo", "demo", func([]string) (string, string, error) {
		calls++
		return "", "", nil
	}); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatal("re-registered identical path")
	}
}

func TestSyncRegisterInterruptedStaysPending(t *testing.T) {
	l := NewLedger()
	err := SyncRegister(l, "t3", "t3", "/mnt/demo", "demo", func([]string) (string, string, error) {
		return "", "boom", errors.New("exit 1")
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if l.Clients["t3"].State != StatePending {
		t.Fatal(l.Clients["t3"].State)
	}
	if blocked, _ := l.RemovalBlocked(); !blocked {
		t.Fatal("pending must block removal")
	}
}
