package com.prestige.replica.service;

import com.prestige.replica.alert.AgentReport;
import com.prestige.replica.alert.Finding;
import com.prestige.replica.domain.*;
import jakarta.enterprise.context.ApplicationScoped;
import jakarta.inject.Inject;
import jakarta.transaction.Transactional;
import jakarta.ws.rs.BadRequestException;
import jakarta.ws.rs.ClientErrorException;
import java.time.*;
import java.util.*;

/** Demandes d'opérations Lot 2, planification et politique de rétention. */
@ApplicationScoped
public class BackupService {
    @Inject OperationService ops;
    @Inject MonitoringService monitoring;

    @Transactional(Transactional.TxType.MANDATORY)
    public Operation requestPreflight(DatabaseNode node, String actor) {
        Map<String, Object> p = new LinkedHashMap<>();
        p.put("role", node.expectedRole.name());
        p.put("suggestedServerId", suggestServerId(node));
        DatabaseNode primary = primaryOf(node.topologyId);
        if (primary != null && !primary.id.equals(node.id)) {
            AgentReport r = monitoring.parse(primary.lastReport);
            if (r != null && r.server() != null) p.put("sourceVersion", r.server().version());
        }
        return ops.request(OperationType.PREFLIGHT, node, p, "Prérequis de " + node.name, actor);
    }

    @Transactional(Transactional.TxType.MANDATORY)
    public Operation requestBackup(DatabaseNode node, String actor) {
        Backup b = new Backup();
        b.tenantId = node.tenantId;
        b.siteId = node.siteId;
        b.topologyId = node.topologyId;
        b.nodeId = node.id;
        b.relativeDir = OperationService.backupDir(node.tenantId, node.siteId, b.id);
        b.status = Backup.Status.RUNNING;
        b.startedAt = Instant.now();
        Map<String, Object> p = Map.of("backupId", b.id.toString(), "relativeDir", b.relativeDir);
        Operation op = ops.request(OperationType.BACKUP_FULL, node, p, "Sauvegarde complète de " + node.name, actor);
        b.operationId = op.id;
        b.persist();
        return op;
    }

    @Transactional(Transactional.TxType.MANDATORY)
    public Operation requestRestoreTest(Backup b, DatabaseNode executor, String actor) {
        if (b.status != Backup.Status.COMPLETED) throw new ClientErrorException("Seule une sauvegarde terminée peut être restaurée", 409);
        if (!executor.siteId.equals(b.siteId)) throw new BadRequestException("Le nœud de restauration doit appartenir au même site");
        Map<String, Object> p = Map.of("backupId", b.id.toString(), "relativeDir", b.relativeDir, "sha256", b.sha256);
        return ops.request(OperationType.RESTORE_TEST, executor, p, "Restauration de contrôle de la sauvegarde du "
                + b.startedAt.toString().substring(0, 16).replace('T', ' ') + " UTC", actor);
    }

    /**
     * Construction d'une réplique : la sauvegarde doit être vérifiée par une
     * restauration réussie et porter une position GTID ; la cible doit être une
     * réplique de la même topologie que la source.
     */
    @Transactional(Transactional.TxType.MANDATORY)
    public Operation requestProvision(Topology t, DatabaseNode target, Backup b, String confirmDataDir, int delaySeconds,
                                      String topologyConfirmation, String actor) {
        if (topologyConfirmation == null || !t.name.equals(topologyConfirmation.trim())) {
            throw new BadRequestException("Saisissez exactement le nom de la topologie pour confirmer");
        }
        if (!t.id.equals(target.topologyId)) throw new BadRequestException("La cible n'appartient pas à cette topologie");
        if (target.expectedRole != NodeRole.REPLICA && target.expectedRole != NodeRole.DELAYED_REPLICA) {
            throw new BadRequestException("Seul un nœud déclaré réplique peut être reconstruit");
        }
        DatabaseNode primary = primaryOf(t.id);
        if (primary == null) throw new BadRequestException("Aucun primaire déclaré dans la topologie");
        if (!t.id.equals(b.topologyId) || b.status != Backup.Status.COMPLETED) throw new BadRequestException("Sauvegarde inutilisable pour cette topologie");
        if (b.verifyStatus != Backup.Verify.VERIFIED) throw new BadRequestException("Sauvegarde non vérifiée : lancez d'abord une restauration de contrôle");
        if (b.gtid == null) throw new BadRequestException("Sauvegarde sans position GTID (journal binaire désactivé sur la source ?)");
        String[] hp = hostPort(primary.addressLabel);
        AgentReport prep = monitoring.parse(primary.lastReport);
        long retention = prep == null || prep.server() == null ? 0 : prep.server().binlogExpireSeconds();
        if (retention > 0 && Duration.between(b.startedAt, Instant.now()).getSeconds() > retention - 3600) {
            throw new BadRequestException("Sauvegarde trop ancienne : le primaire a probablement déjà purgé les binlogs nécessaires (rétention "
                    + retention / 3600 + " h). Faites une nouvelle sauvegarde vérifiée.");
        }
        if (delaySeconds < 0 || delaySeconds > 7 * 86400) throw new BadRequestException("Délai invalide");
        if (target.expectedRole == NodeRole.DELAYED_REPLICA && delaySeconds == 0) {
            throw new BadRequestException("Une réplique différée exige un délai (par exemple 21600 s = 6 h)");
        }
        AgentReport tr = monitoring.parse(target.lastReport);
        if (tr != null && tr.server() != null && tr.server().serverId() != 0) {
            AgentReport pr = monitoring.parse(primary.lastReport);
            if (pr != null && pr.server() != null && pr.server().serverId() == tr.server().serverId()) {
                throw new BadRequestException("La cible a le même server_id que le primaire : corrigez son my.ini d'abord");
            }
        }
        Map<String, Object> p = new LinkedHashMap<>();
        p.put("backupId", b.id.toString());
        p.put("relativeDir", b.relativeDir);
        p.put("sha256", b.sha256);
        p.put("sourceHost", hp[0]);
        p.put("sourcePort", Integer.parseInt(hp[1]));
        p.put("delaySeconds", delaySeconds);
        p.put("confirmDataDir", confirmDataDir);
        return ops.request(OperationType.PROVISION_REPLICA, target, p,
                "Reconstruire " + target.name + " depuis la sauvegarde du " + b.startedAt.toString().substring(0, 16).replace('T', ' ')
                        + " UTC, source " + primary.name + " (" + primary.addressLabel + ")", actor);
    }

    public static String[] hostPortOf(String label) { return hostPort(label); }

    static String[] hostPort(String label) {
        if (label == null || !label.matches("[A-Za-z0-9.\\-]+:\\d{1,5}")) {
            throw new BadRequestException("Adresse du primaire à renseigner au format hôte:port (champ « Adresse » du nœud)");
        }
        return label.split(":");
    }

    public DatabaseNode primaryOf(UUID topologyId) {
        if (topologyId == null) return null;
        return DatabaseNode.find("topologyId = ?1 and expectedRole = ?2", topologyId, NodeRole.PRIMARY).firstResult();
    }

    /** Propose un server_id libre : 1 pour le primaire, sinon le plus petit libre à partir de 2. */
    long suggestServerId(DatabaseNode node) {
        if (node.expectedRole == NodeRole.PRIMARY) return 1;
        Set<Long> used = new HashSet<>();
        if (node.topologyId != null) {
            for (DatabaseNode n : DatabaseNode.<DatabaseNode>list("topologyId", node.topologyId)) {
                if (n.id.equals(node.id)) continue;
                AgentReport r = monitoring.parse(n.lastReport);
                if (r != null && r.server() != null) used.add(r.server().serverId());
            }
        }
        long id = 2;
        while (used.contains(id)) id++;
        return id;
    }

    // ---- Planification ----------------------------------------------------

    /** Lance les sauvegardes, tests de restauration et purges dus. */
    @Transactional
    public void schedule(Instant now) {
        for (Topology t : Topology.<Topology>list("backupEnabled = true and maintenance = false")) {
            Site site = Site.findById(t.siteId);
            ZoneId zone = zone(site);
            ZonedDateTime local = now.atZone(zone);
            if (t.backupNodeId != null && local.getHour() == t.backupHour) {
                Instant dayStart = local.toLocalDate().atStartOfDay(zone).toInstant();
                if (Backup.count("topologyId = ?1 and startedAt >= ?2 and status <> ?3", t.id, dayStart, Backup.Status.FAILED) == 0
                        && Operation.count("topologyId = ?1 and type = ?2 and requestedAt >= ?3", t.id, OperationType.BACKUP_FULL, dayStart) == 0) {
                    DatabaseNode n = DatabaseNode.findById(t.backupNodeId);
                    if (n != null) requestBackup(n, OperationService.SYSTEM);
                }
            }
            // Restauration de contrôle hebdomadaire, deux heures après la sauvegarde.
            if (t.restoreNodeId != null && local.getDayOfWeek().getValue() == t.restoreTestWeekday && local.getHour() == (t.backupHour + 2) % 24) {
                Instant dayStart = local.toLocalDate().atStartOfDay(zone).toInstant();
                if (Operation.count("topologyId = ?1 and type = ?2 and requestedAt >= ?3", t.id, OperationType.RESTORE_TEST, dayStart) == 0) {
                    Backup latest = Backup.find("topologyId = ?1 and status = ?2 ORDER BY startedAt DESC", t.id, Backup.Status.COMPLETED).firstResult();
                    DatabaseNode n = DatabaseNode.findById(t.restoreNodeId);
                    if (latest != null && n != null) requestRestoreTest(latest, n, OperationService.SYSTEM);
                }
            }
            prune(t);
        }
    }

    /**
     * Rétention : garde les N dernières sauvegardes terminées et, en plus,
     * toujours la plus récente sauvegarde vérifiée.
     */
    @Transactional(Transactional.TxType.MANDATORY)
    public void prune(Topology t) {
        if (t.backupNodeId == null) return;
        List<Backup> done = Backup.list("topologyId = ?1 and status = ?2 ORDER BY startedAt DESC", t.id, Backup.Status.COMPLETED);
        if (done.size() <= t.backupKeep) return;
        Backup lastVerified = done.stream().filter(b -> b.verifyStatus == Backup.Verify.VERIFIED).findFirst().orElse(null);
        // Sauvegardes encore lues par une restauration ou une construction en attente ou en cours.
        Set<String> inUse = new HashSet<>();
        for (Operation o : Operation.<Operation>list("topologyId = ?1 and type in (?2) and status in (?3)", t.id,
                List.of(OperationType.RESTORE_TEST, OperationType.PROVISION_REPLICA),
                List.of(OperationStatus.QUEUED, OperationStatus.RUNNING, OperationStatus.AWAITING_APPROVAL))) {
            inUse.add(ops.read(o.params).path("backupId").asText());
        }
        List<String> expired = new ArrayList<>();
        for (int i = t.backupKeep; i < done.size(); i++) {
            Backup b = done.get(i);
            if (b == lastVerified) continue;
            // Une restauration ou construction en cours peut encore la lire.
            if (inUse.contains(b.id.toString())) continue;
            expired.add(b.relativeDir);
        }
        if (expired.isEmpty()) return;
        DatabaseNode n = DatabaseNode.findById(t.backupNodeId);
        if (n == null || Operation.count("nodeId = ?1 and type = ?2 and status in (?3)", n.id, OperationType.BACKUP_PRUNE,
                List.of(OperationStatus.QUEUED, OperationStatus.RUNNING)) > 0) return;
        ops.request(OperationType.BACKUP_PRUNE, n, Map.of("relativeDirs", expired), expired.size() + " sauvegarde(s) expirée(s)", OperationService.SYSTEM);
    }

    /** Constats de supervision des sauvegardes d'une topologie (§5 « Surveillance »). */
    public List<Finding> findings(Topology t, Instant now) {
        List<Finding> out = new ArrayList<>();
        // Une opération interrompue reste signalée, que la sauvegarde automatique soit active ou non.
        for (Operation op : Operation.<Operation>list("topologyId = ?1 and status = ?2 and finishedAt > ?3", t.id,
                OperationStatus.INTERVENTION_REQUIRED, now.minus(Duration.ofDays(7)))) {
            // Résolue d'elle-même si la même opération a réussi ensuite sur ce nœud.
            if (Operation.count("nodeId = ?1 and type = ?2 and status = ?3 and finishedAt > ?4", op.nodeId, op.type,
                    OperationStatus.SUCCEEDED, op.finishedAt) > 0) continue;
            out.add(new Finding("OPERATION_INTERVENTION", op.id.toString(), Severity.CRITICAL,
                    op.type.label + " : intervention requise — " + op.summary, op.error));
        }
        if (!t.backupEnabled) return out;
        Backup lastOk = Backup.find("topologyId = ?1 and status = ?2 ORDER BY startedAt DESC", t.id, Backup.Status.COMPLETED).firstResult();
        if (lastOk == null || lastOk.finishedAt == null || Duration.between(lastOk.finishedAt, now).toHours() >= t.backupMaxAgeHours) {
            out.add(new Finding("BACKUP_MISSING", "", Severity.CRITICAL, lastOk == null
                    ? "Aucune sauvegarde réussie pour cette topologie"
                    : "Dernière sauvegarde réussie il y a " + Duration.between(lastOk.finishedAt, now).toHours() + " h", null));
        }
        Backup last = Backup.find("topologyId = ?1 and status <> ?2 ORDER BY startedAt DESC", t.id, Backup.Status.RUNNING).firstResult();
        if (last != null && last.status == Backup.Status.FAILED) {
            Operation op = last.operationId == null ? null : Operation.findById(last.operationId);
            out.add(new Finding("BACKUP_FAILED", "", Severity.WARNING, "La dernière sauvegarde a échoué", op == null ? null : op.error));
        }
        RestoreTest lastTest = RestoreTest.find("backupId in (select b.id from Backup b where b.topologyId = ?1) ORDER BY at DESC", t.id).firstResult();
        if (lastTest != null && "FAILED".equals(lastTest.status)) {
            Operation op = lastTest.operationId == null ? null : Operation.findById(lastTest.operationId);
            out.add(new Finding("RESTORE_TEST_FAILED", "", Severity.CRITICAL, "La dernière restauration de contrôle a échoué : la sauvegarde n'est pas prouvée restaurable",
                    op == null ? null : op.error));
        }
        if (t.restoreNodeId != null) {
            Backup verified = Backup.find("topologyId = ?1 and verifyStatus = ?2 ORDER BY verifiedAt DESC", t.id, Backup.Verify.VERIFIED).firstResult();
            if (verified == null || Duration.between(verified.verifiedAt, now).toDays() >= 8) {
                out.add(new Finding("RESTORE_TEST_STALE", "", Severity.WARNING, "Aucune restauration vérifiée depuis plus de 8 jours", null));
            }
        }
        return out;
    }

    static ZoneId zone(Site s) {
        try {
            return s == null ? ZoneOffset.UTC : ZoneId.of(s.timezone);
        } catch (DateTimeException e) {
            return ZoneOffset.UTC;
        }
    }
}
