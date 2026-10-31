package com.prestige.replica.domain;

/** Liste fermée des opérations du Lot 2, identique à celle de l'agent. */
public enum OperationType {
    PREFLIGHT(false, "Contrôle des prérequis"),
    BACKUP_FULL(false, "Sauvegarde complète"),
    RESTORE_TEST(false, "Restauration de contrôle"),
    BACKUP_PRUNE(false, "Purge des sauvegardes expirées"),
    /** Destructif pour la cible : double approbation (§8). */
    PROVISION_REPLICA(true, "Construction d'une réplique");

    public final boolean requiresApproval;
    public final String label;

    OperationType(boolean requiresApproval, String label) {
        this.requiresApproval = requiresApproval;
        this.label = label;
    }
}
