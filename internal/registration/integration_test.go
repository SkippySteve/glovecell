package registration

import (
	"errors"
	"strings"
	"testing"
)

func TestResolveClientsBuiltin(t *testing.T) {
	clients, err := ResolveClients(Options{T3Binary: "t3", OrcaBinary: "orca-ide"}, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if len(clients) != 2 || clients[0].Adapter.Kind() != "t3" || clients[1].Adapter.Kind() != "orca" {
		t.Fatalf("%+v", clients)
	}
}

func TestResolveClientsCustomCommand(t *testing.T) {
	env := map[string]string{
		"APC_CLIENT_DEMO_BINARY":   "/usr/bin/demo",
		"APC_CLIENT_DEMO_REGISTER": "add,{path},--title,{title}",
		"APC_CLIENT_DEMO_REVOKE":   "remove,{id}",
	}
	clients, err := ResolveClients(Options{Integrations: []string{"demo"}}, func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if len(clients) != 1 || clients[0].Adapter.Kind() != "demo" {
		t.Fatalf("%+v", clients)
	}
	plan, err := clients[0].Adapter.RegisterPlan("/usr/bin/demo", "/mnt/x", "x")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(plan.Argv, " "); got != "/usr/bin/demo add /mnt/x --title x" {
		t.Fatal(got)
	}
	rev, err := clients[0].Adapter.RevokePlan("/usr/bin/demo", "abc")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(rev.Argv, " "); got != "/usr/bin/demo remove abc" {
		t.Fatal(got)
	}
	if !KnownKind("demo") {
		t.Fatal("custom kind not registered")
	}
}

func TestResolveClientsRejectsBareCommandKind(t *testing.T) {
	if _, err := ResolveClients(Options{Integrations: []string{"command"}}, func(string) string { return "" }); err == nil {
		t.Fatal("expected error")
	}
}

func TestSyncRegisterAdapterCustom(t *testing.T) {
	a := commandAdapter{
		kind:     "tool",
		register: []string{"reg", "{path}"},
		revoke:   []string{"unreg", "{id}"},
	}
	MustRegisterAdapter(a)
	l := NewLedger()
	var calls []string
	run := func(argv []string) (string, string, error) {
		calls = append(calls, strings.Join(argv, " "))
		if strings.Contains(calls[len(calls)-1], "reg ") {
			return "id-1\n", "", nil
		}
		return "", "", nil
	}
	if err := SyncRegisterAdapter(l, a, "toolbin", "/mnt/p", "p", run); err != nil {
		t.Fatal(err)
	}
	if l.Clients["tool"].ID != "id-1" || l.Clients["tool"].State != StateRegistered {
		t.Fatalf("%+v", l.Clients["tool"])
	}
	if err := SyncRevokeAdapter(l, a, "toolbin", run); err != nil {
		t.Fatal(err)
	}
	if l.Clients["tool"].State != StateRevoked {
		t.Fatal(l.Clients["tool"].State)
	}
	if len(calls) != 2 || calls[0] != "toolbin reg /mnt/p" || calls[1] != "toolbin unreg id-1" {
		t.Fatalf("%v", calls)
	}
}

func TestExpandTemplateRejectsShell(t *testing.T) {
	if _, err := expandTemplate([]string{"echo;rm"}, map[string]string{}); err == nil {
		t.Fatal("expected reject")
	}
	if _, err := expandTemplate([]string{"{missing}"}, map[string]string{}); err == nil {
		t.Fatal("expected unresolved")
	}
}

func TestCommandParseRegisterFailure(t *testing.T) {
	a := commandAdapter{kind: "x", register: []string{"a"}, revoke: []string{"b"}}
	if _, _, err := a.ParseRegister("", "no", errors.New("exit 1")); err == nil {
		t.Fatal("expected error")
	}
}
