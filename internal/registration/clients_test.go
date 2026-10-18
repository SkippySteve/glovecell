package registration

import (
	"errors"
	"testing"
)

func TestRegisterPlanT3AndOrca(t *testing.T) {
	t3, err := RegisterPlan("t3", "t3", "/mnt/demo", "demo")
	if err != nil {
		t.Fatal(err)
	}
	if got := len(t3.Argv); got != 6 || t3.Argv[0] != "t3" || t3.Argv[3] != "/mnt/demo" {
		t.Fatalf("%v", t3.Argv)
	}
	t.Setenv("T3CODE_HOME", "/var/lib/control-t3/state")
	withHome, err := RegisterPlan("t3", "t3", "/mnt/demo", "demo")
	if err != nil {
		t.Fatal(err)
	}
	if withHome.Argv[len(withHome.Argv)-2] != "--base-dir" || withHome.Argv[len(withHome.Argv)-1] != "/var/lib/control-t3/state" {
		t.Fatalf("%v", withHome.Argv)
	}
	orca, err := RegisterPlan("orca", "orca-ide", "/mnt/demo", "")
	if err != nil {
		t.Fatal(err)
	}
	if orca.Argv[1] != "repo" || orca.Argv[4] != "/mnt/demo" {
		t.Fatalf("%v", orca.Argv)
	}
	if _, err := RegisterPlan("ssh", "x", "/mnt", ""); err == nil {
		t.Fatal("unknown kind")
	}
	if _, err := RegisterPlan("command", "sh", "/mnt", ""); err == nil {
		t.Fatal("command via RegisterPlan")
	}
}

func TestRegisterCommandPlanRejectsShellStrings(t *testing.T) {
	if _, err := RegisterCommandPlan([]string{"rm -rf /; echo pwned"}); err == nil {
		t.Fatal("shell string accepted")
	}
	if _, err := RegisterCommandPlan([]string{"true"}); err != nil {
		t.Fatal(err)
	}
	if _, err := RegisterCommandPlan([]string{"echo", "ok"}); err != nil {
		t.Fatal(err)
	}
}

func TestParseRegisterSuccess(t *testing.T) {
	id, already, err := ParseRegisterSuccess("t3", "listening\nAdded project 7dc24d95-2241-4e16-92f4-b8eb0f6004ce (demo) at /tmp/x.\n", "", nil)
	if err != nil || already || id != "7dc24d95-2241-4e16-92f4-b8eb0f6004ce" {
		t.Fatalf("%q %v %v", id, already, err)
	}
	_, already, err = ParseRegisterSuccess("t3", "", "ProjectAlreadyExistsError: An active project already exists for '/tmp/x'", errors.New("exit 1"))
	if err != nil || !already {
		t.Fatalf("already-exists: %v %v", already, err)
	}
	id, _, err = ParseRegisterSuccess("orca", `{"ok":true,"result":{"repo":{"id":"9ec9698a-e3e7-43b6-94dc-196c27d8ea4d","path":"/tmp/x"}}}`, "", nil)
	if err != nil || id != "9ec9698a-e3e7-43b6-94dc-196c27d8ea4d" {
		t.Fatalf("%q %v", id, err)
	}
}

func TestParseRevokeSuccess(t *testing.T) {
	if err := ParseRevokeSuccess("t3", "Removed project abc (title).", "", nil); err != nil {
		t.Fatal(err)
	}
	if err := ParseRevokeSuccess("t3", "", "ProjectNotFoundError: No active project found for '/tmp/x'", errors.New("exit 1")); err != nil {
		t.Fatal("not-found should be revoked", err)
	}
	if err := ParseRevokeSuccess("t3", "", "boom", errors.New("exit 1")); err == nil {
		t.Fatal("other errors must stay pending")
	}
	if err := ParseRevokeSuccess("orca", `{"ok":true}`, "", nil); err != nil {
		t.Fatal(err)
	}
}

func TestRevokePlan(t *testing.T) {
	t3, err := RevokePlan("t3", "t3", "7dc24d95-2241-4e16-92f4-b8eb0f6004ce")
	if err != nil || t3.Argv[2] != "remove" {
		t.Fatalf("%v %v", t3, err)
	}
	orca, err := RevokePlan("orca", "orca-ide", "9ec9698a-e3e7-43b6-94dc-196c27d8ea4d")
	if err != nil || orca.Argv[2] != "setup-delete" {
		t.Fatalf("%v %v", orca, err)
	}
}
