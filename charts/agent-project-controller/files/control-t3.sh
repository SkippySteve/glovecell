#!/bin/sh
# Runs in one container so T3 and the FUSE mounts share a mount namespace.
set -eu
state=/var/lib/control-t3
mkdir -p "$state/state" "$state/mounts" "$state/bin"
t3=${APC_T3_BINARY:-/opt/t3/t3}
if [ ! -x "$t3" ] && [ -x "$state/bin/t3" ]; then
  t3=$state/bin/t3
fi
registrar=${APC_REGISTRAR_BINARY:-/usr/local/bin/agent-project-controller}
if [ ! -x "$registrar" ] && [ -x "$state/bin/agent-project-controller" ]; then
  registrar=$state/bin/agent-project-controller
fi
if [ ! -x "$t3" ]; then
  echo "control T3 binary missing at $t3 and $state/bin/t3" >&2
  exit 1
fi
if [ ! -x "$registrar" ]; then
  echo "registrar binary missing at $registrar and $state/bin/agent-project-controller" >&2
  exit 1
fi
if ! command -v git >/dev/null 2>&1 && command -v apk >/dev/null 2>&1; then
  apk add --no-cache git
fi
if command -v git >/dev/null 2>&1; then
  git config --global safe.directory '*'
fi
export T3CODE_HOME="$state/state"
"$t3" serve --host 0.0.0.0 --port 8788 --base-dir "$state/state" --no-browser &
t3pid=$!
trap 'kill "$t3pid" 2>/dev/null || true' EXIT INT TERM
i=0
while [ "$i" -lt 50 ]; do
  if ! kill -0 "$t3pid" 2>/dev/null; then
    echo "control T3 exited before it was ready" >&2
    exit 1
  fi
  # bash /dev/tcp is not required; a connected python probe is enough.
  if python3 -c 'import socket,sys; s=socket.create_connection(("127.0.0.1",8788),2); s.close()' 2>/dev/null; then
    break
  fi
  i=$((i + 1))
  sleep 0.2
done
export APC_T3_BINARY="$t3"
export APC_MOUNT_ROOT="$state/mounts"
export CLIENT_INTEGRATIONS="${CLIENT_INTEGRATIONS:-t3}"
exec "$registrar" register --watch
