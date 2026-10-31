-- Exercices de panne : rappel si aucun exercice réussi depuis N jours (0 = désactivé).
ALTER TABLE replication_topology ADD COLUMN drill_max_age_days INTEGER NOT NULL DEFAULT 90;
