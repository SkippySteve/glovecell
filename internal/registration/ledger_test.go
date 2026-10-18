package registration

import "testing"

func TestLedgerOwnerExclusive(t *testing.T) {
	l := NewLedger()
	if err := l.ClaimOwner("/workspace/worktrees/a", "agent-1"); err != nil {
		t.Fatal(err)
	}
	if err := l.ClaimOwner("/workspace/worktrees/a", "agent-1"); err != nil {
		t.Fatal("same owner reclaim failed", err)
	}
	if err := l.ClaimOwner("/workspace/worktrees/a", "agent-2"); err == nil {
		t.Fatal("second owner accepted")
	}
	if err := l.ReleaseOwner("/workspace/worktrees/a", "agent-2"); err == nil {
		t.Fatal("foreign release accepted")
	}
	if err := l.ReleaseOwner("/workspace/worktrees/a", "agent-1"); err != nil {
		t.Fatal(err)
	}
	if err := l.ClaimOwner("/workspace/worktrees/a", "agent-2"); err != nil {
		t.Fatal(err)
	}
}

func TestLedgerUnknownKindAndStateFailClosed(t *testing.T) {
	l := NewLedger()
	if err := l.SetClient("ssh", StateRegistered, "id", "/m"); err == nil {
		t.Fatal("unknown kind accepted")
	}
	if err := l.SetClient("t3", "done", "id", "/m"); err == nil {
		t.Fatal("unknown state accepted")
	}
}

func TestRemovalBlockedSemantics(t *testing.T) {
	l := NewLedger()
	if blocked, err := l.RemovalBlocked(); blocked || err != nil {
		t.Fatalf("empty ledger blocked: %v %v", blocked, err)
	}
	_ = l.SetClient("t3", StatePending, "", "/m")
	if blocked, err := l.RemovalBlocked(); !blocked || err == nil {
		t.Fatal("pending must block")
	}
	_ = l.SetClient("t3", StateRegistered, "id", "/m")
	if blocked, err := l.RemovalBlocked(); !blocked || err == nil {
		t.Fatal("registered must block")
	}
	_ = l.SetClient("t3", StateRevoked, "id", "/m")
	if blocked, err := l.RemovalBlocked(); blocked || err != nil {
		t.Fatalf("revoked must not block: %v %v", blocked, err)
	}
	l.Clients["mystery"] = ClientRecord{Kind: "mystery", State: StateRegistered}
	if blocked, err := l.RemovalBlocked(); !blocked || err == nil {
		t.Fatal("unknown kind must block")
	}
	delete(l.Clients, "mystery")
	l.Clients["t3"] = ClientRecord{Kind: "t3", State: "weird"}
	if blocked, err := l.RemovalBlocked(); !blocked || err == nil {
		t.Fatal("unknown state must block")
	}
}

func TestLedgerConfigMapRoundTrip(t *testing.T) {
	l := NewLedger()
	_ = l.SetClient("t3", StateRegistered, "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", "/home/steve/.local/share/agent-project-controller/mounts/demo")
	_ = l.SetClient("orca", StatePending, "", "/home/steve/.local/share/agent-project-controller/mounts/demo")
	_ = l.ClaimOwner("/workspace/worktrees/a", "agent-1")
	data := EncodeConfigMapData(l)
	if data["purpose"] != "client-registration" {
		t.Fatal(data["purpose"])
	}
	if name := ConfigMapName("071ae7d4-9f18-4c40-b58f-17c23a9c2117"); name != "clients-071ae7d49f184c40b58f17c23a9c2117" {
		t.Fatal(name)
	}
	got, err := DecodeConfigMapData(data)
	if err != nil {
		t.Fatal(err)
	}
	if got.Clients["t3"].ID != l.Clients["t3"].ID || got.Clients["orca"].State != StatePending {
		t.Fatalf("%+v", got.Clients)
	}
	if got.Owners["/workspace/worktrees/a"] != "agent-1" {
		t.Fatal(got.Owners)
	}
}
