-- Archivage continu des binlogs et restauration à un instant précis.
CREATE TABLE binlog_archive (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    site_id UUID NOT NULL,
    topology_id UUID NOT NULL,
    server_id BIGINT NOT NULL,
    file_name VARCHAR(120) NOT NULL,
    relative_path VARCHAR(500) NOT NULL,
    size_bytes BIGINT,
    sha256 VARCHAR(64),
    first_gtid VARCHAR(200),
    last_gtid VARCHAR(200),
    first_event_at TIMESTAMPTZ,
    last_event_at TIMESTAMPTZ,
    complete BOOLEAN NOT NULL DEFAULT FALSE,
    archived_at TIMESTAMPTZ NOT NULL,
    UNIQUE (topology_id, server_id, file_name)
);
CREATE INDEX binlog_archive_topology ON binlog_archive (topology_id, last_event_at);

ALTER TABLE replication_topology
    ADD COLUMN binlog_archive_enabled BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN binlog_archive_minutes INTEGER NOT NULL DEFAULT 5,
    ADD COLUMN binlog_archive_node_id UUID,
    ADD COLUMN binlog_gap_at TIMESTAMPTZ,
    ADD COLUMN binlog_gap_detail VARCHAR(500);
