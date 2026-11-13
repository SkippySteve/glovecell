-- Operational records (D13 item 2). Applied by the controller at start under
-- a PostgreSQL advisory lock (store.Migrate); never edit an applied file, add
-- the next version instead.

-- +goose Up
CREATE TABLE audit_events (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    record_key    text        NOT NULL UNIQUE,
    workspace_uid text        NOT NULL,
    workspace     text        NOT NULL DEFAULT '',
    generation    bigint      NOT NULL DEFAULT 0,
    action        text        NOT NULL,
    actor         text        NOT NULL DEFAULT '',
    message       text        NOT NULL DEFAULT '',
    details       jsonb       NOT NULL DEFAULT '{}'::jsonb,
    source        text        NOT NULL DEFAULT 'controller',
    occurred_at   timestamptz NOT NULL,
    recorded_at   timestamptz NOT NULL DEFAULT now()
);
-- History of one cell, newest first, and the console's filters.
CREATE INDEX audit_events_workspace_time ON audit_events (workspace_uid, occurred_at DESC, id DESC);
CREATE INDEX audit_events_workspace_action ON audit_events (workspace_uid, action text_pattern_ops);
CREATE INDEX audit_events_workspace_actor ON audit_events (workspace_uid, actor, occurred_at DESC);
-- Retention.
CREATE INDEX audit_events_occurred ON audit_events (occurred_at);

CREATE TABLE access_requests (
    id                bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    workspace_uid     text        NOT NULL,
    workspace         text        NOT NULL DEFAULT '',
    scheme            text        NOT NULL CHECK (scheme IN ('http', 'https')),
    host              text        NOT NULL,
    port              integer     NOT NULL CHECK (port BETWEEN 1 AND 65535),
    method            text        NOT NULL,
    path              text        NOT NULL,
    fingerprint       text        NOT NULL DEFAULT '',
    package_ecosystem text,
    package_name      text,
    package_version   text,
    reason            text        NOT NULL DEFAULT '',
    requested_by      text        NOT NULL,
    state             text        NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'approved', 'denied')),
    observations      bigint      NOT NULL DEFAULT 1,
    created_at        timestamptz NOT NULL DEFAULT now(),
    last_seen_at      timestamptz NOT NULL DEFAULT now(),
    decided_by        text,
    decided_at        timestamptz,
    decision_reason   text        NOT NULL DEFAULT '',
    approval_id       bigint,
    CHECK ((state = 'pending') = (decided_at IS NULL))
);
-- One pending request per exact target: repeats count observations.
CREATE UNIQUE INDEX access_requests_pending_target ON access_requests (workspace_uid, scheme, host, port, method, path, fingerprint) WHERE state = 'pending';
CREATE INDEX access_requests_workspace_state ON access_requests (workspace_uid, state, id DESC);
CREATE INDEX access_requests_workspace_host ON access_requests (workspace_uid, host, id DESC);
CREATE INDEX access_requests_decided ON access_requests (decided_at) WHERE state <> 'pending';

CREATE TABLE approvals (
    id                bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    workspace_uid     text        NOT NULL,
    workspace         text        NOT NULL DEFAULT '',
    scheme            text        NOT NULL CHECK (scheme IN ('http', 'https')),
    host              text        NOT NULL,
    port              integer     NOT NULL CHECK (port BETWEEN 1 AND 65535),
    method            text        NOT NULL,
    path              text        NOT NULL,
    fingerprint       text        NOT NULL DEFAULT '',
    package_ecosystem text,
    package_name      text,
    package_version   text,
    source            text        NOT NULL CHECK (source IN ('approved', 'learned')),
    request_id        bigint REFERENCES access_requests (id),
    created_by        text        NOT NULL,
    created_at        timestamptz NOT NULL DEFAULT now(),
    revoked_by        text,
    revoked_at        timestamptz,
    revoke_reason     text        NOT NULL DEFAULT ''
);
-- At most one active approval per exact target.
CREATE UNIQUE INDEX approvals_active_target ON approvals (workspace_uid, scheme, host, port, method, path, fingerprint) WHERE revoked_at IS NULL;
-- The proxy snapshot and the console list.
CREATE INDEX approvals_workspace_active ON approvals (workspace_uid, id DESC) WHERE revoked_at IS NULL;
CREATE INDEX approvals_workspace_host ON approvals (workspace_uid, host, id DESC);
ALTER TABLE access_requests ADD CONSTRAINT access_requests_approval_fk FOREIGN KEY (approval_id) REFERENCES approvals (id);

CREATE TABLE proxy_requests (
    id                 bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    workspace_uid      text        NOT NULL,
    request_id         text        NOT NULL,
    occurred_at        timestamptz NOT NULL,
    scheme             text        NOT NULL,
    host               text        NOT NULL,
    port               integer     NOT NULL,
    method             text        NOT NULL,
    path               text        NOT NULL,
    fingerprint        text        NOT NULL DEFAULT '',
    decision           text        NOT NULL CHECK (decision IN ('allow', 'deny')),
    matched            text        NOT NULL DEFAULT '',
    egress_window      text        NOT NULL DEFAULT '',
    status             integer     NOT NULL DEFAULT 0,
    bytes_sent         bigint      NOT NULL DEFAULT 0,
    bytes_received     bigint      NOT NULL DEFAULT 0,
    duration_ms        bigint      NOT NULL DEFAULT 0,
    credential_profile text        NOT NULL DEFAULT '',
    UNIQUE (workspace_uid, request_id)
);
CREATE INDEX proxy_requests_workspace_time ON proxy_requests (workspace_uid, occurred_at DESC, id DESC);
CREATE INDEX proxy_requests_workspace_decision ON proxy_requests (workspace_uid, decision, occurred_at DESC);
CREATE INDEX proxy_requests_workspace_host ON proxy_requests (workspace_uid, host, occurred_at DESC);
CREATE INDEX proxy_requests_occurred ON proxy_requests (occurred_at);

-- One-time imports, such as the legacy audit ConfigMaps.
CREATE TABLE imports (
    name         text PRIMARY KEY,
    completed_at timestamptz NOT NULL DEFAULT now(),
    records      bigint      NOT NULL DEFAULT 0
);

-- +goose Down
DROP TABLE imports;
DROP TABLE proxy_requests;
ALTER TABLE access_requests DROP CONSTRAINT access_requests_approval_fk;
DROP TABLE approvals;
DROP TABLE access_requests;
DROP TABLE audit_events;
