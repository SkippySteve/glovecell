# Voice approval gate

The approval gate (`internal/approvalgate`, inside glovecell-voice) lets
Steve approve pull requests by voice instead of clicking Merge in Forgejo
(decision D15). It covers every PR, including one-way doors. Everything
still goes through PRs. The gate is the only automated merger, and it acts
only on Steve's verified approval. Agents never merge.

## Cards

A card is posted to the Approvals room when either of these asks for one:

- an agent, through `POST /v1/notify` with `kind=approval`, `repo` and `pr`;
- the gate itself, when a PR moved under an open card or a merge was
  interrupted.

The gate reads the PR from Forgejo as the `approval-gate` user. It never
takes the agent's word for what the PR contains. The card shows:

- **Facts from Forgejo:** the repository, PR number, the real title, the
  link, the author, the branches and the head SHA, the changed files with
  their additions and deletions, the CI state per check, and whether the PR
  is mergeable.
- **Gate notes** from the changed paths, for example "changes CI
  workflows", "changes the database schema", "changes access control" or
  "changes dependencies".
- **The PR description's claims** (risk, one-way or two-way door, rollback,
  hand steps), labelled "author's words, not verified". The requesting
  agent's note, if any, is labelled the same way.
- **A 4-digit code** (1000–9999, random, not reused within a week) bound to
  (repository, PR, head SHA). It expires after 24 hours (`gate.codeTTL`)
  and works once.

A spoken summary is posted with the text card. The gate also sets the
`voice-approval` commit status to `pending` on the head SHA.

If a card is requested again for the same head, the open card is reused. A
request for a new head retires the old card and issues a new one.

## What Steve can say

| Say | Effect |
|---|---|
| "approve 4721", "approve PR 459 code 4721", "approve four seven two one", "forty-seven twenty-one" | Approve: all the checks below, then merge. |
| "approve 4721 despite failing checks" | The only way past red, pending or missing CI. "anyway" is not enough. |
| "reject 4721 because …" | Closes the card and sets `voice-approval` to failure. Comments with the reason and hands it back to an agent as a task. |
| "hold" (in the card's thread) or "hold 4721" | Recorded, commented, and passed to an agent. The card stays open. "don't approve 4721" is a hold. |
| "tell me more about 4721" | All files, every check and the description, as text and voice. |
| "what's pending?" or "list" | Posts a list card with its own code, bound to every open card at its current head. |
| "approve all on today's list code 8830" | Merges every PR on the list. Every item must pass every check first, or nothing merges. |
| "revert PR 459" | Asks an agent to open a revert PR, which needs its own card. The gate never reverts. |
| "pause all merges", "emergency stop" | Pauses the gate. It works from any of Steve's devices in the encrypted room, because it only stops things. |
| "resume merges" | Unpauses. Needs the verified device. |

Ambiguous replies are refused rather than guessed. These include two
decisions ("approve 4721, no wait, reject"), two codes, "approve" together
with "hold", and an approval without a code.

## Checks

An approval merges only when every check passes, in this order:

1. **Identity.**
   - The sender is the configured Steve MXID. Other senders are refused
     silently.
   - The message is in the Approvals room and was end-to-end encrypted.
   - It is not an edit and not a forward.
   - The sending device is cross-signed and verified (see
     [voice.md](voice.md#device-trust)).
   - The recording's SHA-256 was never used before. This catches a replayed
     or forwarded voice note.
2. **Intent.** The reply contains an approve intent and a code that matches
   a card. A spoken PR number must match the card.
3. **The code.** It has not expired and has not been used. A superseded or
   interrupted card says to use the newer card.
4. **Pause and rate.** Merges are not paused, and fewer than
   `gate.maxMergesPerHour` (default 10) merged in the last hour. A list
   counts all its PRs.
5. **SHA.** The PR head is still the card's SHA. If it moved, the card is
   retired and a new card goes out.
6. **CI.** Every check except `voice-approval` itself is `success`, and at
   least one check ran. The only exception is "despite failing checks".
7. **Mergeable.** Forgejo says the PR is open and mergeable.

Then the gate:

1. consumes the code, atomically (`UPDATE … WHERE state = 'open'`), so
   exactly one approval wins;
2. sets `voice-approval` = success on the head SHA;
3. merges with a merge commit, pinned to the SHA (`head_commit_id`, so a
   push in between makes Forgejo refuse);
4. comments on the PR: "Approved by Steve by voice at <time>, code, head",
   the transcript, the audio SHA-256 and the device ID;
5. confirms by voice in the thread;
6. writes an audit row.

If the merge fails, `voice-approval` is set to failure, the card is marked
failed, and Steve hears why. A new card is needed.

The audit table `voice_approval_audit` records every card, approval,
refusal, rejection, hold, pause and revert request: sender, device,
verified flag, room, event, transcript, audio SHA-256, repository, PR,
SHA, code, decision and reason. Rows are never updated or deleted. The
audio itself is not stored. Its hash in the PR comment and the audit row
ties the decision to the recording in the encrypted room.

## Restarts

If the service stops after consuming a code but before finishing the merge,
the card stays in `approved`. On the next start, `Recover`:

- records a PR that did merge as merged;
- marks anything else `interrupted`, sets `voice-approval` back to pending
  and posts a new card.

Recovery never merges.

When the database is unreachable, every approval is refused: codes cannot
be checked or consumed.

## Configuration

```json
"gate": {
  "enabled": true,
  "forgejoOrigin": "https://git.example.com",
  "repos": ["steve/*"],
  "maxMergesPerHour": 10,
  "codeTTL": "24h",
  "statusContext": "voice-approval"
}
```

`repos` lists the `OWNER/NAME` repositories or `OWNER/*` owners the gate may
act on. A bare `*` is refused. The Approvals room is the configured room
with `approvals: true`. The `forgejo-token` Secret key holds the
`approval-gate` user's token.

## Forgejo setup (Steve's hand steps)

1. Create the `approval-gate` user. Give it write access to the covered
   repositories, and create a token with `write:repository` and
   `write:issue`. Store the token in OpenBao or in the `glovecell-voice`
   Secret in the control namespace (key `forgejo-token`), never in a sandbox.
2. In each repository's branch protection for `main`:
   - add `approval-gate` to the merge whitelist;
   - add `voice-approval` to the required status checks;
   - keep the existing required checks.
3. Keep agents (`orca-agent`) unable to merge: they stay off the merge
   whitelist and keep only their PR permissions.
4. Point agents that ask for cards at the notify API with an `approval`
   kind token.

## Why agents still cannot approve

- Notify has no approve operation. `kind=approval` only asks for a card, and
  the reply never contains the code.
- Notify cannot post into the Approvals room.
- Agents and bots are other Matrix users. The bot ignores them, and the
  gate refuses them as a second check.
- Codes are only shown in the Approvals room, which agents cannot read.
- Even with a code, an approval needs Steve's account on his verified
  device.
- The gate's Forgejo token never leaves the control namespace. Agents keep
  their own token, which cannot merge.
