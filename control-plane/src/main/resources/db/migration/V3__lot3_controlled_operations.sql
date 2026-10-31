-- Lot 3 : opérations contrôlées (pause/reprise, bascule planifiée orchestrée).

-- Une bascule est une opération parente pilotée par la console ; ses étapes
-- sont des opérations ordinaires exécutées par les agents.
ALTER TABLE operation ADD COLUMN parent_id UUID REFERENCES operation (id);
CREATE INDEX operation_parent ON operation (parent_id);

-- Pause volontaire : les alertes « thread arrêté » deviennent un simple rappel.
ALTER TABLE database_node
    ADD COLUMN replication_paused_at TIMESTAMPTZ,
    ADD COLUMN replication_paused_by VARCHAR(100),
    ADD COLUMN replication_pause_reason VARCHAR(500);
