-- Lot 4 : bascule d'urgence. Un ancien primaire isolé reste marqué tant qu'il
-- n'a pas été reconstruit : s'il redevient accessible en écriture, la console
-- le remet aussitôt en lecture seule.
ALTER TABLE database_node
    ADD COLUMN fence_required BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN fence_reason VARCHAR(500);
