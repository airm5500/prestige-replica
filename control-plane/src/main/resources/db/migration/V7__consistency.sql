-- Contrôles de cohérence primaire / réplique (§7). Les résultats sont ceux des opérations CONSISTENCY_CHECK.
ALTER TABLE replication_topology
    ADD COLUMN consistency_enabled BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN consistency_weekday INTEGER NOT NULL DEFAULT 7,
    ADD COLUMN consistency_hour INTEGER NOT NULL DEFAULT 4,
    ADD COLUMN consistency_mode VARCHAR(10) NOT NULL DEFAULT 'FULL',
    ADD COLUMN consistency_databases TEXT,
    ADD COLUMN consistency_exclude TEXT,
    ADD COLUMN consistency_max_age_days INTEGER NOT NULL DEFAULT 30;
