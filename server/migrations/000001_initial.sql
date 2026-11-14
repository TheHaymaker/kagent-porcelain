-- +goose Up

-- People Workbench has seen, keyed by kagent principal ID. Filled on sign-in.
CREATE TABLE person (
    id            TEXT PRIMARY KEY,
    email         TEXT NOT NULL DEFAULT '',
    display_name  TEXT NOT NULL DEFAULT '',
    groups        TEXT[] NOT NULL DEFAULT '{}',
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX person_email_idx ON person (lower(email));

-- A second identity (a Microsoft account used in Teams, say) that resolves to
-- a company principal. One external identity links to exactly one person.
CREATE TABLE identity_link (
    issuer       TEXT NOT NULL,
    subject      TEXT NOT NULL,
    person_id    TEXT NOT NULL REFERENCES person (id) ON DELETE CASCADE,
    display_hint TEXT NOT NULL DEFAULT '',
    linked_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (issuer, subject)
);
CREATE INDEX identity_link_person_idx ON identity_link (person_id);

-- Which agent a kagent session belongs to, and who started it. Recorded the
-- first time Workbench observes the session so it can address the A2A task.
CREATE TABLE watched_session (
    session_id   TEXT PRIMARY KEY,
    agent        TEXT NOT NULL,
    requester_id TEXT NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE app (
    id             TEXT PRIMARY KEY,
    name           TEXT NOT NULL,
    description    TEXT NOT NULL DEFAULT '',
    owner_id       TEXT NOT NULL,
    status         TEXT NOT NULL CHECK (status IN ('building', 'preview', 'live', 'failed', 'archived')),
    scaffold       TEXT NOT NULL DEFAULT '',
    repository_url TEXT NOT NULL DEFAULT '',
    session_id     TEXT NOT NULL DEFAULT '',
    agent          TEXT NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX app_name_live_idx ON app (lower(name)) WHERE status <> 'archived';
CREATE INDEX app_owner_idx ON app (owner_id);

CREATE TABLE app_environment (
    app_id     TEXT NOT NULL REFERENCES app (id) ON DELETE CASCADE,
    name       TEXT NOT NULL CHECK (name IN ('preview', 'production')),
    url        TEXT NOT NULL DEFAULT '',
    revision   TEXT NOT NULL DEFAULT '',
    image      TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (app_id, name)
);

CREATE TABLE deployment (
    id             TEXT PRIMARY KEY,
    app_id         TEXT NOT NULL REFERENCES app (id) ON DELETE CASCADE,
    environment    TEXT NOT NULL CHECK (environment IN ('preview', 'production')),
    revision       TEXT NOT NULL,
    image          TEXT NOT NULL DEFAULT '',
    url            TEXT NOT NULL DEFAULT '',
    deployed_by_id TEXT NOT NULL,
    approval_id    TEXT NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Production is only ever reached through an approval.
    CHECK (environment <> 'production' OR approval_id <> '')
);
CREATE INDEX deployment_app_idx ON deployment (app_id, created_at DESC);
-- An approval allows exactly one production deployment.
CREATE UNIQUE INDEX deployment_approval_idx ON deployment (approval_id) WHERE approval_id <> '';

CREATE TABLE approval (
    id                   TEXT PRIMARY KEY,
    -- Identifies the exact pause: one approval per pending HITL request.
    fingerprint          TEXT NOT NULL UNIQUE,
    state                TEXT NOT NULL CHECK (state IN ('pending', 'approved', 'rejected', 'superseded')),
    -- Empty for approvals that wrap no kagent pause (a promotion requested
    -- from the app catalogue rather than by an agent).
    session_id           TEXT NOT NULL DEFAULT '',
    task_id              TEXT NOT NULL DEFAULT '',
    agent                TEXT NOT NULL,
    requester_id         TEXT NOT NULL,
    kind                 TEXT NOT NULL,
    hint                 TEXT NOT NULL DEFAULT '',
    calls                JSONB NOT NULL,
    approver_groups      TEXT[] NOT NULL DEFAULT '{}',
    approver_ids         TEXT[] NOT NULL DEFAULT '{}',
    requester_may_decide BOOLEAN NOT NULL,
    app_id               TEXT NOT NULL DEFAULT '',
    -- Who decided, and for promotions the revision that was allowed.
    decided_by_id        TEXT NOT NULL DEFAULT '',
    decisions            JSONB NOT NULL DEFAULT '[]',
    reason               TEXT NOT NULL DEFAULT '',
    -- Whether the decision reached the paused task.
    delivered            BOOLEAN NOT NULL DEFAULT false,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    decided_at           TIMESTAMPTZ,
    CHECK (state = 'pending' OR state = 'superseded' OR decided_by_id <> ''),
    -- The rule that matters most is also a database constraint.
    CHECK (requester_may_decide OR decided_by_id = '' OR decided_by_id <> requester_id)
);
CREATE INDEX approval_session_idx ON approval (session_id, created_at);
CREATE INDEX approval_pending_idx ON approval (state, created_at) WHERE state = 'pending';
CREATE INDEX approval_requester_idx ON approval (requester_id, created_at DESC);
CREATE INDEX approval_app_idx ON approval (app_id) WHERE app_id <> '';

CREATE TABLE remembered_answer (
    id         TEXT PRIMARY KEY,
    person_id  TEXT NOT NULL,
    scope      TEXT NOT NULL CHECK (scope IN ('conversation', 'always')),
    tool_name  TEXT NOT NULL,
    agent      TEXT NOT NULL,
    session_id TEXT NOT NULL DEFAULT '',
    approved   BOOLEAN NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (scope = 'always' OR session_id <> '')
);
CREATE UNIQUE INDEX remembered_answer_key_idx ON remembered_answer (person_id, scope, tool_name, agent, session_id);

-- Append-only. The exporter ships rows to BigQuery and records how far it got.
CREATE TABLE audit_event (
    seq             BIGSERIAL PRIMARY KEY,
    id              TEXT NOT NULL UNIQUE,
    time            TIMESTAMPTZ NOT NULL DEFAULT now(),
    type            TEXT NOT NULL,
    actor_id        TEXT NOT NULL DEFAULT '',
    on_behalf_of_id TEXT NOT NULL DEFAULT '',
    session_id      TEXT NOT NULL DEFAULT '',
    task_id         TEXT NOT NULL DEFAULT '',
    app_id          TEXT NOT NULL DEFAULT '',
    approval_id     TEXT NOT NULL DEFAULT '',
    summary         TEXT NOT NULL,
    detail          JSONB NOT NULL DEFAULT '{}',
    surface         TEXT NOT NULL DEFAULT ''
);
CREATE INDEX audit_event_time_idx ON audit_event (time DESC);
CREATE INDEX audit_event_session_idx ON audit_event (session_id, seq) WHERE session_id <> '';
CREATE INDEX audit_event_app_idx ON audit_event (app_id, seq) WHERE app_id <> '';
CREATE INDEX audit_event_actor_idx ON audit_event (actor_id, seq);

-- Audit rows are never changed or removed by the application role.
-- +goose StatementBegin
CREATE FUNCTION audit_event_immutable() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'audit_event is append-only';
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd
CREATE TRIGGER audit_event_no_update BEFORE UPDATE OR DELETE ON audit_event
    FOR EACH ROW EXECUTE FUNCTION audit_event_immutable();

CREATE TABLE export_cursor (
    name     TEXT PRIMARY KEY,
    last_seq BIGINT NOT NULL DEFAULT 0
);

CREATE TABLE surface (
    surface_id TEXT NOT NULL,
    session_id TEXT NOT NULL,
    task_id    TEXT NOT NULL DEFAULT '',
    catalog_id TEXT NOT NULL,
    messages   JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (session_id, surface_id)
);

CREATE TABLE notification (
    id          TEXT PRIMARY KEY,
    person_id   TEXT NOT NULL,
    kind        TEXT NOT NULL,
    title       TEXT NOT NULL,
    body        TEXT NOT NULL DEFAULT '',
    link        TEXT NOT NULL DEFAULT '',
    session_id  TEXT NOT NULL DEFAULT '',
    approval_id TEXT NOT NULL DEFAULT '',
    app_id      TEXT NOT NULL DEFAULT '',
    dedupe_key  TEXT NOT NULL DEFAULT '',
    read        BOOLEAN NOT NULL DEFAULT false,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX notification_person_idx ON notification (person_id, created_at DESC);
CREATE UNIQUE INDEX notification_dedupe_idx ON notification (person_id, dedupe_key) WHERE dedupe_key <> '';

CREATE TABLE notification_preference (
    person_id TEXT NOT NULL,
    kind      TEXT NOT NULL,
    channels  TEXT[] NOT NULL,
    PRIMARY KEY (person_id, kind)
);

CREATE TABLE device (
    push_token  TEXT PRIMARY KEY,
    person_id   TEXT NOT NULL,
    platform    TEXT NOT NULL CHECK (platform IN ('ios', 'android')),
    device_name TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX device_person_idx ON device (person_id);

-- Usage as the model gateway reports it, one row per call.
CREATE TABLE usage_record (
    id            BIGSERIAL PRIMARY KEY,
    time          TIMESTAMPTZ NOT NULL DEFAULT now(),
    person_id     TEXT NOT NULL,
    session_id    TEXT NOT NULL DEFAULT '',
    app_id        TEXT NOT NULL DEFAULT '',
    model         TEXT NOT NULL,
    input_tokens  BIGINT NOT NULL DEFAULT 0,
    output_tokens BIGINT NOT NULL DEFAULT 0,
    cost_micros   BIGINT NOT NULL DEFAULT 0,
    request_id    TEXT NOT NULL DEFAULT ''
);
CREATE INDEX usage_record_person_time_idx ON usage_record (person_id, time);
CREATE INDEX usage_record_app_idx ON usage_record (app_id, time) WHERE app_id <> '';
CREATE UNIQUE INDEX usage_record_request_idx ON usage_record (request_id) WHERE request_id <> '';

CREATE TABLE spend_limit (
    subject       TEXT NOT NULL CHECK (subject IN ('person', 'team')),
    subject_id    TEXT NOT NULL,
    monthly_cents BIGINT NOT NULL CHECK (monthly_cents >= 0),
    PRIMARY KEY (subject, subject_id)
);

-- +goose Down
DROP TABLE spend_limit;
DROP TABLE usage_record;
DROP TABLE device;
DROP TABLE notification_preference;
DROP TABLE notification;
DROP TABLE surface;
DROP TABLE export_cursor;
DROP TRIGGER audit_event_no_update ON audit_event;
DROP FUNCTION audit_event_immutable();
DROP TABLE audit_event;
DROP TABLE remembered_answer;
DROP TABLE approval;
DROP TABLE deployment;
DROP TABLE app_environment;
DROP TABLE app;
DROP TABLE watched_session;
DROP TABLE identity_link;
DROP TABLE person;
