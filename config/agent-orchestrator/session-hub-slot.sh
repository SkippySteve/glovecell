#!/bin/sh
# Agent Orchestrator session hub slot (docs/agent-orchestrator.md, "Session hub").
#
# Installed as /opt/ao/session-hub/<slot>/<executable>. The worker puts that
# directory first on the AO daemon's PATH only when GLOVECELL_AO_HUB_<SLOT>
# names a hub agent. When AO starts this harness for Chat, run the session hub
# connector for that agent; refuse everything else (the terminal agent,
# version and model probes), so the slot never runs anything but the hub.
# The Chat argument shapes are AO 0.15.2's; re-check them when AO_VERSION moves.
set -eu

slot=$(basename "$(dirname "$0")")
acp=
case "$slot" in
droid)
	# droidacp: droid [--settings FILE] exec --output-format acp-daemon [...]
	agent=${GLOVECELL_AO_HUB_DROID:-}
	prev=
	for arg in "$@"; do
		if [ "$prev" = "--output-format" ] && [ "$arg" = "acp-daemon" ]; then acp=1; fi
		prev=$arg
	done
	;;
dsh)
	# deepseekharnessacp: dsh --profile acp
	agent=${GLOVECELL_AO_HUB_DSH:-}
	if [ "$#" -eq 2 ] && [ "$1" = "--profile" ] && [ "$2" = "acp" ]; then acp=1; fi
	;;
*)
	echo "glovecell: unknown Agent Orchestrator slot '$slot'" >&2
	exit 127
	;;
esac

case "$agent" in
"" | *[!a-z0-9-]*)
	echo "glovecell: the '$slot' slot is not mapped to a session hub agent in this cell" >&2
	exit 127
	;;
esac
if [ -z "$acp" ]; then
	echo "glovecell: in this cell the '$slot' harness is the session hub agent '$agent'; start it as a Chat session" >&2
	exit 64
fi
exec /usr/local/bin/agent-project-controller session-hub-acp --agent "$agent"
