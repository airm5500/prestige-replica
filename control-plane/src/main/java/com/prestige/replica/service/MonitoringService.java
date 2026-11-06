package com.prestige.replica.service;

import com.fasterxml.jackson.core.JsonProcessingException;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.prestige.replica.alert.AgentReport;
import com.prestige.replica.alert.AlertRules;
import com.prestige.replica.alert.Finding;
import com.prestige.replica.domain.DatabaseNode;
import com.prestige.replica.domain.Severity;
import com.prestige.replica.domain.NodeMetric;
import com.prestige.replica.domain.Topology;
import jakarta.enterprise.context.ApplicationScoped;
import jakarta.inject.Inject;
import jakarta.transaction.Transactional;
import java.time.Duration;
import java.time.Instant;
import java.util.ArrayList;
import java.util.Comparator;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.UUID;
import org.jboss.logging.Logger;

/** Ingestion des rapports d'agent et évaluation de la santé. */
@ApplicationScoped
public class MonitoringService {
    private static final Logger LOG = Logger.getLogger(MonitoringService.class);
    /** Tolérance d'horloge : un rapport daté du futur au-delà est refusé. */
    private static final Duration MAX_CLOCK_SKEW = Duration.ofMinutes(10);

    @Inject ObjectMapper json;
    @Inject IncidentService incidents;
    @Inject jakarta.enterprise.inject.Instance<BackupService> backups;
    @Inject jakarta.enterprise.inject.Instance<ReplicationService> replication;
    @Inject jakarta.enterprise.inject.Instance<BinlogService> binlogs;
    @Inject jakarta.enterprise.inject.Instance<ConsistencyService> consistency;

    @Transactional
    public int ingest(UUID nodeId, List<AgentReport> reports) {
        DatabaseNode node = DatabaseNode.findById(nodeId);
        Instant now = Instant.now();
        int accepted = 0;
        AgentReport latest = null;
        List<AgentReport> sorted = new ArrayList<>(reports);
        sorted.removeIf(r -> r == null || r.collectedAt() == null || r.collectedAt().isAfter(now.plus(MAX_CLOCK_SKEW)));
        sorted.sort(Comparator.comparing(AgentReport::collectedAt));
        for (AgentReport r : sorted) {
            NodeMetric m = toMetric(node, r);
            m.receivedAt = now;
            m.persist();
            accepted++;
            if (node.lastReportAt == null || !r.collectedAt().isBefore(node.lastReportAt)) {
                latest = r;
            }
        }
        node.lastSeenAt = now;
        if (latest != null) {
            node.lastReportAt = latest.collectedAt();
            node.agentVersion = latest.agentVersion();
            node.agentHostname = latest.hostname();
            node.agentOs = latest.os() + "/" + latest.arch();
            try {
                node.lastReport = json.writeValueAsString(latest);
            } catch (JsonProcessingException e) {
                LOG.warn("Sérialisation du rapport impossible", e);
            }
        }
        if (latest != null && node.fenceRequired) replication.get().enforceFence(node, latest);
        evaluateNode(node, now);
        if (node.topologyId != null) evaluateTopology(node.topologyId, now);
        return accepted;
    }

    @Transactional
    public void evaluateAll() {
        Instant now = Instant.now();
        for (DatabaseNode n : DatabaseNode.<DatabaseNode>listAll()) evaluateNode(n, now);
        for (Topology t : Topology.<Topology>listAll()) evaluateTopology(t.id, now);
    }

    void evaluateNode(DatabaseNode node, Instant now) {
        Topology topo = node.topologyId == null ? null : Topology.findById(node.topologyId);
        List<Finding> findings;
        if (topo != null && topo.maintenance) {
            findings = List.of(); // fenêtre de maintenance : aucun incident ouvert ni notifié
        } else {
            AgentReport report = parse(node.lastReport);
            findings = AlertRules.evaluate(node.expectedRole, report, node.lastSeenAt, now, thresholds(topo));
            findings = applyPause(node, report, findings);
            findings = applyFence(node, report, findings);
        }
        incidents.reconcile(IncidentService.Scope.node(node.tenantId, node.siteId, node.topologyId, node.id), findings, now);
    }

    /** Ancien primaire isolé : un seul rappel, critique s'il accepte de nouveau les écritures. */
    static List<Finding> applyFence(DatabaseNode node, AgentReport r, List<Finding> findings) {
        if (!node.fenceRequired) return findings;
        boolean writable = r != null && r.reachable() && r.server() != null && !r.server().readOnly();
        return List.of(new Finding("NODE_FENCED", "", writable ? Severity.CRITICAL : Severity.WARNING,
                writable ? node.name + " isolé est de nouveau accessible en écriture (remise en lecture seule demandée)"
                        : node.name + " isolé après une bascule d'urgence : à reconstruire depuis une sauvegarde",
                node.fenceReason == null ? "" : node.fenceReason));
    }

    static final Set<String> PAUSE_MASKED = Set.of("IO_THREAD_STOPPED", "SQL_THREAD_STOPPED", "REPLICATION_LAG");

    /**
     * Pause volontaire : les alertes « thread arrêté » et « retard » laissent
     * place à un rappel unique. Si la réplication a été relancée hors console,
     * la pause est levée.
     */
    static List<Finding> applyPause(DatabaseNode node, AgentReport r, List<Finding> findings) {
        if (node.replicationPausedAt == null) return findings;
        if (r != null && r.collectedAt() != null && r.collectedAt().isAfter(node.replicationPausedAt)
                && !r.channelsOrEmpty().isEmpty() && r.channelsOrEmpty().stream().allMatch(c -> c.ioUp() && c.sqlUp())) {
            node.replicationPausedAt = null;
            node.replicationPausedBy = null;
            node.replicationPauseReason = null;
            return findings;
        }
        List<Finding> out = new ArrayList<>(findings.stream().filter(f -> !PAUSE_MASKED.contains(f.type())).toList());
        out.add(new Finding("REPLICATION_PAUSED", "", Severity.WARNING, "Réplication suspendue par " + node.replicationPausedBy
                + (node.replicationPauseReason == null ? "" : " : " + node.replicationPauseReason), "Depuis " + node.replicationPausedAt));
        return out;
    }

    void evaluateTopology(UUID topologyId, Instant now) {
        Topology topo = Topology.findById(topologyId);
        if (topo == null) return;
        Map<String, Long> ids = new LinkedHashMap<>();
        for (DatabaseNode n : DatabaseNode.<DatabaseNode>list("topologyId", topologyId)) {
            AgentReport r = parse(n.lastReport);
            if (r != null && r.server() != null) ids.put(n.name, r.server().serverId());
        }
        List<Finding> f = new ArrayList<>();
        if (!topo.maintenance) {
            f.addAll(AlertRules.evaluateTopology(ids));
            f.addAll(backups.get().findings(topo, now));
            f.addAll(replication.get().drillFindings(topo, now));
            f.addAll(binlogs.get().findings(topo, now));
            f.addAll(consistency.get().findings(topo, now));
        }
        incidents.reconcile(IncidentService.Scope.topology(topo.tenantId, topo.siteId, topo.id), f, now);
    }

    public AgentReport parse(String raw) {
        if (raw == null) return null;
        try {
            return json.readValue(raw, AgentReport.class);
        } catch (JsonProcessingException e) {
            LOG.warn("Rapport stocké illisible", e);
            return null;
        }
    }

    public static AlertRules.Thresholds thresholds(Topology t) {
        return t == null ? AlertRules.Thresholds.DEFAULT
                : new AlertRules.Thresholds(t.lagWarnSeconds, t.lagCritSeconds, t.diskWarnPct, t.diskCritPct, t.agentSilenceSeconds);
    }

    static NodeMetric toMetric(DatabaseNode node, AgentReport r) {
        NodeMetric m = new NodeMetric();
        m.tenantId = node.tenantId;
        m.nodeId = node.id;
        m.collectedAt = r.collectedAt();
        m.reachable = r.reachable();
        if (!r.channelsOrEmpty().isEmpty()) {
            Long worstLag = null;
            boolean io = true, sql = true;
            long relay = 0;
            for (AgentReport.Channel c : r.channelsOrEmpty()) {
                io &= c.ioUp();
                sql &= c.sqlUp();
                relay += c.relayLogSpace();
                if (c.secondsBehindMaster() != null && (worstLag == null || c.secondsBehindMaster() > worstLag)) {
                    worstLag = c.secondsBehindMaster();
                }
            }
            m.lagSeconds = worstLag;
            m.ioRunning = io;
            m.sqlRunning = sql;
            m.relayLogBytes = relay;
        }
        if (r.binlogs() != null) m.binlogBytes = r.binlogs().totalBytes();
        r.disksOrEmpty().stream().mapToDouble(AgentReport.Disk::freePct).min().ifPresent(v -> m.minDiskFreePct = v);
        if (r.server() != null) {
            m.threadsConnected = r.server().threadsConnected();
            m.questions = r.server().questions();
            m.binlogBytesWritten = r.server().binlogBytesWritten();
            m.gtidCurrentPos = r.server().gtidCurrentPos();
        }
        return m;
    }
}
