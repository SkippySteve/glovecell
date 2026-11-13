# Cell DNS and direct-IP qualification

`scripts/qualify-cell-network.py` supplies repeatable IPv4 probes for APC #107.
It does not create pods, grant access, change policy or assert that the whole
cluster is isolated. Run it only against disposable environments and endpoints
approved for testing. Do not use production subscription sessions or credentials.

## Prepare positive controls

Prepare two disposable pods with Python 3 and trusted CA certificates:

- A cell worker under the actual Cilium policy and runtime being qualified.
- A control pod in a separate namespace with direct access to the test endpoints.
  The script does not create or relax its policy. Use an existing approved fixture.

The worker must have its normal `HTTPS_PROXY` and `SSL_CERT_FILE` configured.
The script requires a proxy for HTTPS worker probes and removes `NO_PROXY`
inside that short-lived process. It never disables TLS verification. No changes
are made to the container's persistent environment.

Choose a real, resolvable name absent from the cell allowlist, and a controlled
public IPv4 endpoint listening on 443. Supply a public DNS resolver that actually
answers UDP/TCP 53 and accepts TCP 853 from the control pod. DoT checks TCP
reachability, not a TLS handshake. Every negative probe first runs against the
control pod. An unreachable control is **inconclusive**, never a pass.

For DoH supply two HTTPS GET URLs containing valid `dns=` wire queries, one on
an allowed host/path and one on an unlisted host. Both must return HTTP 200,
`application/dns-message`, and a successful DNS response with answers from the
control pod. Do not put credentials in these URLs. The worker's allowed DoH
probe must also pass, proving the proxy and CA work. An unrelated HTTP failure
or invalid response is an execution error, not a policy denial.

```sh
python3 scripts/qualify-cell-network.py \
  --namespace "$CELL_NAMESPACE" --pod "$CELL_POD" --container workspace \
  --control-namespace "$CONTROL_NAMESPACE" --control-pod "$CONTROL_POD" \
  --control-container probe --acknowledge-disposable-targets \
  --unlisted-name "$UNLISTED_NAME" --public-ip "$PUBLIC_HTTPS_IP" \
  --resolver-ip "$RESOLVER_IP" --allowed-https "$ALLOWED_HTTPS_URL" \
  --allowed-doh "$ALLOWED_DOH_URL" --unlisted-doh "$UNLISTED_DOH_URL" \
  > network-probes.json
```

Exit 0 means all eight probes matched expectations. Exit 1 includes a bypass,
failed positive control or inconclusive baseline. Exit 2 means execution failed.
Kubernetes exec failures, missing Python and malformed output cannot pass as
denials. Probe output contains only check names and results, never response
bodies, environment values, pairing offers or exception text.

## Correlate denials and bound DNS leases

Probe results alone are insufficient for acceptance. A timeout or refusal can
also come from routing or a remote firewall. Capture a bounded Hubble observation
for the cell IP while running the probes. Record timestamps and correlate each
negative probe with a policy drop or DNS-proxy refusal. Retain only sanitized
flow metadata. An unlisted DNS lookup must fail even when its public name is
resolvable from the control. DNS to an alternate resolver, direct public TCP,
DoT and unlisted DoH must be denied; allowed proxy HTTPS and DoH must work.

Read the installed Cilium version, effective agent configuration and
`cilium-config`. Record `tofqdns-min-ttl`, `tofqdns-idle-connection-grace-period`
and `tofqdns-max-deferred-connection-deletes`. An absent ConfigMap key is **not**
proof of a zero value: read the effective agent setting or version-matched
default. A read-only infrastructure snapshot on 2026-11-09 found an idle grace
of `0s` and deferred-delete cap of `10000`; `tofqdns-min-ttl` was absent. These
values are observations, not product defaults or proof of lease expiry.

In the disposable fixture, use an operator-controlled DNS record with a known
TTL and an approved endpoint. Observe the Cilium FQDN cache and identity before
resolution, after resolution, and after the effective TTL. Verify new connections
to an expired address lose authorization. Separately record what happens to an
already established connection; conntrack can preserve it beyond DNS expiry.
Do not claim "allowed once never means allowed forever" from TTL alone.

Retain the effective policy, runtime/Cilium versions, sanitized probe output,
correlated denial evidence and lease-expiry observations in the qualification
record. Cover IPv6, bootstrap races, additive cluster policies and node-local
DNS separately before a full bypass-prevention sign-off. Do not mark APC #107
complete until the live denial and lease evidence exists.
