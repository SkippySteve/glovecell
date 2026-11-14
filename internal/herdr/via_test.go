package herdr

import (
	"slices"
	"testing"
)

// The hub's line is ignored: the worker always runs the same argv, with the
// state directories the worker's `hc server` uses.
func TestConnectCommandIsFixed(t *testing.T) {
	t.Parallel()
	want := []string{"env", "HOME=/workspace/home", "XDG_RUNTIME_DIR=/workspace/herdr", "herdr-core", "connect"}
	if got := ConnectCommand(); !slices.Equal(got, want) {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestMachineViaCommand(t *testing.T) {
	t.Parallel()
	if got := MachineViaCommand("scrapecast"); got != "agent-project-controller herdr-via scrapecast" {
		t.Fatalf("got %q", got)
	}
}
