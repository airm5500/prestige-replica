package com.prestige.replica.domain;

import io.quarkus.hibernate.orm.panache.PanacheEntityBase;
import jakarta.persistence.*;
import java.time.Instant;
import java.util.UUID;

/** Groupe primaire/répliques et seuils d'alerte associés. */
@Entity
@Table(name = "replication_topology")
public class Topology extends PanacheEntityBase {
    @Id public UUID id = UUID.randomUUID();
    @Column(name = "tenant_id") public UUID tenantId;
    @Column(name = "site_id") public UUID siteId;
    public String name;
    /** Mode de la spécification : A, B, C ou D. */
    public String mode = "A";
    public boolean maintenance;
    @Column(name = "lag_warn_seconds") public int lagWarnSeconds = 60;
    @Column(name = "lag_crit_seconds") public int lagCritSeconds = 300;
    @Column(name = "disk_warn_pct") public int diskWarnPct = 20;
    @Column(name = "disk_crit_pct") public int diskCritPct = 10;
    @Column(name = "agent_silence_seconds") public int agentSilenceSeconds = 90;
    @Column(name = "created_at") public Instant createdAt = Instant.now();

    // Politique de sauvegarde (Lot 2)
    @Column(name = "backup_enabled") public boolean backupEnabled;
    /** Nœud qui exécute les sauvegardes (la réplique en mode A, §4). */
    @Column(name = "backup_node_id") public UUID backupNodeId;
    /** Nœud qui exécute les restaurations de contrôle. */
    @Column(name = "restore_node_id") public UUID restoreNodeId;
    @Column(name = "backup_hour") public int backupHour = 2;
    @Column(name = "backup_keep") public int backupKeep = 7;
    /** 1 = lundi … 7 = dimanche. */
    @Column(name = "restore_test_weekday") public int restoreTestWeekday = 7;
    @Column(name = "backup_max_age_hours") public int backupMaxAgeHours = 26;

    /** Lot 4 : rappel si aucun exercice de bascule réussi depuis ce nombre de jours (0 = jamais). */
    @Column(name = "drill_max_age_days") public int drillMaxAgeDays = 90;

    // Archivage continu des binlogs (restauration à un instant précis)
    @Column(name = "binlog_archive_enabled") public boolean binlogArchiveEnabled;
    @Column(name = "binlog_archive_minutes") public int binlogArchiveMinutes = 5;
    /** Nœud qui archive ; à défaut, celui des sauvegardes. */
    @Column(name = "binlog_archive_node_id") public UUID binlogArchiveNodeId;
    /** Fichiers purgés sur la source avant archivage : pas de restauration à un instant dans cet intervalle. */
    @Column(name = "binlog_gap_at") public Instant binlogGapAt;
    @Column(name = "binlog_gap_detail") public String binlogGapDetail;

    // Contrôles de cohérence (§7)
    @Column(name = "consistency_enabled") public boolean consistencyEnabled;
    /** 1 = lundi … 7 = dimanche, heure de la pharmacie. */
    @Column(name = "consistency_weekday") public int consistencyWeekday = 7;
    @Column(name = "consistency_hour") public int consistencyHour = 4;
    @Column(name = "consistency_mode") public String consistencyMode = "FULL";
    /** Bases à comparer, séparées par des virgules (vide = toutes sauf système). */
    @Column(name = "consistency_databases") public String consistencyDatabases;
    /** Exclusions « base.table » ou « base.* », séparées par des virgules. */
    @Column(name = "consistency_exclude") public String consistencyExclude;
    @Column(name = "consistency_max_age_days") public int consistencyMaxAgeDays = 30;
}
