package com.prestige.replica.service;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.node.ObjectNode;
import com.prestige.replica.alert.AgentReport;
import com.prestige.replica.domain.*;
import jakarta.enterprise.context.ApplicationScoped;
import jakarta.inject.Inject;
import jakarta.transaction.Transactional;
import jakarta.ws.rs.BadRequestException;
import java.time.Duration;
import java.time.Instant;
import java.util.*;

/**
 * Lot 3 : pause/reprise, réparation guidée et bascule planifiée.
 *
 * <p>La bascule est une opération parente pilotée par la console. Chaque étape
 * est une opération ordinaire exécutée par l'agent concerné, déclenchée
 * seulement quand la précédente a réussi :
 * <ol>
 *   <li>rétrogradation de l'ancien primaire (read_only, coupure des connexions,
 *       constat de l'arrêt des écritures, position GTID finale) ;</li>
 *   <li>promotion de la cible après MASTER_GTID_WAIT sur cette position : aucune
 *       transaction confirmée n'est perdue ; si la position n'est pas atteinte,
 *       rien n'est modifié et l'ancien primaire est rouvert en écriture ;</li>
 *   <li>rattachement des autres répliques (et, sur demande, de l'ancien
 *       primaire) à la nouvelle source.</li>
 * </ol>
 */
@ApplicationScoped
public class ReplicationService {
    public static final int MAX_LAG_FOR_SWITCHOVER = 10;
    static final Duration REPORT_FRESHNESS = Duration.ofSeconds(120);

    @Inject OperationService ops;
    @Inject MonitoringService monitoring;
    @Inject AuditService audit;

    public record Check(String id, String status, String message) {}

    public record SwitchoverPlan(boolean ready, List<Check> checks, String oldPrimary, String target, String newSource,
                                 List<String> repointed) {}

    public record RepairAction(String id, String label, String detail, boolean recommended) {}

    public record RepairPlan(String diagnosis, String detail, List<RepairAction> actions, List<UUID> eligibleBackups) {}

    // ---- Pause / reprise -----------------------------------------------------

    @Transactional(Transactional.TxType.MANDATORY)
    public Operation pause(DatabaseNode node, String mode, String reason, String actor) {
        requireReplica(node);
        if (reason == null || reason.isBlank()) throw new BadRequestException("Indiquez le motif de la suspension (il sera rappelé dans les alertes)");
        String m = mode == null || mode.isBlank() ? "ALL" : mode;
        if (!m.equals("ALL") && !m.equals("SQL")) throw new BadRequestException("Mode ALL ou SQL");
        return ops.request(OperationType.REPLICATION_PAUSE, node, Map.of("mode", m, "reason", reason.trim()),
                (m.equals("SQL") ? "Suspendre l'application des transactions sur " : "Suspendre la réplication de ") + node.name + " — " + reason.trim(), actor);
    }

    @Transactional(Transactional.TxType.MANDATORY)
    public Operation resume(DatabaseNode node, String actor) {
        requireReplica(node);
        return ops.request(OperationType.REPLICATION_RESUME, node, Map.of(), "Reprendre la réplication de " + node.name, actor);
    }

    @Transactional(Transactional.TxType.MANDATORY)
    public Operation attach(DatabaseNode node, DatabaseNode primary, String[] hostPort, String actor) {
        if (node.id.equals(primary.id)) throw new BadRequestException("Un primaire ne se rattache pas à lui-même");
        Map<String, Object> p = new LinkedHashMap<>();
        p.put("sourceHost", hostPort[0]);
        p.put("sourcePort", Integer.parseInt(hostPort[1]));
        return ops.request(OperationType.REPLICA_ATTACH, node, p, "Rattacher " + node.name + " à " + primary.name + " (" + primary.addressLabel + ")", actor);
    }

    private static void requireReplica(DatabaseNode node) {
        if (node.expectedRole != NodeRole.REPLICA && node.expectedRole != NodeRole.DELAYED_REPLICA) {
            throw new BadRequestException("Seule une réplique peut être suspendue ou reprise");
        }
    }

    // ---- Réparation guidée ---------------------------------------------------

    /** Diagnostic d'une réplique et actions proposées ; jamais de saut d'erreur silencieux. */
    @Transactional(Transactional.TxType.MANDATORY)
    public RepairPlan repairPlan(DatabaseNode node, Instant now) {
        requireReplica(node);
        AgentReport r = monitoring.parse(node.lastReport);
        List<RepairAction> actions = new ArrayList<>();
        List<UUID> backups = eligibleBackups(node, now);
        RepairAction rebuild = new RepairAction("REBUILD", "Reconstruire depuis une sauvegarde vérifiée",
                backups.isEmpty() ? "Aucune sauvegarde vérifiée encore couverte par les binlogs du primaire : sauvegardez puis testez la restauration d'abord."
                        : "Remplace les données de la réplique par la dernière sauvegarde vérifiée puis rattrape le primaire. Double approbation.", false);
        if (r == null || !r.reachable()) {
            return new RepairPlan("Réplique injoignable", "L'agent ne joint pas MariaDB ou n'envoie plus de rapport : vérifiez le service MariaDB et l'agent sur la machine.",
                    List.of(), backups);
        }
        if (r.channelsOrEmpty().isEmpty()) {
            return new RepairPlan("Aucun canal de réplication", "Ce serveur n'a pas de configuration de réplication.",
                    List.of(new RepairAction(rebuild.id(), rebuild.label(), rebuild.detail(), true)), backups);
        }
        AgentReport.Channel c = r.channelsOrEmpty().get(0);
        if (c.lastIoErrno() == 1236) {
            return new RepairPlan("Journaux binaires manquants sur la source",
                    "Le primaire a purgé des binlogs dont la réplique a besoin (erreur 1236) : seule une reconstruction répare. " + nz(c.lastIoError()),
                    List.of(new RepairAction(rebuild.id(), rebuild.label(), rebuild.detail(), true)), backups);
        }
        if (c.lastSqlErrno() != 0) {
            actions.add(new RepairAction(rebuild.id(), rebuild.label(), rebuild.detail(), true));
            actions.add(new RepairAction("RESUME", "Reprendre après correction manuelle",
                    "Uniquement si la cause a été corrigée à la main sur la réplique (et tracée). Ignorer l'erreur (sql_slave_skip_counter) n'est pas proposé : la réplique deviendrait divergente sans que personne le sache.", false));
            return new RepairPlan("Erreur SQL " + c.lastSqlErrno() + " : la réplique a divergé",
                    nz(c.lastSqlError()), actions, backups);
        }
        if (c.lastIoErrno() != 0) {
            actions.add(new RepairAction("WAIT", "Laisser MariaDB réessayer",
                    "Le thread IO se reconnecte seul toutes les quelques secondes. Vérifiez le réseau, le pare-feu, l'adresse du primaire et le compte de réplication.", true));
            actions.add(new RepairAction("RESUME", "Relancer la réplication", "Utile si le thread IO a été arrêté après trop d'échecs.", false));
            return new RepairPlan("Erreur de connexion " + c.lastIoErrno() + " vers la source", nz(c.lastIoError()), actions, backups);
        }
        if (!c.ioUp() || !c.sqlUp()) {
            String who = node.replicationPausedAt != null ? "Suspendue par " + node.replicationPausedBy + " : " + nz(node.replicationPauseReason) : "Arrêtée sans erreur (arrêt manuel ?)";
            actions.add(new RepairAction("RESUME", "Reprendre la réplication", "START SLAVE puis vérification que les deux threads tournent.", true));
            return new RepairPlan("Réplication à l'arrêt", who, actions, backups);
        }
        return new RepairPlan("Réplication saine", "Les deux threads tournent sans erreur ; retard " + (c.secondsBehindMaster() == null ? "inconnu" : c.secondsBehindMaster() + " s") + ".",
                List.of(), backups);
    }

    /**
     * Sauvegardes utilisables pour reconstruire : vérifiées, avec GTID, et
     * encore couvertes par la rétention des binlogs du primaire.
     */
    public List<UUID> eligibleBackups(DatabaseNode node, Instant now) {
        if (node.topologyId == null) return List.of();
        long retention = primaryBinlogRetention(node.topologyId);
        List<UUID> out = new ArrayList<>();
        for (Backup b : Backup.<Backup>list("topologyId = ?1 and status = ?2 and verifyStatus = ?3 and gtid is not null ORDER BY startedAt DESC",
                node.topologyId, Backup.Status.COMPLETED, Backup.Verify.VERIFIED)) {
            if (retention <= 0 || Duration.between(b.startedAt, now).getSeconds() < retention - 3600) out.add(b.id);
        }
        return out;
    }

    /** Rétention des binlogs du primaire en secondes (0 = inconnue ou illimitée). */
    public long primaryBinlogRetention(UUID topologyId) {
        DatabaseNode p = DatabaseNode.find("topologyId = ?1 and expectedRole = ?2", topologyId, NodeRole.PRIMARY).firstResult();
        AgentReport r = p == null ? null : monitoring.parse(p.lastReport);
        return r == null || r.server() == null ? 0 : r.server().binlogExpireSeconds();
    }

    // ---- Bascule planifiée ---------------------------------------------------

    @Transactional(Transactional.TxType.MANDATORY)
    public SwitchoverPlan plan(Topology t, DatabaseNode target, Instant now) {
        return plan(t, target, now, null);
    }

    /** {@code exclude} : opération parente déjà créée, ignorée dans le décompte des opérations actives. */
    @Transactional(Transactional.TxType.MANDATORY)
    public SwitchoverPlan plan(Topology t, DatabaseNode target, Instant now, UUID exclude) {
        List<Check> checks = new ArrayList<>();
        List<DatabaseNode> nodes = DatabaseNode.list("topologyId", t.id);
        List<DatabaseNode> primaries = nodes.stream().filter(n -> n.expectedRole == NodeRole.PRIMARY).toList();
        DatabaseNode old = primaries.size() == 1 ? primaries.get(0) : null;
        if (old == null) checks.add(fail("primary", primaries.isEmpty() ? "Aucun primaire déclaré dans la topologie" : "Plusieurs primaires déclarés"));
        if (target == null || !t.id.equals(target.topologyId)) {
            checks.add(fail("target", "La cible n'appartient pas à cette topologie"));
            return new SwitchoverPlan(false, checks, old == null ? null : old.name, null, null, List.of());
        }
        if (target.expectedRole == NodeRole.DELAYED_REPLICA) checks.add(fail("target-role", "Une réplique différée est en retard par construction : elle ne peut pas devenir primaire"));
        else if (target.expectedRole != NodeRole.REPLICA) checks.add(fail("target-role", "La cible doit être une réplique"));
        String[] newSource = null;
        try {
            newSource = BackupService.hostPort(target.addressLabel);
            checks.add(ok("target-address", "Nouvelle adresse de la base : " + target.addressLabel));
        } catch (BadRequestException e) {
            checks.add(fail("target-address", "Renseignez l'adresse hôte:port de " + target.name + " (champ « Adresse ») : elle sert au rattachement des répliques"));
        }

        AgentReport rt = fresh(target, now, checks);
        AgentReport ro = old == null ? null : fresh(old, now, checks);
        for (DatabaseNode n : old == null ? List.of(target) : List.of(old, target)) {
            AgentReport r = monitoring.parse(n.lastReport);
            if (r != null && !r.capabilitiesOrEmpty().contains("replicationControl")) {
                checks.add(fail("capability-" + n.name, n.name + " : pilotage non autorisé localement (replicationControl.enabled dans agent.json)"));
            }
        }
        if (rt != null && rt.server() != null) {
            AgentReport.Server s = rt.server();
            if (!s.logBin()) checks.add(fail("target-binlog", target.name + " : journal binaire désactivé, les autres serveurs ne pourraient pas répliquer depuis lui"));
            else checks.add(ok("target-binlog", target.name + " : journal binaire actif"));
            if (!s.logSlaveUpdates()) checks.add(warn("target-log-slave-updates", "log_slave_updates=OFF sur " + target.name + " : recommandé ON"));
            if (!s.gtidStrictMode()) checks.add(warn("target-strict", "gtid_strict_mode=OFF sur " + target.name + " : recommandé ON pour détecter toute divergence"));
            if (ro != null && ro.server() != null) {
                if (ro.server().serverId() == s.serverId()) checks.add(fail("server-id", "Même server_id (" + s.serverId() + ") sur les deux serveurs"));
                if (majorMinor(s.version()).compareTo(majorMinor(ro.server().version())) < 0) {
                    checks.add(warn("version", "Version de " + target.name + " (" + s.version() + ") plus ancienne que celle du primaire (" + ro.server().version() + ")"));
                }
            }
            AgentReport.Channel c = rt.channelsOrEmpty().isEmpty() ? null : rt.channelsOrEmpty().get(0);
            if (c == null) {
                checks.add(fail("target-channel", target.name + " ne réplique pas"));
            } else {
                if (!c.ioUp() || !c.sqlUp() || c.lastIoErrno() != 0 || c.lastSqlErrno() != 0) {
                    checks.add(fail("target-channel", "Réplication de " + target.name + " non saine (IO=" + c.ioRunning() + ", SQL=" + c.sqlRunning() + ")"));
                } else if (c.secondsBehindMaster() == null || c.secondsBehindMaster() > MAX_LAG_FOR_SWITCHOVER) {
                    checks.add(fail("target-lag", "Retard de " + target.name + " : " + c.secondsBehindMaster() + " s (maximum " + MAX_LAG_FOR_SWITCHOVER + " s)"));
                } else {
                    checks.add(ok("target-channel", "Réplication saine, retard " + c.secondsBehindMaster() + " s"));
                }
                if (c.sqlDelay() > 0) checks.add(fail("target-delay", target.name + " a un retard volontaire de " + c.sqlDelay() + " s"));
                if ("No".equalsIgnoreCase(c.usingGtid())) checks.add(fail("target-gtid", target.name + " ne réplique pas en GTID"));
                if (old != null && old.addressLabel != null && !old.addressLabel.equalsIgnoreCase(c.masterHost() + ":" + c.masterPort())) {
                    checks.add(warn("target-source", target.name + " réplique depuis " + c.masterHost() + ":" + c.masterPort()
                            + " alors que le primaire est déclaré à " + old.addressLabel + " : vérifiez qu'il s'agit bien du même serveur"));
                }
            }
        }
        if (ro != null && ro.server() != null && ro.server().readOnly()) checks.add(warn("old-read-only", "Le primaire est déjà en lecture seule"));
        if (t.backupEnabled || t.backupNodeId != null) {
            Backup v = Backup.find("topologyId = ?1 and verifyStatus = ?2 ORDER BY verifiedAt DESC", t.id, Backup.Verify.VERIFIED).firstResult();
            if (v == null || Duration.between(v.verifiedAt, now).toDays() >= 8) checks.add(warn("backup", "Aucune sauvegarde vérifiée depuis 8 jours"));
            else checks.add(ok("backup", "Sauvegarde vérifiée du " + v.startedAt.toString().substring(0, 16).replace('T', ' ') + " UTC disponible"));
        }
        List<UUID> ids = nodes.stream().map(n -> n.id).toList();
        long active = Operation.count("nodeId in ?1 and status in ?2 and id <> ?3", ids,
                List.of(OperationStatus.AWAITING_APPROVAL, OperationStatus.QUEUED, OperationStatus.RUNNING),
                exclude == null ? new UUID(0, 0) : exclude);
        if (active > 0) checks.add(fail("busy", active + " opération(s) en attente ou en cours dans la topologie : terminez-les d'abord"));

        List<String> repointed = new ArrayList<>();
        for (DatabaseNode n : nodes) {
            if (n.id.equals(target.id) || (old != null && n.id.equals(old.id)) || n.expectedRole == NodeRole.STANDALONE) continue;
            AgentReport r = monitoring.parse(n.lastReport);
            if (r == null || !r.capabilitiesOrEmpty().contains("replicationControl")) {
                checks.add(fail("capability-" + n.name, n.name + " devra être rattachée à la nouvelle source mais son agent ne l'autorise pas (replicationControl)"));
            }
            repointed.add(n.name);
        }
        if (!repointed.isEmpty()) checks.add(ok("repoint", "Répliques rattachées à " + target.name + " : " + String.join(", ", repointed)));
        boolean ready = checks.stream().noneMatch(c -> c.status().equals("FAIL"));
        return new SwitchoverPlan(ready, checks, old == null ? null : old.name, target.name,
                newSource == null ? null : target.addressLabel, repointed);
    }

    private AgentReport fresh(DatabaseNode n, Instant now, List<Check> checks) {
        AgentReport r = monitoring.parse(n.lastReport);
        if (r == null || n.lastReportAt == null || Duration.between(n.lastReportAt, now).compareTo(REPORT_FRESHNESS) > 0) {
            checks.add(fail("fresh-" + n.name, n.name + " : aucun rapport récent de l'agent (état inconnu)"));
            return null;
        }
        if (!r.reachable()) {
            checks.add(fail("reachable-" + n.name, n.name + " : MariaDB injoignable"));
            return null;
        }
        return r;
    }

    @Transactional(Transactional.TxType.MANDATORY)
    public Operation requestSwitchover(Topology t, DatabaseNode target, boolean reattachOldPrimary, String topologyConfirmation, String actor) {
        if (topologyConfirmation == null || !t.name.equals(topologyConfirmation.trim())) {
            throw new BadRequestException("Saisissez exactement le nom de la topologie pour confirmer");
        }
        Instant now = Instant.now();
        SwitchoverPlan p = plan(t, target, now);
        if (!p.ready()) throw new BadRequestException("Contrôles préalables non satisfaits : " + failures(p));
        DatabaseNode old = DatabaseNode.find("topologyId = ?1 and expectedRole = ?2", t.id, NodeRole.PRIMARY).firstResult();
        Map<String, Object> params = new LinkedHashMap<>();
        params.put("oldPrimaryId", old.id.toString());
        params.put("targetId", target.id.toString());
        params.put("reattachOldPrimary", reattachOldPrimary);
        params.put("oldAddress", old.addressLabel);
        params.put("newAddress", target.addressLabel);
        Operation op = ops.request(OperationType.SWITCHOVER, target, params,
                "Bascule : " + old.name + " → " + target.name + " (" + t.name + ")" + (reattachOldPrimary ? ", ancien primaire rattaché" : ", ancien primaire isolé"), actor);
        return op;
    }

    static String failures(SwitchoverPlan p) {
        return String.join(" ; ", p.checks().stream().filter(c -> c.status().equals("FAIL")).map(Check::message).toList());
    }

    /** Appelée à l'approbation : nouveaux contrôles sur l'état le plus récent, puis étape 1. */
    @Transactional(Transactional.TxType.MANDATORY)
    public void start(Operation parent) {
        JsonNode p = ops.read(parent.params);
        Topology t = Topology.findById(parent.topologyId);
        DatabaseNode target = DatabaseNode.findById(UUID.fromString(p.path("targetId").asText()));
        DatabaseNode old = DatabaseNode.findById(UUID.fromString(p.path("oldPrimaryId").asText()));
        parent.status = OperationStatus.RUNNING;
        parent.startedAt = Instant.now();
        parent.lastEventAt = parent.startedAt;
        // L'opération parente elle-même ne doit pas bloquer les contrôles.
        SwitchoverPlan plan = plan(t, target, Instant.now(), parent.id);
        if (!plan.ready() || old == null || old.expectedRole != NodeRole.PRIMARY) {
            finish(parent, OperationStatus.FAILED, "Contrôles au moment de l'exécution non satisfaits, rien n'a été modifié : " + failures(plan), t);
            return;
        }
        ObjectNode pm = (ObjectNode) p;
        pm.put("maintenanceBefore", t.maintenance);
        parent.params = ops.write(pm);
        t.maintenance = true; // aucune alerte parasite pendant les quelques secondes de bascule
        ops.event(parent, "INFO", "Contrôles validés ; topologie en maintenance pendant la bascule");
        ops.event(parent, "INFO", "Étape 1 : rétrogradation de " + old.name);
        ops.requestChild(parent, OperationType.SWITCHOVER_DEMOTE, old, Map.of("maxWaitSeconds", 30),
                "Mettre " + old.name + " en lecture seule et constater l'arrêt des écritures");
    }

    /** Enchaînement des étapes quand une étape se termine. */
    @Transactional(Transactional.TxType.MANDATORY)
    public void onChildFinished(Operation child) {
        Operation parent = Operation.findById(child.parentId);
        if (parent == null || parent.status.terminal()) return;
        parent.lastEventAt = Instant.now();
        ObjectNode p = (ObjectNode) ops.read(parent.params);
        Topology t = Topology.findById(parent.topologyId);
        DatabaseNode target = DatabaseNode.findById(UUID.fromString(p.path("targetId").asText()));
        DatabaseNode old = DatabaseNode.findById(UUID.fromString(p.path("oldPrimaryId").asText()));
        JsonNode result = ops.read(child.result);
        boolean ok = child.status == OperationStatus.SUCCEEDED;
        switch (child.type) {
            case SWITCHOVER_DEMOTE -> {
                if (!ok) {
                    finish(parent, child.status == OperationStatus.INTERVENTION_REQUIRED ? OperationStatus.INTERVENTION_REQUIRED : OperationStatus.FAILED,
                            "Rétrogradation impossible : " + child.error + (child.status == OperationStatus.FAILED ? " — " + old.name + " est resté primaire, aucune donnée perdue" : ""), t);
                    return;
                }
                String gtid = result.path("gtid").asText("");
                p.put("finalGtid", gtid);
                p.put("writesStoppedAt", child.finishedAt.toString());
                parent.params = ops.write(p);
                ops.event(parent, "INFO", old.name + " en lecture seule ; dernière transaction : " + gtid
                        + " (" + result.path("killedConnections").asInt() + " connexion(s) coupée(s))");
                ops.event(parent, "INFO", "Étape 2 : promotion de " + target.name + " après application de " + gtid);
                ops.requestChild(parent, OperationType.SWITCHOVER_PROMOTE, target, Map.of("gtid", gtid, "timeoutSeconds", 120),
                        "Attendre " + gtid + " puis promouvoir " + target.name);
            }
            case SWITCHOVER_PROMOTE -> {
                if (!ok) {
                    if (!result.path("promoted").asBoolean(false) && !result.path("partial").asBoolean(false)) {
                        p.put("promoteError", String.valueOf(child.error));
                        parent.params = ops.write(p);
                        ops.event(parent, "WARN", "Promotion non effectuée (" + child.error + ") : retour arrière, " + old.name + " va être rouvert en écriture");
                        ops.requestChild(parent, OperationType.SWITCHOVER_UNDO, old, Map.of(), "Rouvrir " + old.name + " en écriture");
                    } else {
                        finish(parent, OperationStatus.INTERVENTION_REQUIRED, "Promotion interrompue en cours de route sur " + target.name + " : " + child.error
                                + ". " + old.name + " est en lecture seule ; vérifiez l'état de " + target.name + " avant toute action.", t);
                    }
                    return;
                }
                // Inventaire : rôles échangés dès que la nouvelle source accepte les écritures.
                old.expectedRole = NodeRole.REPLICA;
                target.expectedRole = NodeRole.PRIMARY;
                if (t.backupNodeId != null && t.backupNodeId.equals(target.id)) t.backupNodeId = old.id;
                Instant stopped = Instant.parse(p.path("writesStoppedAt").asText(child.finishedAt.toString()));
                Operation demote = Operation.find("parentId = ?1 and type = ?2", parent.id, OperationType.SWITCHOVER_DEMOTE).firstResult();
                Instant from = demote != null && demote.startedAt != null ? demote.startedAt : stopped;
                long unavailableMs = Duration.between(from, child.finishedAt).toMillis();
                p.put("newPrimaryGtid", result.path("gtid").asText(""));
                p.put("writeUnavailableMs", unavailableMs);
                parent.params = ops.write(p);
                ops.event(parent, "INFO", target.name + " est le nouveau primaire (position " + result.path("gtid").asText("") + ") ; écritures interrompues "
                        + String.format(Locale.FRANCE, "%.1f", unavailableMs / 1000.0) + " s");
                ops.event(parent, "WARN", "Action manuelle : faire pointer Prestige vers " + target.addressLabel + " (au lieu de " + old.addressLabel + ")");
                String[] hp = BackupService.hostPort(target.addressLabel);
                int attached = 0;
                for (DatabaseNode n : DatabaseNode.<DatabaseNode>list("topologyId", t.id)) {
                    if (n.id.equals(target.id) || n.expectedRole == NodeRole.STANDALONE) continue;
                    if (n.id.equals(old.id) && !p.path("reattachOldPrimary").asBoolean(false)) continue;
                    Map<String, Object> ap = new LinkedHashMap<>();
                    ap.put("sourceHost", hp[0]);
                    ap.put("sourcePort", Integer.parseInt(hp[1]));
                    ops.requestChild(parent, OperationType.REPLICA_ATTACH, n, ap,
                            (n.id.equals(old.id) ? "Rattacher l'ancien primaire " : "Rattacher ") + n.name + " à " + target.name);
                    attached++;
                }
                if (attached == 0) {
                    finish(parent, OperationStatus.SUCCEEDED, null, t);
                } else {
                    ops.event(parent, "INFO", "Étape 3 : rattachement de " + attached + " serveur(s) à " + target.name);
                }
                if (!p.path("reattachOldPrimary").asBoolean(false)) {
                    ops.event(parent, "INFO", old.name + " reste isolé en lecture seule jusqu'à validation (bouton « Rattacher » sur sa fiche)");
                }
            }
            case SWITCHOVER_UNDO -> finish(parent, ok ? OperationStatus.FAILED : OperationStatus.INTERVENTION_REQUIRED,
                    ok ? "Bascule annulée sans perte : " + old.name + " accepte de nouveau les écritures. Cause : " + p.path("promoteError").asText("")
                       : "Retour arrière impossible : " + child.error + ". " + old.name + " est en lecture seule : intervention immédiate requise.", t);
            case REPLICA_ATTACH -> {
                if (Operation.count("parentId = ?1 and type = ?2 and status in ?3", parent.id, OperationType.REPLICA_ATTACH,
                        List.of(OperationStatus.QUEUED, OperationStatus.RUNNING)) > 0) return;
                long failed = Operation.count("parentId = ?1 and type = ?2 and status <> ?3", parent.id, OperationType.REPLICA_ATTACH, OperationStatus.SUCCEEDED);
                if (failed == 0) finish(parent, OperationStatus.SUCCEEDED, null, t);
                else finish(parent, OperationStatus.INTERVENTION_REQUIRED, target.name + " est primaire, mais " + failed
                        + " rattachement(s) ont échoué : voir les étapes", t);
            }
            default -> { }
        }
    }

    private void finish(Operation parent, OperationStatus st, String error, Topology t) {
        JsonNode p = ops.read(parent.params);
        parent.status = st;
        parent.finishedAt = Instant.now();
        parent.lastEventAt = parent.finishedAt;
        parent.error = error;
        if (t != null && p.has("maintenanceBefore")) t.maintenance = p.path("maintenanceBefore").asBoolean(false);
        Map<String, Object> res = new LinkedHashMap<>();
        res.put("finalGtid", p.path("finalGtid").asText(null));
        res.put("newPrimaryGtid", p.path("newPrimaryGtid").asText(null));
        if (p.has("writeUnavailableMs")) res.put("writeUnavailableMs", p.path("writeUnavailableMs").asLong());
        res.put("totalSeconds", parent.startedAt == null ? null : Duration.between(parent.startedAt, parent.finishedAt).toSeconds());
        res.put("newPrimary", st == OperationStatus.SUCCEEDED || p.has("newPrimaryGtid") ? p.path("newAddress").asText() : p.path("oldAddress").asText());
        parent.result = ops.write(res);
        ops.event(parent, st == OperationStatus.SUCCEEDED ? "INFO" : "ERROR",
                st == OperationStatus.SUCCEEDED ? "Bascule terminée avec succès" : error);
        audit.record("system", "SWITCHOVER_" + st, "operation:" + parent.id, st == OperationStatus.SUCCEEDED ? "SUCCESS" : "FAILURE",
                parent.tenantId, parent.siteId, parent.summary + (error == null ? "" : " — " + error));
    }

    private static Check ok(String id, String m) { return new Check(id, "OK", m); }
    private static Check warn(String id, String m) { return new Check(id, "WARN", m); }
    private static Check fail(String id, String m) { return new Check(id, "FAIL", m); }
    /** « 10.6.18-MariaDB-log » → « 010.006 » (comparable). */
    static String majorMinor(String v) {
        if (v == null) return "";
        String[] parts = v.split("[.-]");
        try {
            return String.format("%03d.%03d", Integer.parseInt(parts[0]), Integer.parseInt(parts[1]));
        } catch (RuntimeException e) {
            return "";
        }
    }

    private static String nz(String s) { return s == null ? "" : s; }
}
