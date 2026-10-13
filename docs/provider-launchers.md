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
not qualify providers. Automated backend renewal and four-provider live
qualification remain separate acceptance requirements.
