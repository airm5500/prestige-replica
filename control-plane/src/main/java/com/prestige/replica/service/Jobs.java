package com.prestige.replica.service;

import com.prestige.replica.domain.NodeMetric;
import io.quarkus.scheduler.Scheduled;
import jakarta.enterprise.context.ApplicationScoped;
import jakarta.inject.Inject;
import jakarta.transaction.Transactional;
import java.time.Duration;
import java.time.Instant;
import java.util.List;
import org.eclipse.microprofile.config.inject.ConfigProperty;

/** Tâches périodiques : détection des agents silencieux et rétention des métriques. */
@ApplicationScoped
public class Jobs {
    @Inject MonitoringService monitoring;
    @Inject BackupService backups;
    @Inject OperationService operations;
    @Inject ReplicationService replication;
    @Inject BinlogService binlogs;

    @ConfigProperty(name = "prm.operations.stale-after", defaultValue = "PT2H")
    java.time.Duration staleAfter;

    @ConfigProperty(name = "prm.metrics.retention-days", defaultValue = "30")
    int retentionDays;

    @Scheduled(every = "${prm.health.evaluation-interval:30s}", delayed = "10s",
            concurrentExecution = Scheduled.ConcurrentExecution.SKIP)
    void evaluate() {
        monitoring.evaluateAll();
    }

    @Scheduled(every = "${prm.backup.schedule-interval:60s}", delayed = "20s",
            concurrentExecution = Scheduled.ConcurrentExecution.SKIP)
    void scheduleBackups() {
        operations.markStale(staleAfter);
        replication.expireFence(Instant.now());
        backups.schedule(Instant.now());
        binlogs.schedule(Instant.now());
    }

    @Scheduled(every = "${prm.drill.interval:5s}", delayed = "15s", concurrentExecution = Scheduled.ConcurrentExecution.SKIP)
    void drills() {
        replication.advanceDrills(Instant.now());
    }

    @Transactional
    public int purgeSearchResults(Instant before) {
        List<com.prestige.replica.domain.Operation> old = com.prestige.replica.domain.Operation.list(
                "type = ?1 and finishedAt < ?2 and result is not null", com.prestige.replica.domain.OperationType.BINLOG_SEARCH, before);
        for (var o : old) {
            if (!o.result.contains("\"purged\"")) o.result = "{\"purged\":true}";
        }
        return old.size();
    }

    @Scheduled(cron = "0 15 3 * * ?")
    @Transactional
    void purgeMetrics() {
        NodeMetric.delete("collectedAt < ?1", Instant.now().minus(Duration.ofDays(retentionDays)));
        // Archivages réussis : seul le catalogue compte, l'historique des opérations est allégé après 7 jours.
        Instant old = Instant.now().minus(Duration.ofDays(7));
        com.prestige.replica.domain.OperationEvent.delete("operationId in (select o.id from Operation o where o.type = ?1 and o.status = ?2 and o.finishedAt < ?3)",
                com.prestige.replica.domain.OperationType.BINLOG_ARCHIVE, com.prestige.replica.domain.OperationStatus.SUCCEEDED, old);
        // Recherches dans les journaux : aperçu des données effacé de la console après 30 jours (le résultat
        // complet reste chiffré dans le stockage de la pharmacie).
        purgeSearchResults(Instant.now().minus(Duration.ofDays(30)));
        com.prestige.replica.domain.Operation.delete("type = ?1 and status = ?2 and finishedAt < ?3",
                com.prestige.replica.domain.OperationType.BINLOG_ARCHIVE, com.prestige.replica.domain.OperationStatus.SUCCEEDED, old);
    }
}
