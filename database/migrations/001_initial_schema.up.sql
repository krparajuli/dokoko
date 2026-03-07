-- 001_initial_schema.up.sql
-- Initial schema for dokoko persistent store.
--
-- All six Docker resource types are persisted here so that store state
-- survives process restarts.  Timestamps are stored as ISO-8601 UTC strings
-- (Go's time.RFC3339Nano format).  JSON arrays/objects are stored as TEXT
-- and marshalled/unmarshalled at the application layer.
--
-- schema_migrations is managed by the migration runner, not here.

-- ── Containers ───────────────────────────────────────────────────────────────
-- Maps to containers/state.ContainerRecord.
-- Keyed by full Docker container ID.
-- status: present | removed | removed_out_of_band | errored
-- origin: in_band | out_of_band

CREATE TABLE containers (
    docker_id       TEXT    PRIMARY KEY,
    short_id        TEXT    NOT NULL,
    name            TEXT    NOT NULL DEFAULT '',
    image           TEXT    NOT NULL DEFAULT '',
    image_id        TEXT    NOT NULL DEFAULT '',
    network_mode    TEXT    NOT NULL DEFAULT '',
    runtime_state   TEXT    NOT NULL DEFAULT '',
    exit_code       INTEGER NOT NULL DEFAULT 0,
    created_at      TEXT    NOT NULL DEFAULT '',  -- RFC3339 UTC
    registered_at   TEXT    NOT NULL,             -- RFC3339 UTC
    updated_at      TEXT    NOT NULL,             -- RFC3339 UTC
    origin          TEXT    NOT NULL,             -- in_band | out_of_band
    status          TEXT    NOT NULL,             -- present | removed | removed_out_of_band | errored
    err_msg         TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX idx_containers_status ON containers (status);
CREATE INDEX idx_containers_origin ON containers (origin);

-- ── Images ───────────────────────────────────────────────────────────────────
-- Maps to images/state.ImageRecord.
-- Keyed by full Docker image ID (sha256:...).
-- repo_tags and repo_digests are JSON arrays, e.g. '["ubuntu:22.04"]'.
-- status: present | deleted | deleted_out_of_band | errored
-- origin: in_band | out_of_band

CREATE TABLE images (
    docker_id        TEXT    PRIMARY KEY,  -- sha256:... full ID
    short_id         TEXT    NOT NULL,
    config_digest    TEXT    NOT NULL DEFAULT '',   -- Fingerprint.ConfigDigest
    layer_chain      TEXT    NOT NULL DEFAULT '',   -- Fingerprint.LayerChain (colon-joined)
    repo_tags        TEXT    NOT NULL DEFAULT '[]', -- JSON array
    repo_digests     TEXT    NOT NULL DEFAULT '[]', -- JSON array
    os               TEXT    NOT NULL DEFAULT '',
    architecture     TEXT    NOT NULL DEFAULT '',
    variant          TEXT    NOT NULL DEFAULT '',
    size             INTEGER NOT NULL DEFAULT 0,
    image_created_at TEXT    NOT NULL DEFAULT '',   -- RFC3339 UTC
    registered_at    TEXT    NOT NULL,              -- RFC3339 UTC
    updated_at       TEXT    NOT NULL,              -- RFC3339 UTC
    origin           TEXT    NOT NULL,              -- in_band | out_of_band
    status           TEXT    NOT NULL,              -- present | deleted | deleted_out_of_band | errored
    err_msg          TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX idx_images_status ON images (status);
CREATE INDEX idx_images_origin ON images (origin);

-- ── Volumes ──────────────────────────────────────────────────────────────────
-- Maps to volumes/state.VolumeRecord.
-- Keyed by volume name (Docker's stable primary identifier).
-- labels and options are JSON objects, e.g. '{"key":"value"}'.
-- status: present | deleted | deleted_out_of_band | errored
-- origin: in_band | out_of_band

CREATE TABLE volumes (
    name          TEXT    PRIMARY KEY,
    driver        TEXT    NOT NULL DEFAULT '',
    mountpoint    TEXT    NOT NULL DEFAULT '',
    scope         TEXT    NOT NULL DEFAULT '',
    labels        TEXT    NOT NULL DEFAULT '{}',  -- JSON object
    options       TEXT    NOT NULL DEFAULT '{}',  -- JSON object
    registered_at TEXT    NOT NULL,               -- RFC3339 UTC
    updated_at    TEXT    NOT NULL,               -- RFC3339 UTC
    origin        TEXT    NOT NULL,               -- in_band | out_of_band
    status        TEXT    NOT NULL,               -- present | deleted | deleted_out_of_band | errored
    err_msg       TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX idx_volumes_status ON volumes (status);
CREATE INDEX idx_volumes_origin ON volumes (origin);

-- ── Networks ─────────────────────────────────────────────────────────────────
-- Maps to networks/state.NetworkRecord.
-- Keyed by Docker network ID (stable hash).
-- status: present | deleted | deleted_out_of_band | errored
-- origin: in_band | out_of_band

CREATE TABLE networks (
    docker_id     TEXT     PRIMARY KEY,
    short_id      TEXT     NOT NULL,
    name          TEXT     NOT NULL DEFAULT '',
    driver        TEXT     NOT NULL DEFAULT '',
    scope         TEXT     NOT NULL DEFAULT '',
    internal      INTEGER  NOT NULL DEFAULT 0,    -- boolean: 0/1
    attachable    INTEGER  NOT NULL DEFAULT 0,    -- boolean: 0/1
    enable_ipv6   INTEGER  NOT NULL DEFAULT 0,    -- boolean: 0/1
    registered_at TEXT     NOT NULL,              -- RFC3339 UTC
    updated_at    TEXT     NOT NULL,              -- RFC3339 UTC
    origin        TEXT     NOT NULL,              -- in_band | out_of_band
    status        TEXT     NOT NULL,              -- present | deleted | deleted_out_of_band | errored
    err_msg       TEXT     NOT NULL DEFAULT ''
);

CREATE INDEX idx_networks_status ON networks (status);
CREATE INDEX idx_networks_origin ON networks (origin);

-- ── Builds ───────────────────────────────────────────────────────────────────
-- Maps to builds/state.BuildRecord.
-- Keyed by state-machine change ID (immutable event log, no reconcile).
-- tags is a JSON array, e.g. '["myapp:latest","myapp:v1.2"]'.
-- status: pending | succeeded | failed | abandoned

CREATE TABLE builds (
    change_id       TEXT    PRIMARY KEY,
    tags            TEXT    NOT NULL DEFAULT '[]', -- JSON array
    dockerfile      TEXT    NOT NULL DEFAULT '',
    context_dir     TEXT    NOT NULL DEFAULT '',
    platform        TEXT    NOT NULL DEFAULT '',
    result_image_id TEXT    NOT NULL DEFAULT '',   -- sha256 of produced image
    err_msg         TEXT    NOT NULL DEFAULT '',
    registered_at   TEXT    NOT NULL,              -- RFC3339 UTC
    finished_at     TEXT    NOT NULL DEFAULT '',   -- RFC3339 UTC; empty until settled
    status          TEXT    NOT NULL               -- pending | succeeded | failed | abandoned
);

CREATE INDEX idx_builds_status ON builds (status);

-- ── Exec Sessions ────────────────────────────────────────────────────────────
-- Maps to containerexec/state.ExecRecord.
-- Keyed by Docker exec ID.  Ephemeral events — no reconcile, no origin.
-- cmd is a JSON array, e.g. '["/bin/sh","-c","ls"]'.
-- status: created | running | finished | errored

CREATE TABLE exec_sessions (
    exec_id       TEXT     PRIMARY KEY,
    container_id  TEXT     NOT NULL,
    cmd           TEXT     NOT NULL DEFAULT '[]',  -- JSON array
    tty           INTEGER  NOT NULL DEFAULT 0,     -- boolean: 0/1
    detach        INTEGER  NOT NULL DEFAULT 0,     -- boolean: 0/1
    exit_code     INTEGER  NOT NULL DEFAULT 0,
    err_msg       TEXT     NOT NULL DEFAULT '',
    registered_at TEXT     NOT NULL,               -- RFC3339 UTC
    started_at    TEXT     NOT NULL DEFAULT '',    -- RFC3339 UTC; empty until running
    finished_at   TEXT     NOT NULL DEFAULT '',    -- RFC3339 UTC; empty until settled
    status        TEXT     NOT NULL                -- created | running | finished | errored
);

CREATE INDEX idx_exec_sessions_container_id ON exec_sessions (container_id);
CREATE INDEX idx_exec_sessions_status       ON exec_sessions (status);
