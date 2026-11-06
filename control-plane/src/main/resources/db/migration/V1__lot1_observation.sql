-- Prestige Replica Manager — Lot 1 : inventaire, observation, incidents et audit.
-- Toutes les ressources portent tenant_id (et site_id lorsque pertinent) afin
-- d'appliquer l'isolation côté serveur (spécification §19.2).

CREATE TABLE tenant (
    id          UUID PRIMARY KEY,
    code        VARCHAR(32)  NOT NULL UNIQUE,
    name        VARCHAR(200) NOT NULL,
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT now()
);

CREATE TABLE site (
    id           UUID PRIMARY KEY,
    tenant_id    UUID NOT NULL REFERENCES tenant(id),
    code         VARCHAR(16)  NOT NULL,
    name         VARCHAR(200) NOT NULL,
    environment  VARCHAR(32)  NOT NULL DEFAULT 'production',
    color        VARCHAR(16)  NOT NULL DEFAULT '#2563eb',
    timezone     VARCHAR(64)  NOT NULL DEFAULT 'UTC',
    created_at   TIMESTAMPTZ  NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, code)
);

CREATE TABLE app_user (
    id            UUID PRIMARY KEY,
    username      VARCHAR(100) NOT NULL UNIQUE,
    password_hash VARCHAR(200) NOT NULL,
    role          VARCHAR(32)  NOT NULL,
    tenant_id     UUID REFERENCES tenant(id),
    site_id       UUID REFERENCES site(id),
    display_name  VARCHAR(200),
    enabled       BOOLEAN NOT NULL DEFAULT TRUE,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE replication_topology (
    id                 UUID PRIMARY KEY,
    tenant_id          UUID NOT NULL REFERENCES tenant(id),
    site_id            UUID NOT NULL REFERENCES site(id),
    name               VARCHAR(120) NOT NULL,
    mode               VARCHAR(8)   NOT NULL DEFAULT 'A',
    maintenance        BOOLEAN      NOT NULL DEFAULT FALSE,
    lag_warn_seconds   INTEGER      NOT NULL DEFAULT 60,
    lag_crit_seconds   INTEGER      NOT NULL DEFAULT 300,
    disk_warn_pct      INTEGER      NOT NULL DEFAULT 20,
    disk_crit_pct      INTEGER      NOT NULL DEFAULT 10,
    agent_silence_seconds INTEGER   NOT NULL DEFAULT 90,
    created_at         TIMESTAMPTZ  NOT NULL DEFAULT now(),
    UNIQUE (site_id, name)
);

CREATE TABLE database_node (
    id                 UUID PRIMARY KEY,
    tenant_id          UUID NOT NULL REFERENCES tenant(id),
    site_id            UUID NOT NULL REFERENCES site(id),
    topology_id        UUID REFERENCES replication_topology(id),
    name               VARCHAR(120) NOT NULL,
    expected_role      VARCHAR(24)  NOT NULL,
    address_label      VARCHAR(200),
    agent_token_hash   CHAR(64)     NOT NULL UNIQUE,
    agent_token_prefix VARCHAR(12)  NOT NULL,
    agent_version      VARCHAR(40),
    agent_hostname     VARCHAR(200),
    agent_os           VARCHAR(40),
    last_seen_at       TIMESTAMPTZ,
    last_report_at     TIMESTAMPTZ,
    last_report        JSONB,
    created_at         TIMESTAMPTZ  NOT NULL DEFAULT now(),
    UNIQUE (site_id, name)
);

CREATE TABLE node_metric (
    id                 BIGSERIAL PRIMARY KEY,
    tenant_id          UUID NOT NULL,
    node_id            UUID NOT NULL REFERENCES database_node(id) ON DELETE CASCADE,
    collected_at       TIMESTAMPTZ NOT NULL,
    received_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    reachable          BOOLEAN NOT NULL,
    lag_seconds        BIGINT,
    io_running         BOOLEAN,
    sql_running        BOOLEAN,
    binlog_bytes       BIGINT,
    relay_log_bytes    BIGINT,
    min_disk_free_pct  DOUBLE PRECISION,
    threads_connected  BIGINT,
    questions          BIGINT,
    binlog_bytes_written BIGINT,
    gtid_current_pos   VARCHAR(500)
);
CREATE INDEX node_metric_node_time ON node_metric (node_id, collected_at DESC);

CREATE TABLE health_incident (
    id               UUID PRIMARY KEY,
    tenant_id        UUID NOT NULL REFERENCES tenant(id),
    site_id          UUID NOT NULL REFERENCES site(id),
    topology_id      UUID REFERENCES replication_topology(id),
    node_id          UUID REFERENCES database_node(id) ON DELETE CASCADE,
    type             VARCHAR(48)  NOT NULL,
    dedup_key        VARCHAR(200) NOT NULL,
    severity         VARCHAR(16)  NOT NULL,
    status           VARCHAR(16)  NOT NULL,
    message          VARCHAR(1000) NOT NULL,
    details          TEXT,
    opened_at        TIMESTAMPTZ NOT NULL,
    last_seen_at     TIMESTAMPTZ NOT NULL,
    acknowledged_at  TIMESTAMPTZ,
    acknowledged_by  VARCHAR(100),
    assignee         VARCHAR(100),
    resolved_at      TIMESTAMPTZ
);
CREATE INDEX health_incident_open ON health_incident (tenant_id, status);
-- Un seul incident non résolu par clé de déduplication.
CREATE UNIQUE INDEX health_incident_dedup ON health_incident (dedup_key) WHERE status <> 'RESOLVED';

CREATE TABLE notification_channel (
    id            UUID PRIMARY KEY,
    tenant_id     UUID NOT NULL REFERENCES tenant(id),
    site_id       UUID REFERENCES site(id),
    type          VARCHAR(16)  NOT NULL,
    target        VARCHAR(500) NOT NULL,
    min_severity  VARCHAR(16)  NOT NULL DEFAULT 'WARNING',
    enabled       BOOLEAN NOT NULL DEFAULT TRUE,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Journal d'audit append-only chaîné par empreinte SHA-256.
CREATE TABLE audit_event (
    id          BIGSERIAL PRIMARY KEY,
    at          TIMESTAMPTZ  NOT NULL,
    tenant_id   UUID,
    site_id     UUID,
    actor       VARCHAR(100) NOT NULL,
    action      VARCHAR(80)  NOT NULL,
    resource    VARCHAR(200),
    result      VARCHAR(16)  NOT NULL,
    details     TEXT,
    prev_hash   CHAR(64)     NOT NULL,
    hash        CHAR(64)     NOT NULL UNIQUE
);
CREATE INDEX audit_event_tenant_time ON audit_event (tenant_id, at DESC);

CREATE FUNCTION audit_event_immutable() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'audit_event est en ajout seul : % interdit', TG_OP;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER audit_event_no_update BEFORE UPDATE OR DELETE ON audit_event
    FOR EACH ROW EXECUTE FUNCTION audit_event_immutable();
CREATE TRIGGER audit_event_no_truncate BEFORE TRUNCATE ON audit_event
    FOR EACH STATEMENT EXECUTE FUNCTION audit_event_immutable();
