# Gateway runtime health pilot

This implements the opt-in runtime guard for APC #108. It is disabled by
default and does not change generated pod templates or existing readiness
probes. Do not mark #108 complete from these unit tests.

With `APC_PROXY_RUNTIME_GUARD=true`, the gateway requires
`APC_PROXY_STATE_DIR` to name a writable **pod-scoped emptyDir**. It arms a
marker before serving. Restarting the container, even without an observed
fault, requires replacing the pod. A PVC would prevent replacement recovery;
a container filesystem would allow restart to bypass the latch. Neither is
supported. The state directory must be inaccessible to workers.

The guard checks mounted inputs before CONNECT, each inspected HTTP/1.1 and
HTTP/2 request, outbound round trips and loopback control operations. A
one-second watcher covers idle tunnels. Policy and CA bytes must still match
the inputs actually loaded at startup. Malformed or unreadable optional
credential documents quarantine the proxy. Once an optional file has been
observed, disappearance also quarantines it. Initially absent optional inputs
remain permitted for features that are not configured; the existing route
handlers still require their credentials when those routes are used.

Quarantine cancels tunnel contexts, closes tracked connections, makes readiness
fail and emits one reason-code log without input contents. Restoring a file or
restarting the control listener cannot clear quarantine. The pre-armed marker
retains the recovery requirement even if a subsequent marker write fails.
Credential rotation with valid documents remains supported. Expired/revoked
leases deny their routes through the existing authorization checks; expiration
alone does not quarantine unrelated traffic. Project policy or CA changes
require pod replacement in this mode.

`GET http://127.0.0.1:3129/ready` and
`agent-project-controller proxy-ready` expose readiness without credentials.
The CLI bypasses environment proxies and has a two-second timeout. Binding the
control listener is now a startup prerequisite in both modes, rather than a
warning followed by continued forwarding. No new listener is exposed to cells.

## Before activation

This is substantial security code and needs one independent reviewer, launched
only at Steve's request. Required CI and Steve's merge gate remain in place.
Activation is a separate PR after disposable qualification:

1. Add the emptyDir and proxy-only mount to generated pod templates behind an
   explicit controller switch. Qualify it on the supported runtime.
2. Use the `proxy-ready` exec probe. Test loss of the control listener, all
   tracked inputs, a corrupt marker and inability to persist state.
3. Inject each fault during active HTTP/1.1 and HTTP/2 streaming; require tunnel
   closure, no further forwarding and one reason-code audit record. Test valid
   credential rotation and disabled providers without unnecessary incidents.
4. Ensure legitimate policy edits cause a controller-driven pod replacement,
   with policies installed before workers resume. Do not widen the network.
5. Surface quarantine as a project incident in the controller/console. Do not
   describe the pod as merely starting. The console incident projection and
   generated probe/mount wiring are not implemented by this pilot.

File reads are not an atomic snapshot across Kubernetes projected volumes.
Per-route authorization continues to reread current credentials; the pilot is
an extra denial mechanism, not a replacement for those checks. Qualify update
races and interrupted operations before enabling it on a usable cell.
