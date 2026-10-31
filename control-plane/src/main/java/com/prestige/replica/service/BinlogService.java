package com.prestige.replica.service;

import com.fasterxml.jackson.databind.JsonNode;
import com.prestige.replica.alert.AgentReport;
import com.prestige.replica.alert.Finding;
import com.prestige.replica.domain.*;
import jakarta.enterprise.context.ApplicationScoped;
import jakarta.inject.Inject;
import jakarta.transaction.Transactional;
import jakarta.ws.rs.BadRequestException;
import java.time.Duration;
import java.time.Instant;
import java.util.*;
import java.util.regex.Pattern;

/**
 * Archivage continu des binlogs du primaire et restauration à un instant
 * précis (§6.1). La console tient le catalogue et choisit les fichiers ;
 * l'agent lit, chiffre, rejoue. La production n'est jamais modifiée.
 */
@ApplicationScoped
public class BinlogService {
    @Inject OperationService ops;
    @Inject MonitoringService monitoring;
    @Inject AuditService audit;

    static final Pattern TABLE = Pattern.compile("^[A-Za-z0-9_$]{1,64}\\.[A-Za-z0-9_$]{1,64}$");

    public static String archiveDir(Topology t) {
        return "tenants/" + t.tenantId + "/sites/" + t.siteId + "/binlogs/" + t.id;
    }

    DatabaseNode archiveNode(Topology t) {
        UUID id = t.binlogArchiveNodeId != null ? t.binlogArchiveNodeId : t.backupNodeId;
        return id == null ? null : DatabaseNode.findById(id);
    }

    /** Lance les archivages dus (toutes les binlogArchiveMinutes). */
    @Transactional
    public void schedule(Instant now) {
        for (Topology t : Topology.<Topology>list("binlogArchiveEnabled = true")) {
            DatabaseNode node = archiveNode(t);
            DatabaseNode primary = DatabaseNode.find("topologyId = ?1 and expectedRole = ?2", t.id, NodeRole.PRIMARY).firstResult();
            if (node == null || primary == null) continue;
            if (Operation.count("topologyId = ?1 and type = ?2 and status in ?3", t.id, OperationType.BINLOG_ARCHIVE,
                    List.of(OperationStatus.QUEUED, OperationStatus.RUNNING)) > 0) continue;
            // Pas pendant une bascule : le primaire change.
            if (ops.switchoverRunning(t.id)) continue;
            Operation last = Operation.find("topologyId = ?1 and type = ?2 order by requestedAt desc", t.id, OperationType.BINLOG_ARCHIVE).firstResult();
            if (last != null && Duration.between(last.requestedAt, now).toMinutes() < Math.max(1, t.binlogArchiveMinutes)) continue;
            AgentReport pr = monitoring.parse(primary.lastReport);
            AgentReport nr = monitoring.parse(node.lastReport);
            if (pr == null || !pr.reachable() || pr.server() == null || !pr.server().logBin()) continue;
            if (nr == null || !nr.capabilitiesOrEmpty().contains("binlogArchive")) continue;
            String[] hp;
            try {
                hp = BackupService.hostPort(primary.addressLabel);
            } catch (BadRequestException e) {
                continue;
            }
            long serverId = pr.server().serverId();
            List<String> archived = BinlogArchive.<BinlogArchive>list("topologyId = ?1 and serverId = ?2 and complete = true", t.id, serverId)
                    .stream().map(b -> b.fileName).toList();
            Map<String, Object> p = new LinkedHashMap<>();
            p.put("sourceHost", hp[0]);
            p.put("sourcePort", Integer.parseInt(hp[1]));
            p.put("serverId", serverId);
            p.put("relativeDir", archiveDir(t));
            p.put("archived", archived);
            p.put("expired", expired(t));
            ops.request(OperationType.BINLOG_ARCHIVE, node, p, "Archivage des journaux de " + primary.name, OperationService.SYSTEM);
        }
    }

    /** Fichiers antérieurs à la plus ancienne sauvegarde conservée (marge d'une heure). */
    List<String> expired(Topology t) {
        Backup oldest = Backup.find("topologyId = ?1 and status = ?2 order by startedAt asc", t.id, Backup.Status.COMPLETED).firstResult();
        if (oldest == null || oldest.startedAt == null) return List.of();
        return BinlogArchive.<BinlogArchive>list("topologyId = ?1 and complete = true and lastEventAt < ?2", t.id,
                oldest.startedAt.minus(Duration.ofHours(1))).stream().map(b -> b.relativePath).limit(200).toList();
    }

    /** Résultat d'un archivage : catalogue mis à jour. */
    @Transactional(Transactional.TxType.MANDATORY)
    public void applyArchive(Operation op, JsonNode params, JsonNode result) {
        if (result == null) return;
        Topology t = Topology.findById(op.topologyId);
        long serverId = params.path("serverId").asLong();
        for (JsonNode f : result.path("files")) {
            String name = f.path("file").asText();
            BinlogArchive b = BinlogArchive.find("topologyId = ?1 and serverId = ?2 and fileName = ?3", t.id, serverId, name).firstResult();
            if (b == null) {
                b = new BinlogArchive();
                b.tenantId = t.tenantId;
                b.siteId = t.siteId;
                b.topologyId = t.id;
                b.serverId = serverId;
                b.fileName = name;
            }
            b.relativePath = f.path("relativePath").asText();
            b.sizeBytes = f.path("sizeBytes").asLong();
            b.sha256 = f.path("sha256").asText(null);
            b.firstGtid = blank(f.path("firstGtid").asText(""));
            b.lastGtid = blank(f.path("lastGtid").asText(""));
            b.firstEventAt = instant(f.path("firstEventAt"));
            b.lastEventAt = instant(f.path("lastEventAt"));
            b.complete = f.path("complete").asBoolean(false);
            b.archivedAt = Instant.now();
            if (b.isPersistent()) continue;
            b.persist();
        }
        for (JsonNode d : result.path("deleted")) BinlogArchive.delete("topologyId = ?1 and relativePath = ?2", t.id, d.asText());
        String gap = result.path("gap").asText("");
        if (!gap.isBlank()) {
            t.binlogGapAt = Instant.now();
            t.binlogGapDetail = gap;
        }
    }

    private static String blank(String s) { return s == null || s.isBlank() ? null : s; }

    private static Instant instant(JsonNode n) {
        if (n == null || n.isNull() || n.isMissingNode() || n.asText().isBlank()) return null;
        return Instant.parse(n.asText());
    }

    /** Constats : archivage en retard, trou dans l'archive. */
    @Transactional(Transactional.TxType.MANDATORY)
    public List<Finding> findings(Topology t, Instant now) {
        if (!t.binlogArchiveEnabled) return List.of();
        List<Finding> out = new ArrayList<>();
        Operation lastOk = Operation.find("topologyId = ?1 and type = ?2 and status = ?3 order by finishedAt desc",
                t.id, OperationType.BINLOG_ARCHIVE, OperationStatus.SUCCEEDED).firstResult();
        long limitMin = Math.max(15, 3L * t.binlogArchiveMinutes);
        Instant since = lastOk != null ? lastOk.finishedAt : t.createdAt;
        if (since != null && Duration.between(since, now).toMinutes() > limitMin) {
            Operation lastFail = Operation.find("topologyId = ?1 and type = ?2 and status <> ?3 order by requestedAt desc",
                    t.id, OperationType.BINLOG_ARCHIVE, OperationStatus.SUCCEEDED).firstResult();
            out.add(new Finding("BINLOG_ARCHIVE_LATE", "", Severity.WARNING,
                    lastOk == null ? "Aucun archivage des journaux binaires réussi"
                            : "Archivage des journaux binaires en retard : dernier succès il y a " + Duration.between(since, now).toMinutes() + " min",
                    lastFail != null && lastFail.error != null ? lastFail.error : "La restauration à un instant précis ne couvre pas la période récente"));
        }
        if (t.binlogGapAt != null && Backup.count("topologyId = ?1 and status = ?2 and startedAt > ?3", t.id, Backup.Status.COMPLETED, t.binlogGapAt) == 0) {
            out.add(new Finding("BINLOG_ARCHIVE_GAP", "", Severity.CRITICAL,
                    "Trou dans l'archive des journaux : restauration à un instant impossible avant la prochaine sauvegarde complète",
                    t.binlogGapDetail + ". Augmentez la rétention des binlogs du primaire (binlog_expire_logs_seconds) et lancez une sauvegarde."));
        }
        return out;
    }

    // ---- Restauration à un instant précis ------------------------------------

    public record BinlogRef(String relativePath, String sha256, String fileName, long serverId, Instant firstEventAt, Instant lastEventAt) {}

    public record PitrPlan(boolean ready, List<ReplicationService.Check> checks, UUID backupId, Instant backupAt, String backupGtid,
                           UUID executorId, String executor, List<BinlogRef> binlogs, Instant coveredUntil) {}

    static long seq(String gtid) {
        if (gtid == null) return -1;
        String[] p = gtid.split(",")[0].trim().split("-");
        try {
            return p.length == 3 ? Long.parseLong(p[2]) : -1;
        } catch (NumberFormatException e) {
            return -1;
        }
    }

    @Transactional(Transactional.TxType.MANDATORY)
    public PitrPlan plan(Topology t, Instant stopAt, Instant now) {
        List<ReplicationService.Check> checks = new ArrayList<>();
        if (stopAt == null) throw new BadRequestException("Instant visé manquant");
        if (stopAt.isAfter(now)) checks.add(new ReplicationService.Check("stop", "FAIL", "L'instant visé est dans le futur"));
        Backup b = Backup.find("topologyId = ?1 and status = ?2 and verifyStatus = ?3 and finishedAt <= ?4 and gtid is not null order by startedAt desc",
                t.id, Backup.Status.COMPLETED, Backup.Verify.VERIFIED, stopAt).firstResult();
        if (b == null) {
            checks.add(new ReplicationService.Check("backup", "FAIL", "Aucune sauvegarde vérifiée, avec position GTID, terminée avant cet instant"));
        } else {
            checks.add(new ReplicationService.Check("backup", "OK", "Point de départ : sauvegarde vérifiée du "
                    + b.startedAt.toString().substring(0, 16).replace('T', ' ') + " UTC (" + b.gtid + ")"));
        }
        DatabaseNode exec = t.restoreNodeId == null ? null : DatabaseNode.findById(t.restoreNodeId);
        AgentReport er = exec == null ? null : monitoring.parse(exec.lastReport);
        if (exec == null || er == null || !er.capabilitiesOrEmpty().contains("restoreTest")) {
            checks.add(new ReplicationService.Check("executor", "FAIL", "Aucun serveur de restauration isolée configuré (restoreTest)"));
        }
        List<BinlogRef> chain = new ArrayList<>();
        Instant covered = null;
        if (b != null) {
            long from = seq(b.gtid);
            List<BinlogArchive> all = BinlogArchive.<BinlogArchive>list("topologyId = ?1 and lastGtid is not null", t.id);
            all.sort(Comparator.comparingLong((BinlogArchive a) -> seq(a.firstGtid)).thenComparing(a -> a.firstEventAt, Comparator.nullsFirst(Comparator.naturalOrder())));
            for (BinlogArchive a : all) {
                if (seq(a.lastGtid) <= from) continue;
                if (a.firstEventAt != null && a.firstEventAt.isAfter(stopAt)) continue;
                chain.add(new BinlogRef(a.relativePath, a.sha256, a.fileName, a.serverId, a.firstEventAt, a.lastEventAt));
                if (a.lastEventAt != null && (covered == null || a.lastEventAt.isAfter(covered))) covered = a.lastEventAt;
            }
            Instant latestArchived = BinlogArchive.<BinlogArchive>find("topologyId = ?1 and lastEventAt is not null order by lastEventAt desc", t.id)
                    .firstResultOptional().map(a -> a.lastEventAt).orElse(null);
            if (!t.binlogArchiveEnabled) {
                checks.add(new ReplicationService.Check("archive", "WARN", "Archivage des journaux désactivé : retour à l'état de la sauvegarde seulement"));
            } else if (latestArchived == null || latestArchived.isBefore(stopAt)) {
                checks.add(new ReplicationService.Check("archive", "WARN", "Journaux archivés jusqu'au " + (latestArchived == null ? "—"
                        : latestArchived.toString().substring(0, 19).replace('T', ' ') + " UTC") + " : les transactions postérieures ne seront pas rejouées"));
            } else {
                checks.add(new ReplicationService.Check("archive", "OK", chain.size() + " fichier(s) de journaux à rejouer"));
            }
            if (t.binlogGapAt != null && t.binlogGapAt.isAfter(b.startedAt)) {
                checks.add(new ReplicationService.Check("gap", "WARN", "Trou signalé dans l'archive (" + t.binlogGapDetail + ") : l'agent signalera les transactions absentes"));
            }
        }
        boolean ready = checks.stream().noneMatch(c -> c.status().equals("FAIL"));
        return new PitrPlan(ready, checks, b == null ? null : b.id, b == null ? null : b.startedAt, b == null ? null : b.gtid,
                exec == null ? null : exec.id, exec == null ? null : exec.name, chain, covered);
    }

    @Transactional(Transactional.TxType.MANDATORY)
    public Operation requestPitr(Topology t, Instant stopAt, List<String> extract, String reason, String actor) {
        if (reason == null || reason.isBlank()) throw new BadRequestException("Motif obligatoire (ex. ligne supprimée par erreur)");
        List<String> tables = extract == null ? List.of() : extract.stream().map(String::trim).filter(s -> !s.isEmpty()).distinct().toList();
        for (String s : tables) if (!TABLE.matcher(s).matches()) throw new BadRequestException("Table invalide : « " + s + " » (forme base.table)");
        if (tables.size() > 20) throw new BadRequestException("20 tables au plus par restauration");
        PitrPlan p = plan(t, stopAt, Instant.now());
        if (!p.ready()) throw new BadRequestException("Restauration impossible : " + String.join(" ; ",
                p.checks().stream().filter(c -> c.status().equals("FAIL")).map(ReplicationService.Check::message).toList()));
        Backup b = Backup.findById(p.backupId());
        DatabaseNode exec = DatabaseNode.findById(p.executorId());
        Map<String, Object> backup = Map.of("backupId", b.id.toString(), "relativeDir", b.relativeDir, "sha256", b.sha256);
        List<Map<String, Object>> files = p.binlogs().stream().map(r -> Map.<String, Object>of("relativePath", r.relativePath(), "sha256", r.sha256() == null ? "" : r.sha256())).toList();
        UUID rid = UUID.randomUUID();
        Map<String, Object> params = new LinkedHashMap<>();
        params.put("backup", backup);
        params.put("startGtid", b.gtid);
        params.put("binlogs", files);
        params.put("stopAt", stopAt.toString());
        params.put("extract", tables);
        params.put("extractDir", "tenants/" + t.tenantId + "/sites/" + t.siteId + "/recoveries/" + rid);
        Operation op = ops.request(OperationType.PITR_RESTORE, exec, params, "Restauration isolée de " + t.name + " au "
                + stopAt.toString().substring(0, 19).replace('T', ' ') + " UTC — " + reason.trim(), actor);
        return op;
    }
}
