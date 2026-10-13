# Subscription provider launchers

The workspace image includes stock Grok Build 1.0.46, Cursor CLI
2026.10.01-e373342 and Muse Code 1.4.7-R6101.1. T3 uses the Grok and Cursor
launchers through its native integrations. Muse runs in the project terminal.
These are client installations, not evidence of usable provider accounts.

The trusted backend projects a `provider-access` JSON field in the existing
read-only `model-access` Secret. It contains entries named `grok`, `cursor` and
`muse`. An enabled entry has `enabled: true` and an `access` object containing
`accessToken` and the Unix `expiresAt`. Grok also requires
`issuer: https://auth.x.ai`. Revocation uses `enabled: false` without access
material. Unknown providers, refresh fields and nested credential containers
are rejected. The launchers deny account sign-in, client updates, credential
overrides, alternate endpoints and execution outside enrolled projects.

Grok consumes projected replacements through its official external-auth
contract. Cursor receives an access-only token through CURSOR_AUTH_TOKEN.
Rotation stops only its provider process; the app must resume the retained
session to use the replacement. The launcher never replays a prompt or completed
command. Session directories remain on the workspace volume. Grant expiry,
revocation or a missing projection stops the affected launcher with a status
message. No worker connection to OpenBao or credential owners is added.

The inspecting gateway also checks the current grant and bearer credential on
every provider request. Raw HTTPS cannot bypass expiry, revocation or credential
replacement. Projected replacements are consumed without restarting other
providers or the workspace. Provider connections remain bounded; the gateway
admits up to 32 simultaneous TLS tunnels for the CLI's concurrent tool messages.

Muse's accessToken must be the non-secret placeholder
`apc-muse-subscription-gateway`. Its subscription credential comes from official
device-code sign-in and remains outside worker storage. The trusted controller
reads MUSE_SUBSCRIPTION_KEY_FILE and projects it only to the inspecting proxy.
The proxy injects it only for reviewed api.meta.ai model and inference routes,
using verified upstream TLS. Missing or revoked gateway material fails closed.
This does not create or substitute a separately billed Meta API key.

The launchers require qualified HTTPS policy, trusted account authorization and
a backend that publishes fresh grants. Installation and offline tests alone do
not qualify providers. Four-provider live qualification remains a separate
acceptance requirement.

The optional Go `provider-broker` command runs on the trusted control side. It
publishes five-minute Grok and Cursor access leases every thirty seconds from
explicitly configured credential owners. It renews Grok through the verified
official OAuth endpoint. Cursor reads the retained owner's current access token;
automatic renewal of the owner's Cursor login remains unimplemented. An owner
outage leaves that provider's lease to expire and does not block the other owner.
Disabled grants stay disabled after broker restarts. Muse is not renewed by this
publisher.

Bind this broker to a specific ProjectWorkspace UID. Give its ServiceAccount
only reads of that workspace, get/patch of its model-access projection and
renewal journal, and exec on the explicitly selected owner pods. Give workers
none of these permissions. Keep the journal in the trusted control namespace.
Grok rotation records intent before requesting a new token and the response
before replacing the owner's credential file. Recovery completes a saved owner
write without replaying OAuth. An interrupted request with no recorded response
fails closed and requires owner recovery. Concurrent revocation or replacement
of the workspace or projection prevents publication.

The existing Codex lifecycle still requires a valid Codex grant for the whole
workspace. This publisher does not yet replace that lifecycle with independent
Codex renewal. Do not treat these launchers as complete workbench qualification.
