package com.prestige.replica.domain;

/** Liste fermée des opérations, identique à celle de l'agent (sauf celles pilotées par la console). */
public enum OperationType {
    PREFLIGHT(false, "Contrôle des prérequis"),
    BACKUP_FULL(false, "Sauvegarde complète"),
    RESTORE_TEST(false, "Restauration de contrôle"),
    BACKUP_PRUNE(false, "Purge des sauvegardes expirées"),
    /** Destructif pour la cible : double approbation (§8). */
    PROVISION_REPLICA(true, "Construction d'une réplique"),

    // Lot 3
    REPLICATION_PAUSE(false, "Suspension de la réplication"),
    REPLICATION_RESUME(false, "Reprise de la réplication"),
    /** Bascule planifiée : orchestrée par la console, jamais envoyée à un agent. */
    SWITCHOVER(true, "Bascule planifiée du primaire", true),
    SWITCHOVER_DEMOTE(false, "Rétrogradation de l'ancien primaire"),
    SWITCHOVER_PROMOTE(false, "Promotion de la nouvelle source"),
    SWITCHOVER_UNDO(false, "Retour arrière : ancien primaire rouvert en écriture"),
    REPLICA_ATTACH(false, "Rattachement à la nouvelle source"),

    // Lot 4
    /** Bascule d'urgence : le primaire est perdu ; perte possible des dernières transactions. */
    FAILOVER(true, "Bascule d'urgence (primaire perdu)", true),
    FENCE(false, "Isolement de l'ancien primaire"),
    /** Exercice de panne : bascule planifiée aller puis retour, mesurée, éventuellement programmée la nuit. */
    DRILL(true, "Exercice de bascule (aller-retour)", true),

    // Archivage des binlogs et restauration à un instant précis
    BINLOG_ARCHIVE(false, "Archivage des journaux binaires"),
    /** Lecture seule de l'archive : vue d'audit des transactions (qui a modifié quoi, quand). */
    BINLOG_SEARCH(false, "Recherche dans les journaux binaires"),
    /** Lecture seule des deux côtés ; jamais de correction automatique (§7). */
    CONSISTENCY_CHECK(false, "Contrôle de cohérence avec le primaire"),
    /** Ne touche jamais la production : instance isolée, extraction chiffrée. */
    PITR_RESTORE(false, "Restauration à un instant précis (isolée)");

    public final boolean requiresApproval;
    public final String label;
    /** Opération pilotée par la console (aucun agent ne l'exécute). */
    public final boolean consoleOnly;

    OperationType(boolean requiresApproval, String label) {
        this(requiresApproval, label, false);
    }

    OperationType(boolean requiresApproval, String label, boolean consoleOnly) {
        this.requiresApproval = requiresApproval;
        this.label = label;
        this.consoleOnly = consoleOnly;
    }
}
