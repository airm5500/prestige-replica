package com.prestige.replica.service;

import com.prestige.replica.domain.NodeMetric;
import io.quarkus.scheduler.Scheduled;
import jakarta.enterprise.context.ApplicationScoped;
import jakarta.inject.Inject;
import jakarta.transaction.Transactional;
import java.time.Duration;
import java.time.Instant;
import org.eclipse.microprofile.config.inject.ConfigProperty;

/** Tâches périodiques : détection des agents silencieux et rétention des métriques. */
@ApplicationScoped
public class Jobs {
    @Inject MonitoringService monitoring;
    @Inject BackupService backups;
    @Inject OperationService operations;

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
        backups.schedule(Instant.now());
    }

    @Scheduled(cron = "0 15 3 * * ?")
    @Transactional
    void purgeMetrics() {
        NodeMetric.delete("collectedAt < ?1", Instant.now().minus(Duration.ofDays(retentionDays)));
    }
}
