package agentorchestrator

import (
	"fmt"
	"strings"
)

// Session hub slots (docs/agent-orchestrator.md, "Session hub").
//
// AO has no user-defined ACP agent: its Chat mode runs a fixed list of
// harnesses, each launched from the executable its plugin finds on PATH. In a
// cell with a session hub, Glovecell puts a small script in front of some of
// those harnesses ("slots"). When AO starts such a harness for Chat, the
// script runs the session hub connector for one hub agent instead:
//
//	agent-project-controller session-hub-acp --agent <hub agent id>
//
// Any other use of a slot (the terminal agent, version or model probes) is
// refused. The scripts live read-only in the image under SlotDir/<slot>/.
// Before the daemon starts, the worker reads the hub's allowlist from
// AgentsFile and gives the slots, in order, to the first allowlisted agents.
// Without the file (no hub in this cell) nothing changes.

// SlotDir holds one directory per slot, each with one script named after the
// harness executable (Dockerfile target ao-workspace).
const SlotDir = "/opt/ao/session-hub"

// AgentsFile is the hub's allowlist as mounted into the workspace container
// of a session-hub cell: a JSON array of {"id", "displayName", ...}. Only id
// is used here.
const AgentsFile = "/run/session-hub-client/agents.json"

// Slot is one AO Chat harness that can carry a hub agent. Executable is the
// name AO's plugin looks up on PATH; ACPArgs is how AO 0.15.2 launches it for
// Chat (the script checks the same thing). Re-check both when AO_VERSION
// moves.
type Slot struct {
	Name       string
	Executable string
	Label      string
	ACPArgs    string
}

// Slots are the harnesses checked against AO 0.15.2, in the order they are
// given to allowlisted agents. Their Chat launch has no version probe, no
// check of the agent's name and writes no files into the repository, so the
// connector can stand in for them.
var Slots = []Slot{
	{Name: "droid", Executable: "droid", Label: "Droid", ACPArgs: "… exec --output-format acp-daemon …"},
	{Name: "dsh", Executable: "dsh", Label: "DeepSeek Harness", ACPArgs: "--profile acp"},
}

// SlotEnv names the daemon's environment variable that maps a slot to a hub
// agent id, for example GLOVECELL_AO_HUB_DROID=cell-claude. The worker
// command sets it in the daemon's process only; it is never part of the pod.
func SlotEnv(slot string) string {
	return "GLOVECELL_AO_HUB_" + strings.ToUpper(slot)
}

// HubWorkerCommand is WorkerCommand for a cell created with a session hub.
// Before the daemon starts it reads AgentsFile, gives each slot (in order)
// the next allowlisted agent id, puts that slot first on PATH, and turns off
// AO's harness update checks and managed installs: a managed install would
// take precedence over PATH and replace the slot. Without the file, or with
// no usable id in it, the daemon starts exactly as in WorkerCommand.
func HubWorkerCommand() []string {
	return workerCommand(hubPrelude(AgentsFile))
}

// hubPrelude reads at most len(Slots) ids that match the hub's id pattern.
// set -- reuses the script's (empty) positional parameters as the list.
func hubPrelude(agentsFile string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "hub=\nset --\nif [ -r %[1]s ]; then\n  set -- $(jq -r 'if type == \"array\" then .[] | objects | .id | strings else empty end' %[1]s 2>/dev/null | grep -E '^[a-z][a-z0-9-]{0,31}$' | head -n %[2]d)\nfi\n", agentsFile, len(Slots))
	for i, s := range Slots {
		fmt.Fprintf(&b, "if [ -n \"${%[1]d:-}\" ]; then export %[2]s=\"$%[1]d\"; PATH=%[3]s/%[4]s:$PATH; hub=1; fi\n", i+1, SlotEnv(s.Name), SlotDir, s.Name)
	}
	b.WriteString("set --\nif [ -n \"$hub\" ]; then export PATH AO_HARNESS_UPDATE_CHECKS=off AO_MANAGED_HARNESS_INSTALLS=off; fi\n")
	return b.String()
}
