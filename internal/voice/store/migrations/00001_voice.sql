-- glovecell-voice: threads, tasks, approval cards, the gate switch and the
-- approval audit (docs/voice.md, docs/approval-gate.md).

-- +goose Up
CREATE TABLE voice_events (
    event_id   text PRIMARY KEY,
    claimed_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE voice_threads (
    room_id    text NOT NULL,
    root_event text NOT NULL,
    target     text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (room_id, root_event)
);

CREATE TABLE voice_tasks (
    id           bigserial PRIMARY KEY,
    room_id      text NOT NULL,
    thread_root  text NOT NULL DEFAULT '',
    source_event text NOT NULL DEFAULT '',
    origin       text NOT NULL CHECK (origin IN ('voice', 'text', 'gate')),
    target       text NOT NULL DEFAULT '',
    repo         text NOT NULL DEFAULT '',
    external_id  text NOT NULL DEFAULT '',
    state        text NOT NULL CHECK (state IN ('received', 'running', 'succeeded', 'failed')),
    transcript   text NOT NULL DEFAULT '',
    audio_sha256 text NOT NULL DEFAULT '',
    result       text NOT NULL DEFAULT '',
    error        text NOT NULL DEFAULT '',
    delivered    boolean NOT NULL DEFAULT false,
    created_at   timestamptz NOT NULL,
    updated_at   timestamptz NOT NULL
);
CREATE INDEX voice_tasks_pending ON voice_tasks (id) WHERE NOT delivered;
CREATE INDEX voice_tasks_thread ON voice_tasks (room_id, thread_root);

CREATE TABLE voice_approval_cards (
    id           bigserial PRIMARY KEY,
    kind         text NOT NULL CHECK (kind IN ('pr', 'list')),
    code         text NOT NULL CHECK (code ~ '^[0-9]{4}$'),
    repo         text NOT NULL DEFAULT '',
    pr           integer NOT NULL DEFAULT 0,
    head_sha     text NOT NULL DEFAULT '',
    title        text NOT NULL DEFAULT '',
    items        jsonb NOT NULL DEFAULT '[]',
    room_id      text NOT NULL,
    event_id     text NOT NULL DEFAULT '',
    requested_by text NOT NULL DEFAULT '',
    state        text NOT NULL CHECK (state IN ('open', 'approved', 'merged', 'failed', 'rejected', 'superseded', 'interrupted')),
    outcome      text NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL,
    expires_at   timestamptz NOT NULL,
    used_at      timestamptz
);
-- A code names at most one open card.
CREATE UNIQUE INDEX voice_cards_open_code ON voice_approval_cards (code) WHERE state = 'open';
CREATE INDEX voice_cards_code ON voice_approval_cards (code, created_at DESC);
CREATE INDEX voice_cards_pr ON voice_approval_cards (repo, pr) WHERE state = 'open';

CREATE TABLE voice_gate_state (
    id         integer PRIMARY KEY CHECK (id = 1),
    paused     boolean NOT NULL,
    changed_by text NOT NULL DEFAULT '',
    changed_at timestamptz NOT NULL
);

-- Append-only: the service never updates or deletes rows.
CREATE TABLE voice_approval_audit (
    id           bigserial PRIMARY KEY,
    occurred_at  timestamptz NOT NULL,
    sender       text NOT NULL DEFAULT '',
    device_id    text NOT NULL DEFAULT '',
    verified     boolean NOT NULL DEFAULT false,
    room_id      text NOT NULL DEFAULT '',
    event_id     text NOT NULL DEFAULT '',
    transcript   text NOT NULL DEFAULT '',
    audio_sha256 text NOT NULL DEFAULT '',
    repo         text NOT NULL DEFAULT '',
    pr           integer NOT NULL DEFAULT 0,
    head_sha     text NOT NULL DEFAULT '',
    code         text NOT NULL DEFAULT '',
    card_id      bigint NOT NULL DEFAULT 0,
    decision     text NOT NULL,
    reason       text NOT NULL DEFAULT ''
);
CREATE INDEX voice_audit_decision ON voice_approval_audit (decision, occurred_at);
CREATE INDEX voice_audit_audio ON voice_approval_audit (audio_sha256) WHERE audio_sha256 <> '';

-- +goose Down
DROP TABLE voice_approval_audit;
DROP TABLE voice_gate_state;
DROP TABLE voice_approval_cards;
DROP TABLE voice_tasks;
DROP TABLE voice_threads;
DROP TABLE voice_events;
