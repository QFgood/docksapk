PRAGMA foreign_keys = ON;
PRAGMA journal_mode = WAL;
CREATE TABLE IF NOT EXISTS instance (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    id TEXT NOT NULL,
    shortcuts_version INTEGER NOT NULL DEFAULT 1,
    rules_version INTEGER NOT NULL DEFAULT 1
);
CREATE TABLE IF NOT EXISTS pairing_codes (
    digest TEXT PRIMARY KEY,
    expires INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS devices (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    digest TEXT NOT NULL UNIQUE,
    created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS shortcuts (
    id TEXT PRIMARY KEY,
    data TEXT NOT NULL,
    source TEXT,
    source_id TEXT,
    override TEXT,
    hidden INTEGER NOT NULL DEFAULT 0,
    UNIQUE (source, source_id)
);
CREATE TABLE IF NOT EXISTS requests (
    device_id TEXT NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    key TEXT NOT NULL,
    fingerprint TEXT NOT NULL,
    response TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (device_id, key)
);
CREATE TABLE IF NOT EXISTS import_state (
    source TEXT PRIMARY KEY,
    last_success TEXT,
    error TEXT
);
CREATE TABLE IF NOT EXISTS events (
    id TEXT PRIMARY KEY,
    source TEXT NOT NULL,
    source_id TEXT NOT NULL,
    occurred_at TEXT NOT NULL,
    received_at TEXT NOT NULL,
    service TEXT NOT NULL,
    level TEXT NOT NULL,
    summary TEXT NOT NULL,
    shortcut_id TEXT NOT NULL DEFAULT '',
    recovery_of TEXT NOT NULL DEFAULT '',
    read INTEGER NOT NULL DEFAULT 0,
    UNIQUE (source, source_id)
);
CREATE INDEX IF NOT EXISTS events_timeline ON events(occurred_at DESC, id DESC);
CREATE TABLE IF NOT EXISTS notification_rules (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    data TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS host_samples (
    sampled_at INTEGER PRIMARY KEY,
    data TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS host_rollups (
    bucket INTEGER PRIMARY KEY,
    samples INTEGER NOT NULL,
    cpu_sum REAL NOT NULL,
    memory_sum INTEGER NOT NULL,
    disk_sum INTEGER NOT NULL,
    rx_sum INTEGER NOT NULL,
    tx_sum INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS collector_cursor (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    docker_until TEXT NOT NULL
);
