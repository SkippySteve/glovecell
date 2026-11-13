# Repositories in one sandbox

T3's Add project and Orca's `repo add` only register a directory with the
runtime. They do not enroll a remote repository or authorize Git. Enroll
through the controller first.

In the controller, open the project sandbox → **Credentials**. While the
sandbox is **Ready**, under **Repositories in this sandbox** enter a directory
name and an `owner/repository`, then **Add repository**. Pause is not required.
The controller grants access and refreshes the worker briefly so bootstrap
clones onto the existing volume; agents reconnect. (Enrollment while settled
Paused still works; Resume then clones.) The original `/workspace/repo` remains
in place. Additional checkouts land at `/workspace/projects/<name>`. Hubspoke
then runs `orca repo add` for each new path on the same spoke environment (the
primary `/workspace/repo` was registered at enroll). On a T3-linked sandbox the
same checkout is registered with stock T3. Select the new project in the
runtime and start a thread in its current checkout. Use Ctrl+J (T3) or the
spoke terminal to check `pwd`.

This enrollment path does not create another sandbox, volume, provider login or
runtime environment. **New sandbox** creates an isolated environment; use the
enrollment form inside the existing sandbox for additional repositories.

## Forgejo

Owned Forgejo repositories get independent non-administrator bot identities and
repository-scoped `write:repository` and `write:issue` tokens. The issues scope is
needed by stock `fj` for PR search and comments. Existing branch protection is
checked before granting access. The controller refuses grants when it cannot
verify the protected main branch and owner-only merges.

The workspace build includes pinned stock Forgejo CLI v0.6.0. Its `fj` launcher
selects the current checkout's grant, reads it again on each invocation and
supplies it through a temporary private config. Credentials are not passed in
arguments, exported in the child environment or stored in a persistent CLI home.
The CLI consumes grant replacement on its next invocation. Sign-in and host
overrides are controller-managed.

From an enrolled Forgejo checkout:

```sh
fj repo view
fj pr search
fj pr create --help
git fetch origin
```

Additional Forgejo routes accept Git clone URLs with or without `.git`.
Agents get no Forgejo administration token. Tokens and inspected routes grant
only enrolled repository Git and repository API access.

## GitHub

Public GitHub enrollment accepts any `owner/repository`. It permits anonymous
clone/fetch for that repository and denies the push endpoint.

Private GitHub clone/fetch uses a fine-grained repository token held by the
trusted controller and injected at the inspecting gateway. Workers receive
neither the token nor GitHub account credentials. Use selected repositories,
Contents read-only, Metadata read-only and a finite expiry. The gateway injects
only for an enrolled private repository's Git upload requests at `github.com`.
It denies push discovery and receive-pack, and never forwards the token to a
redirect destination or API/browser route. Missing, expired or revoked
authorization blocks access without anonymous or direct-authentication fallback.

Configure the chart's `additionalGitHub` with `enabled`, `owner`,
`existingSecret` and `tokenKey`. The Secret must already exist in the trusted
control namespace. Do not put its value in chart values or repository files.
For a non-Helm deployment use `ADDITIONAL_GITHUB_OWNER` and
`GITHUB_REPOSITORY_TOKEN_FILE`. Replace the trusted token and restart only the
controller to republish it. The gateway reads the projected token per request;
the workspace volume and T3 identity do not need replacing.

Private GitHub write access remains separate from this clone-only grant. The
existing GitHub App implementation for isolated environments verifies branch
protection and mints repository-specific short-lived tokens; it is not enabled
by this additional-clone setting.

## Recovery and removal

Enrollment records are durable and immutable. Grant intents are saved before
external requests. Failed clones retain an ownership receipt and are retried
through an owned staging directory. Existing unowned directories and symlink
escapes are refused. Interrupted enrollment leaves the workbench paused or
unready; it does not delete project data to make preparation succeed.

Individual project removal remains blocked until a verified process-stop,
external-revocation and owned-storage-cleanup operation is implemented. Whole
sandbox removal revokes all enrolled Forgejo identities and verifies sandbox
storage cleanup. It is a separate destructive operation. Do not use it to
remove one checkout.

The offline suite covers policy denial, interrupted clone/grant recovery,
credential replacement, original volume preservation and revocation failure.
The disposable Forgejo qualification can also verify stock `fj`:

```sh
FORGEJO_BINARY=/path/to/verified/forgejo \
APC_TEST_FJ_BINARY=/path/to/verified/fj python3 scripts/qualify-forgejo.py
```

These checks do not imply live deployment, private GitHub account verification
or qualification of all model providers.
