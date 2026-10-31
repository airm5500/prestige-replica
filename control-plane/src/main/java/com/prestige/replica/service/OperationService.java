package com.prestige.replica.service;

import com.fasterxml.jackson.core.JsonProcessingException;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.prestige.replica.domain.*;
import jakarta.enterprise.context.ApplicationScoped;
import jakarta.inject.Inject;
import jakarta.transaction.Transactional;
import jakarta.ws.rs.BadRequestException;
import jakarta.ws.rs.ClientErrorException;
import jakarta.ws.rs.ForbiddenException;
import java.time.Duration;
import java.time.Instant;
import java.util.*;
import org.jboss.logging.Logger;

/**
 * Cycle de vie des opérations : demande, approbation éventuelle, file
 * d'attente par nœud, exécution par l'agent, résultat et effets sur le
 * catalogue. Toute transition est auditée.
 */
@ApplicationScoped
public class OperationService {
    private static final Logger LOG = Logger.getLogger(OperationService.class);
    public static final String SYSTEM = "planificateur";

    @Inject ObjectMapper json;
    @Inject AuditService audit;
    @Inject ReplicationService replication;

    /** Types exécutés par un agent (les opérations parentes sont pilotées par la console). */
    static final List<OperationType> AGENT_TYPES = Arrays.stream(OperationType.values()).filter(t -> !t.consoleOnly).toList();

    /** Dossier relatif d'une sauvegarde, identique côté stockage (§19.3). */
    public static String backupDir(UUID tenant, UUID site, UUID backup) {
        return "tenants/" + tenant + "/sites/" + site + "/backups/" + backup;
    }

    /** Étape d'une opération parente : déjà couverte par l'approbation de la parente. */
    @Transactional(Transactional.TxType.MANDATORY)
    public Operation requestChild(Operation parent, OperationType type, DatabaseNode node, Map<String, Object> params, String summary) {
        Operation op = request(type, node, params, summary, parent.approvedBy == null ? SYSTEM : parent.approvedBy + " (bascule)");
        op.parentId = parent.id;
        return op;
    }

    @Transactional(Transactional.TxType.MANDATORY)
    public Operation request(OperationType type, DatabaseNode node, Map<String, Object> params, String summary, String actor) {
        if (Operation.count("nodeId = ?1 and type = ?2 and status in (?3)", node.id, type,
                List.of(OperationStatus.AWAITING_APPROVAL, OperationStatus.QUEUED, OperationStatus.RUNNING)) > 0) {
            throw new ClientErrorException("Une opération « " + type.label + " » est déjà en attente ou en cours sur ce nœud", 409);
        }
        Operation op = new Operation();
        op.tenantId = node.tenantId;
        op.siteId = node.siteId;
        op.topologyId = node.topologyId;
        op.nodeId = node.id;
        op.type = type;
        op.requiresApproval = type.requiresApproval;
        op.status = type.requiresApproval ? OperationStatus.AWAITING_APPROVAL : OperationStatus.QUEUED;
        op.params = write(params);
        op.summary = summary;
        op.requestedBy = actor;
        op.requestedAt = Instant.now();
        op.persist();
        event(op, "INFO", "Demandée par " + actor + (op.requiresApproval ? " — en attente d'approbation" : ""));
        audit.record(actor, "OPERATION_REQUEST", "operation:" + op.id, "SUCCESS", op.tenantId, op.siteId, type + " — " + summary);
        return op;
    }

    /** Approbation par une autre personne que le demandeur, avec ressaisie du code du site. */
    @Transactional
    public Operation approve(Operation op, String approver, boolean approverHasRole, String confirmation) {
        if (op.status != OperationStatus.AWAITING_APPROVAL) throw new ClientErrorException("Opération non approuvable dans l'état " + op.status, 409);
        if (!approverHasRole) throw new ForbiddenException("Rôle Approbateur ou Super-administrateur requis");
        if (approver.equals(op.requestedBy)) {
            audit.recordDenied(approver, "OPERATION_APPROVE", "operation:" + op.id, op.tenantId, op.siteId, "auto-approbation refusée");
            throw new ForbiddenException("Le demandeur ne peut pas approuver sa propre opération");
        }
        Site site = Site.findById(op.siteId);
        if (site == null || confirmation == null || !site.code.equalsIgnoreCase(confirmation.trim())) {
            audit.recordDenied(approver, "OPERATION_APPROVE", "operation:" + op.id, op.tenantId, op.siteId, "code de site incorrect");
            throw new BadRequestException("Code de confirmation incorrect : ressaisissez le code court de la pharmacie");
        }
        op.status = OperationStatus.QUEUED;
        op.approvedBy = approver;
        op.approvedAt = Instant.now();
        event(op, "INFO", "Approuvée par " + approver);
        audit.record(approver, "OPERATION_APPROVE", "operation:" + op.id, "SUCCESS", op.tenantId, op.siteId, op.type + " — " + op.summary);
        if (op.type.consoleOnly) replication.start(op);
        return op;
    }

    @Transactional
    public Operation cancel(Operation op, String actor) {
        if (op.parentId != null) throw new ClientErrorException("Étape d'une bascule : elle ne s'annule pas séparément", 409);
        if (op.status != OperationStatus.AWAITING_APPROVAL && op.status != OperationStatus.QUEUED) {
            throw new ClientErrorException("Seule une opération en attente peut être annulée (état " + op.status + ")", 409);
        }
        op.status = OperationStatus.CANCELLED;
        op.finishedAt = Instant.now();
        event(op, "INFO", "Annulée par " + actor);
        if (op.type == OperationType.BACKUP_FULL) {
            Backup.update("status = ?1, finishedAt = ?2 where operationId = ?3 and status = ?4", Backup.Status.FAILED, Instant.now(), op.id, Backup.Status.RUNNING);
        }
        audit.record(actor, "OPERATION_CANCEL", "operation:" + op.id, "SUCCESS", op.tenantId, op.siteId, op.type.name());
        return op;
    }

    // ---- Côté agent -----------------------------------------------------

    /** Opérations à exécuter : une seule à la fois par nœud. */
    @Transactional
    public List<Operation> pending(DatabaseNode node) {
        if (Operation.count("nodeId = ?1 and status = ?2 and type in ?3", node.id, OperationStatus.RUNNING, AGENT_TYPES) > 0) return List.of();
        return Operation.find("nodeId = ?1 and status = ?2 and type in ?3 ORDER BY requestedAt", node.id, OperationStatus.QUEUED, AGENT_TYPES)
                .page(0, 1).list();
    }

    @Transactional
    public boolean switchoverRunning(UUID topologyId) {
        return topologyId != null && Operation.count("topologyId = ?1 and type = ?2 and status = ?3", topologyId,
                OperationType.SWITCHOVER, OperationStatus.RUNNING) > 0;
    }

    @Transactional
    public Operation start(DatabaseNode node, UUID id) {
        Operation op = ownOp(node, id);
        if (op.status != OperationStatus.QUEUED) throw new ClientErrorException("Opération non disponible (" + op.status + ")", 409);
        op.status = OperationStatus.RUNNING;
        op.startedAt = Instant.now();
        op.lastEventAt = op.startedAt;
        event(op, "INFO", "Prise en charge par l'agent");
        return op;
    }

    @Transactional
    public void agentEvent(DatabaseNode node, UUID id, String level, String message) {
        Operation op = ownOp(node, id);
        if (op.status != OperationStatus.RUNNING) return;
        op.lastEventAt = Instant.now();
        event(op, "WARN".equals(level) ? "WARN" : "INFO", message);
    }

    @Transactional
    public void finish(DatabaseNode node, UUID id, String status, String error, JsonNode result) {
        Operation op = ownOp(node, id);
        if (op.status.terminal()) return; // renvoi idempotent
        if (op.status != OperationStatus.RUNNING) throw new ClientErrorException("Opération non démarrée", 409);
        OperationStatus st = switch (status == null ? "" : status) {
            case "SUCCEEDED" -> OperationStatus.SUCCEEDED;
            case "INTERVENTION_REQUIRED" -> OperationStatus.INTERVENTION_REQUIRED;
            default -> OperationStatus.FAILED;
        };
        op.status = st;
        op.finishedAt = Instant.now();
        op.lastEventAt = op.finishedAt;
        op.error = error == null || error.isBlank() ? null : truncate(error, 4000);
        op.result = result == null || result.isNull() ? null : result.toString();
        event(op, st == OperationStatus.SUCCEEDED ? "INFO" : "ERROR",
                st == OperationStatus.SUCCEEDED ? "Terminée avec succès" : "Échec : " + truncate(op.error, 1500));
        applyEffects(op, result);
        audit.record("agent:" + node.agentTokenPrefix, "OPERATION_FINISH", "operation:" + op.id,
                st == OperationStatus.SUCCEEDED ? "SUCCESS" : "FAILURE", op.tenantId, op.siteId, op.type + " " + st);
        if (op.parentId != null) replication.onChildFinished(op);
    }

    /** Opérations sans nouvelle de l'agent : jamais considérées réussies (§11). */
    @Transactional
    public int markStale(Duration silence) {
        Instant limit = Instant.now().minus(silence);
        List<Operation> stale = Operation.list("status = ?1 and lastEventAt < ?2 and type in ?3", OperationStatus.RUNNING, limit, AGENT_TYPES);
        for (Operation op : stale) {
            op.status = OperationStatus.INTERVENTION_REQUIRED;
            op.finishedAt = Instant.now();
            op.error = "Aucune nouvelle de l'agent depuis " + silence.toMinutes() + " min : état réel à vérifier sur le serveur";
            event(op, "ERROR", op.error);
            if (op.type == OperationType.BACKUP_FULL) {
                Backup.update("status = ?1, finishedAt = ?2 where operationId = ?3 and status = ?4", Backup.Status.FAILED, Instant.now(), op.id, Backup.Status.RUNNING);
            }
            audit.record("system", "OPERATION_STALE", "operation:" + op.id, "FAILURE", op.tenantId, op.siteId, op.type.name());
            if (op.parentId != null) replication.onChildFinished(op);
        }
        return stale.size();
    }

    private void applyEffects(Operation op, JsonNode result) {
        JsonNode params = read(op.params);
        switch (op.type) {
            case BACKUP_FULL -> {
                Backup b = Backup.findById(UUID.fromString(params.path("backupId").asText()));
                if (b == null) return;
                b.finishedAt = op.finishedAt;
                if (op.status == OperationStatus.SUCCEEDED && result != null && result.has("manifest")) {
                    JsonNode m = result.get("manifest");
                    b.status = Backup.Status.COMPLETED;
                    b.sizeBytes = m.path("sizeBytes").asLong();
                    b.sha256 = m.path("sha256").asText(null);
                    b.keyId = m.path("keyId").asText(null);
                    b.gtid = blankToNull(m.path("gtid").asText(""));
                    b.serverVersion = m.path("serverVersion").asText(null);
                } else {
                    b.status = Backup.Status.FAILED;
                }
            }
            case RESTORE_TEST -> {
                Backup b = Backup.findById(UUID.fromString(params.path("backupId").asText()));
                if (b == null) return;
                RestoreTest rt = new RestoreTest();
                rt.tenantId = op.tenantId;
                rt.backupId = b.id;
                rt.operationId = op.id;
                rt.nodeId = op.nodeId;
                rt.at = op.finishedAt;
                rt.status = op.status == OperationStatus.SUCCEEDED ? "SUCCEEDED" : "FAILED";
                if (result != null) {
                    rt.totalSeconds = result.path("timings").path("totalSeconds").isMissingNode() ? null : result.path("timings").path("totalSeconds").asInt();
                    rt.gtid = blankToNull(result.path("gtid").asText(""));
                    rt.details = result.toString();
                }
                rt.persist();
                b.verifyStatus = op.status == OperationStatus.SUCCEEDED ? Backup.Verify.VERIFIED : Backup.Verify.FAILED;
                b.verifiedAt = op.finishedAt;
                b.restoreSeconds = rt.totalSeconds;
            }
            case BACKUP_PRUNE -> {
                if (result != null) {
                    for (JsonNode d : result.path("deleted")) {
                        Backup.update("status = ?1 where relativeDir = ?2", Backup.Status.DELETED, d.asText());
                    }
                }
            }
            case REPLICATION_PAUSE -> {
                if (op.status != OperationStatus.SUCCEEDED) return;
                DatabaseNode n = DatabaseNode.findById(op.nodeId);
                n.replicationPausedAt = op.finishedAt;
                n.replicationPausedBy = op.requestedBy;
                n.replicationPauseReason = blankToNull(params.path("reason").asText(""));
            }
            case REPLICATION_RESUME, REPLICA_ATTACH -> {
                if (op.status != OperationStatus.SUCCEEDED) return;
                DatabaseNode n = DatabaseNode.findById(op.nodeId);
                n.replicationPausedAt = null;
                n.replicationPausedBy = null;
                n.replicationPauseReason = null;
            }
            default -> { }
        }
    }

    private Operation ownOp(DatabaseNode node, UUID id) {
        Operation op = Operation.findById(id);
        if (op == null || !op.nodeId.equals(node.id)) throw new jakarta.ws.rs.NotFoundException();
        return op;
    }

    public void event(Operation op, String level, String message) {
        OperationEvent e = new OperationEvent();
        e.operationId = op.id;
        e.at = Instant.now();
        e.level = level;
        e.message = truncate(message == null ? "" : message, 2000);
        e.persist();
    }

    public String write(Object o) {
        try {
            return json.writeValueAsString(o);
        } catch (JsonProcessingException e) {
            throw new IllegalStateException(e);
        }
    }

    public JsonNode read(String s) {
        try {
            return s == null ? json.createObjectNode() : json.readTree(s);
        } catch (JsonProcessingException e) {
            LOG.warn("JSON illisible", e);
            return json.createObjectNode();
        }
    }

    private static String blankToNull(String s) { return s == null || s.isBlank() ? null : s; }

    static String truncate(String s, int max) {
        return s == null || s.length() <= max ? s : s.substring(0, max - 1) + "…";
    }
}
