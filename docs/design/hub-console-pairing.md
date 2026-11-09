# Design: console Hub page (operator pairing)

**Status:** accepted direction (Steve, 2026-11-09). Implementation:
[APC #113](https://git.cluster.aimingupward.com/steve/agent-project-controller/issues/113).

**Related:** [hubspoke](../hubspoke.md), [console](../console.md),
[security](../security.md).

## Problem

Phones and desktops are meant to pair **once** with the hub. Minting that offer
today requires `kubectl exec` into the hub pod and DevTools IPC
(`getPairingQR` / `getRuntimePairingUrl`). That is fine as a spike, not as the
product path for Steve or for OSS operators.

## Decision

**A — Hub page in the controller console.** The console asks the hub for phone
or runtime offers over an authenticated control path. Not a CLI-only helper (B)
and not full remote-server management in the console (C).

## Product surface

Cluster-level route `#/hub` (not per-project). When hubspoke is enabled and the
hub is addressable, `GET /api/v1/session` reports `hub: true` and the nav shows
**Hub**.

| Card | Action |
| --- | --- |
| Pair phone | Mint mobile offer; show QR + expiry |
| Pair desktop | Mint runtime offer; show `orca://pair…` and a copyable `orca environment add …` line |
| Spokes | Table from hubspoke ledgers (`enrolled` / `revoked`), never pairing URLs |
| Devices | List / revoke paired phones and desktops when hub IPC supports it |

Pairing material is shown **once**, with a countdown from `expiresAt`, and
cleared when leaving the page. Treat the offer as a credential.

## API

OIDC operator only (same rule as LAN hosts / uploads). Automation bearer and
static login get 403.

| Method | Path | Behaviour |
| --- | --- | --- |
| `GET` | `/api/v1/hub` | Readiness, public WebSocket endpoint, spoke ledger summary |
| `POST` | `/api/v1/hub/pairing` | Body `{"scope":"mobile"\|"runtime","rotate":true}`. Response is the only copy of the offer |
| `GET` | `/api/v1/hub/devices` | List devices (follow-up if IPC incomplete) |
| `DELETE` | `/api/v1/hub/devices/{id}` | Revoke one device |

No `GET` that returns a previously minted offer. Nothing writes `pairingUrl` /
`qrDataUrl` to ConfigMaps, status, logs, or audit bodies (actor + scope +
result only).

## Control path to the hub

Wrap existing desktop IPC behind a small admin HTTP inside the hub pod
(loopback or NetworkPolicy-locked). The controller calls it with a projected
token. Do **not** shell out from the controller for pairing, and do **not**
expose the admin port on Traefik public routes.

Emergency fallback remains the kubectl helper in the hub README; document it as
operator-emergency only once the console path ships.

## Security properties

- Pairing URLs never enter hubspoke ledgers (existing rule).
- Short TTL; rotate on each mint.
- Audit who minted / revoked, never the offer string.
- A compromised spoke still cannot open another spoke; this page only minting
  hub client offers does not change that boundary.

## Acceptance

1. OIDC operator mints phone and desktop offers from `#/hub` without kubectl.
2. Offer never appears in controller logs, ledger ConfigMaps, or history GETs.
3. Spoke table matches hubspoke ledger states.
4. Docs updated (`hubspoke.md`, `console.md`); kubectl QR path marked emergency.

## Implementation order

1. Hub-local admin pairing endpoint (IPC wrap) + NetworkPolicy.
2. Controller proxy handlers + session bit + redaction tests.
3. Console `#/hub` UI (pair cards first; devices if IPC ready).
4. Docs + mark kubectl helper as emergency.
