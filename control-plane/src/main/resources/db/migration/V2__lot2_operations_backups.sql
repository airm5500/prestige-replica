-- Lot 2 : opérations contrôlées, catalogue des sauvegardes et restaurations de contrôle.

ALTER TABLE replication_topology
    ADD COLUMN backup_enabled        BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN backup_node_id        UUID REFERENCES database_node(id) ON DELETE SET NULL,
    ADD COLUMN restore_node_id       UUID REFERENCES database_node(id) ON DELETE SET NULL,
    ADD COLUMN backup_hour           INTEGER NOT NULL DEFAULT 2 CHECK (backup_hour BETWEEN 0 AND 23),
    ADD COLUMN backup_keep           INTEGER NOT NULL DEFAULT 7 CHECK (backup_keep BETWEEN 1 AND 365),
    ADD COLUMN restore_test_weekday  INTEGER NOT NULL DEFAULT 7 CHECK (restore_test_weekday BETWEEN 1 AND 7),
    ADD COLUMN backup_max_age_hours  INTEGER NOT NULL DEFAULT 26;

CREATE TABLE operation (
    id                 UUID PRIMARY KEY,
    tenant_id          UUID NOT NULL REFERENCES tenant(id),
    site_id            UUID NOT NULL REFERENCES site(id),
    topology_id        UUID REFERENCES replication_topology(id) ON DELETE SET NULL,
    node_id            UUID NOT NULL REFERENCES database_node(id) ON DELETE CASCADE,
    type               VARCHAR(32)  NOT NULL,
    status             VARCHAR(32)  NOT NULL,
    params             JSONB        NOT NULL,
    summary            VARCHAR(500) NOT NULL,
    requires_approval  BOOLEAN      NOT NULL DEFAULT FALSE,
    requested_by       VARCHAR(100) NOT NULL,
    requested_at       TIMESTAMPTZ  NOT NULL,
    approved_by        VARCHAR(100),
    approved_at        TIMESTAMPTZ,
    started_at         TIMESTAMPTZ,
    finished_at        TIMESTAMPTZ,
    last_event_at      TIMESTAMPTZ,
    error              TEXT,
    result             JSONB
);
CREATE INDEX operation_node_status ON operation (node_id, status);
CREATE INDEX operation_tenant_time ON operation (tenant_id, requested_at DESC);

CREATE TABLE operation_event (
    id            BIGSERIAL PRIMARY KEY,
    operation_id  UUID NOT NULL REFERENCES operation(id) ON DELETE CASCADE,
    at            TIMESTAMPTZ NOT NULL,
    level         VARCHAR(8)  NOT NULL,
    message       VARCHAR(2000) NOT NULL
);
CREATE INDEX operation_event_op ON operation_event (operation_id, id);

CREATE TABLE backup (
    id               UUID PRIMARY KEY,
    tenant_id        UUID NOT NULL REFERENCES tenant(id),
    site_id          UUID NOT NULL REFERENCES site(id),
    topology_id      UUID REFERENCES replication_topology(id) ON DELETE SET NULL,
    node_id          UUID NOT NULL REFERENCES database_node(id) ON DELETE CASCADE,
    operation_id     UUID REFERENCES operation(id) ON DELETE SET NULL,
    kind             VARCHAR(16)  NOT NULL DEFAULT 'FULL',
    relative_dir     VARCHAR(400) NOT NULL UNIQUE,
    status           VARCHAR(16)  NOT NULL,
    started_at       TIMESTAMPTZ  NOT NULL,
    finished_at      TIMESTAMPTZ,
    size_bytes       BIGINT,
    sha256           CHAR(64),
    key_id           VARCHAR(32),
    gtid             VARCHAR(500),
    server_version   VARCHAR(100),
    verify_status    VARCHAR(16)  NOT NULL DEFAULT 'NOT_VERIFIED',
    verified_at      TIMESTAMPTZ,
    restore_seconds  INTEGER
);
CREATE INDEX backup_topology_time ON backup (topology_id, started_at DESC);

CREATE TABLE restore_test (
    id              UUID PRIMARY KEY,
    tenant_id       UUID NOT NULL REFERENCES tenant(id),
    backup_id       UUID NOT NULL REFERENCES backup(id) ON DELETE CASCADE,
    operation_id    UUID REFERENCES operation(id) ON DELETE SET NULL,
    node_id         UUID NOT NULL REFERENCES database_node(id) ON DELETE CASCADE,
    status          VARCHAR(16) NOT NULL,
    at              TIMESTAMPTZ NOT NULL,
    total_seconds   INTEGER,
    gtid            VARCHAR(500),
    details         JSONB
);
