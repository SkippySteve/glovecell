# Pending-request notifications

When a request starts waiting for the operator's approval, the controller can
send one short push notification to an [ntfy](https://ntfy.sh) topic (ntfy is
a self-hostable push-notification server with Android and iOS apps). The
operator taps it, signs in to the console and approves or denies there. The
notification itself approves nothing and has no action buttons.

It is off by default.

## What a notification says

The controller writes every word itself, from fields it observed. An agent
cannot put text into it: a request's reason, a title or any other text the
agent wrote is never sent, and observed values an agent can influence (a
package name, a host) are cut to plain name characters (no spaces, line
breaks, colons or markup) and a fixed length.

| Field | Example |
|---|---|
| Title | `Glovecell: cell platform asks for a package` |
| Kind | web access, a package, LAN access, a new repository, a port forward, a preview |
| Cell | `Cell: platform` |
| Requested access | `Asks for: npm package left-pad 1.3.0 from registry.npmjs.org` |
| Duration, when the access would expire | `For: 1 h 30 min` |
| Link | the console page of the request, such as `https://projects.example.com/#/sandboxes/platform/internet` |

A web request shows its method, scheme, host and port, not its path or query,
which can carry data. Tokens and approval codes are never sent.

Today the controller records two kinds as pending requests: web requests and
package downloads. The other kinds' text is defined and tested so that each
request kind, when it is added, uses the same notifier.

## Limits

- One notification per new pending request. A repeat of the same pending
  request (the proxy seeing it again) is not sent again.
- At most `notifications.maxPerHour` (default 20) per hour per installation.
  Past that, nothing more is sent that hour; when the hour ends, one summary
  says how many more requests waited.
- Sending never blocks or fails the request. A notification that cannot be
  sent (ntfy unreachable, token refused, token file unreadable) is logged
  without the token and counted; the request is still in the console.

## Metrics

Exported with the controller's other OpenTelemetry metrics (in Prometheus
form with a `_total` suffix):

| Metric | Labels | Meaning |
|---|---|---|
| `glovecell.notifications.sent` | `kind` | accepted by ntfy |
| `glovecell.notifications.failed` | `kind` | not delivered; the request is still in the console |
| `glovecell.notifications.skipped` | `kind`, `reason` (`duplicate`, `rate_limited`) | not sent on purpose |

An alert on `failed` increasing tells you notifications have stopped.

## Configuration

```yaml
notifications:
  enabled: true
  ntfy:
    url: https://ntfy.example.com     # https; http only for an in-cluster Service (*.svc)
    topic: glovecell-approvals
    existingSecret: glovecell-ntfy    # created by you, in the release namespace
    tokenKey: token
  consoleURL: ''                      # defaults to config.PUBLIC_ORIGIN
  maxPerHour: 20
```

1. On the ntfy server, create a user for Glovecell with write-only access to
   the topic, and an access token for it
   (`ntfy user add glovecell`, `ntfy access glovecell glovecell-approvals write-only`,
   `ntfy token add glovecell`). Give your own phone's user read access to the
   topic. Do not use a public topic without access control: anyone who
   guesses its name could read or post to it.
2. Create the Secret in the controller's namespace, with the token under
   `tokenKey`. The controller reads the token file for every send, so a
   rotated Secret is picked up without a restart.
3. Allow the controller's egress to the ntfy server if your network policy
   restricts it (TCP 443, or the Service port for an in-cluster ntfy).
4. Set the values above and upgrade the release. A partial setting (a topic
   without a URL, a missing Secret) fails the render or the controller's
   start rather than silently sending nothing.

The chart passes the settings to the controller as `NOTIFY_NTFY_URL`,
`NOTIFY_NTFY_TOPIC`, `NOTIFY_NTFY_TOKEN_FILE`, `NOTIFY_CONSOLE_URL` and
`NOTIFY_MAX_PER_HOUR`, and mounts only the token key of the Secret.

To turn it off, set `notifications.enabled: false`; nothing else changes.

## Design

The notifier is part of the design for agent-requested repositories
([repository-requests.md](design/repository-requests.md), section 6, and
decision D17 in [decisions.md](design/decisions.md)). It runs inside the
controller, so nothing is installed on an operator's machine.
