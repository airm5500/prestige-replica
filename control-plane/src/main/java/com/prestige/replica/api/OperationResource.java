package com.prestige.replica.api;

import com.prestige.replica.domain.*;
import com.prestige.replica.security.CurrentUser;
import com.prestige.replica.service.AuditService;
import com.prestige.replica.service.BackupService;
import com.prestige.replica.service.OperationService;
import com.prestige.replica.service.ReplicationService;
import java.time.Instant;
import io.quarkus.security.Authenticated;
import jakarta.annotation.security.RolesAllowed;
import jakarta.inject.Inject;
import jakarta.transaction.Transactional;
import jakarta.validation.Valid;
import jakarta.validation.constraints.*;
import jakarta.ws.rs.*;
import jakarta.ws.rs.core.MediaType;
import java.util.*;

/** Centre d'opérations, catalogue des sauvegardes et politique de sauvegarde. */
@Path("/api/v1")
@Produces(MediaType.APPLICATION_JSON)
@Consumes(MediaType.APPLICATION_JSON)
@Authenticated
public class OperationResource {
    @Inject CurrentUser me;
    @Inject OperationService ops;
    @Inject BackupService backups;
    @Inject AuditService audit;
    @Inject ReplicationService replication;
    @Inject com.prestige.replica.service.BinlogService binlogs;

    public record ApproveIn(@NotBlank String confirmation) {}
    public record RestoreTestIn(UUID nodeId) {}
    public record ProvisionIn(@NotNull UUID targetNodeId, @NotNull UUID backupId, @NotBlank @Size(max = 500) String confirmDataDir,
                              @Min(0) @Max(604800) Integer delaySeconds, @NotBlank String topologyConfirmation) {}
    public record PolicyIn(boolean backupEnabled, UUID backupNodeId, UUID restoreNodeId, @Min(0) @Max(23) int backupHour,
                           @Min(1) @Max(365) int backupKeep, @Min(1) @Max(7) int restoreTestWeekday, @Min(2) @Max(240) int backupMaxAgeHours) {}
    public record OperationView(Operation operation, List<OperationEvent> events, String nodeName, String siteCode,
                                String siteName, String topologyName, List<OperationView> steps) {}
    public record PauseIn(@Pattern(regexp = "ALL|SQL") String mode, @NotBlank @Size(max = 300) String reason) {}
    public record SwitchoverPlanIn(@NotNull UUID targetNodeId) {}
    public record FailoverPlanIn(UUID targetNodeId) {}
    public record FailoverIn(UUID targetNodeId, boolean fencingConfirmed, @NotBlank String topologyConfirmation) {}
    public record DrillIn(@NotNull UUID targetNodeId, java.time.Instant notBefore, @NotBlank String topologyConfirmation) {}
    public record DrillPolicyIn(@jakarta.validation.constraints.Min(0) @jakarta.validation.constraints.Max(730) int drillMaxAgeDays) {}
    public record ManualFailoverIn(@NotNull UUID newPrimaryNodeId, @NotBlank String topologyConfirmation) {}
    public record BinlogPolicyIn(boolean binlogArchiveEnabled, UUID binlogArchiveNodeId, @Min(1) @Max(60) int binlogArchiveMinutes) {}
    public record PitrPlanIn(@NotNull java.time.Instant stopAt) {}
    public record PitrIn(@NotNull java.time.Instant stopAt, List<String> extract, @NotBlank @Size(max = 300) String reason,
                         com.prestige.replica.service.BinlogService.StopPoint stopBefore) {}
    public record BinlogCatalog(List<com.prestige.replica.domain.BinlogArchive> files, java.time.Instant coveredFrom, java.time.Instant coveredUntil,
                                long totalBytes, java.time.Instant gapAt, String gapDetail, Operation lastRun) {}
    public record SwitchoverIn(@NotNull UUID targetNodeId, boolean reattachOldPrimary, @NotBlank String topologyConfirmation) {}

    static final String[] OPERATORS = {Roles.SUPER_ADMIN, Roles.OPERATOR, Roles.SITE_ADMIN};

    // ---- Demandes ---------------------------------------------------------

    @POST @Path("/nodes/{id}/preflight") @Transactional @RolesAllowed({Roles.SUPER_ADMIN, Roles.OPERATOR, Roles.SITE_ADMIN})
    public Operation preflight(@PathParam("id") UUID id) {
        return backups.requestPreflight(node(id), me.username());
    }

    @POST @Path("/nodes/{id}/backups") @Transactional @RolesAllowed({Roles.SUPER_ADMIN, Roles.OPERATOR, Roles.SITE_ADMIN})
    public Operation backupNow(@PathParam("id") UUID id) {
        DatabaseNode n = node(id);
        if (n.topologyId == null) throw new BadRequestException("Rattachez d'abord ce nœud à une topologie");
        return backups.requestBackup(n, me.username());
    }

    @POST @Path("/backups/{id}/verify") @Transactional @RolesAllowed({Roles.SUPER_ADMIN, Roles.OPERATOR, Roles.SITE_ADMIN})
    public Operation verify(@PathParam("id") UUID id, RestoreTestIn in) {
        Backup b = backup(id);
        UUID exec = in != null && in.nodeId() != null ? in.nodeId() : null;
        if (exec == null && b.topologyId != null) {
            Topology t = Topology.findById(b.topologyId);
            exec = t == null ? null : t.restoreNodeId;
        }
        if (exec == null) throw new BadRequestException("Choisissez le nœud qui exécutera la restauration de contrôle");
        return backups.requestRestoreTest(b, node(exec), me.username());
    }

    @POST @Path("/topologies/{id}/replicas/provision") @Transactional @RolesAllowed({Roles.SUPER_ADMIN, Roles.OPERATOR, Roles.SITE_ADMIN})
    public Operation provision(@PathParam("id") UUID id, @Valid ProvisionIn in) {
        Topology t = topology(id);
        return backups.requestProvision(t, node(in.targetNodeId()), backup(in.backupId()), in.confirmDataDir(),
                in.delaySeconds() == null ? 0 : in.delaySeconds(), in.topologyConfirmation(), me.username());
    }

    @PUT @Path("/topologies/{id}/backup-policy") @Transactional @RolesAllowed({Roles.SUPER_ADMIN, Roles.SITE_ADMIN})
    public Topology policy(@PathParam("id") UUID id, @Valid PolicyIn in) {
        Topology t = topology(id);
        for (UUID n : new UUID[]{in.backupNodeId(), in.restoreNodeId()}) {
            if (n != null) {
                DatabaseNode dn = node(n);
                if (!t.id.equals(dn.topologyId)) throw new BadRequestException("Le nœud " + dn.name + " n'appartient pas à la topologie");
            }
        }
        if (in.backupEnabled() && in.backupNodeId() == null) throw new BadRequestException("Choisissez le nœud qui exécute les sauvegardes");
        t.backupEnabled = in.backupEnabled();
        t.backupNodeId = in.backupNodeId();
        t.restoreNodeId = in.restoreNodeId();
        t.backupHour = in.backupHour();
        t.backupKeep = in.backupKeep();
        t.restoreTestWeekday = in.restoreTestWeekday();
        t.backupMaxAgeHours = in.backupMaxAgeHours();
        audit.record(me.username(), "BACKUP_POLICY_UPDATE", "topology:" + t.id, "SUCCESS", t.tenantId, t.siteId,
                String.format("actif=%s, %02dh, conserver %d, test jour %d", t.backupEnabled, t.backupHour, t.backupKeep, t.restoreTestWeekday));
        return t;
    }

    // ---- Journaux binaires et restauration à un instant précis --------------

    @PUT @Path("/topologies/{id}/binlog-policy") @Transactional @RolesAllowed({Roles.SUPER_ADMIN, Roles.SITE_ADMIN})
    public Topology binlogPolicy(@PathParam("id") UUID id, @Valid BinlogPolicyIn in) {
        Topology t = topology(id);
        if (in.binlogArchiveNodeId() != null) {
            DatabaseNode dn = node(in.binlogArchiveNodeId());
            if (!t.id.equals(dn.topologyId)) throw new BadRequestException("Le nœud " + dn.name + " n'appartient pas à la topologie");
        }
        if (in.binlogArchiveEnabled() && in.binlogArchiveNodeId() == null && t.backupNodeId == null) {
            throw new BadRequestException("Choisissez le serveur qui archive les journaux (par défaut, celui des sauvegardes)");
        }
        t.binlogArchiveEnabled = in.binlogArchiveEnabled();
        t.binlogArchiveNodeId = in.binlogArchiveNodeId();
        t.binlogArchiveMinutes = in.binlogArchiveMinutes();
        audit.record(me.username(), "BINLOG_POLICY_UPDATE", "topology:" + t.id, "SUCCESS", t.tenantId, t.siteId,
                "archivage=" + t.binlogArchiveEnabled + ", toutes les " + t.binlogArchiveMinutes + " min");
        return t;
    }

    @GET @Path("/topologies/{id}/binlogs") @Transactional
    public BinlogCatalog binlogCatalog(@PathParam("id") UUID id) {
        Topology t = topology(id);
        List<com.prestige.replica.domain.BinlogArchive> files = com.prestige.replica.domain.BinlogArchive
                .<com.prestige.replica.domain.BinlogArchive>list("topologyId = ?1 ORDER BY firstEventAt DESC NULLS LAST, fileName DESC", t.id);
        java.time.Instant from = files.stream().map(f -> f.firstEventAt).filter(java.util.Objects::nonNull).min(java.util.Comparator.naturalOrder()).orElse(null);
        java.time.Instant until = files.stream().map(f -> f.lastEventAt).filter(java.util.Objects::nonNull).max(java.util.Comparator.naturalOrder()).orElse(null);
        long total = files.stream().mapToLong(f -> f.sizeBytes == null ? 0 : f.sizeBytes).sum();
        Operation last = Operation.find("topologyId = ?1 and type = ?2 ORDER BY requestedAt DESC", t.id, OperationType.BINLOG_ARCHIVE).firstResult();
        return new BinlogCatalog(files.size() > 200 ? files.subList(0, 200) : files, from, until, total, t.binlogGapAt, t.binlogGapDetail, last);
    }

    @POST @Path("/topologies/{id}/pitr/plan") @Transactional
    public com.prestige.replica.service.BinlogService.PitrPlan pitrPlan(@PathParam("id") UUID id, @Valid PitrPlanIn in) {
        return binlogs.plan(topology(id), in.stopAt(), java.time.Instant.now());
    }

    @POST @Path("/topologies/{id}/pitr") @Transactional @RolesAllowed({Roles.SUPER_ADMIN, Roles.OPERATOR, Roles.SITE_ADMIN})
    public Operation pitr(@PathParam("id") UUID id, @Valid PitrIn in) {
        return binlogs.requestPitr(topology(id), in.stopAt(), in.extract(), in.reason(), in.stopBefore(), me.username());
    }

    // ---- Lot 3 : opérations contrôlées ---------------------------------------

    @POST @Path("/nodes/{id}/replication/pause") @Transactional @RolesAllowed({Roles.SUPER_ADMIN, Roles.OPERATOR, Roles.SITE_ADMIN})
    public Operation pause(@PathParam("id") UUID id, @Valid PauseIn in) {
        return replication.pause(node(id), in.mode(), in.reason(), me.username());
    }

    @POST @Path("/nodes/{id}/replication/resume") @Transactional @RolesAllowed({Roles.SUPER_ADMIN, Roles.OPERATOR, Roles.SITE_ADMIN})
    public Operation resume(@PathParam("id") UUID id) {
        return replication.resume(node(id), me.username());
    }

    /** Rattache une réplique (par exemple l'ancien primaire isolé après une bascule) au primaire actuel. */
    @POST @Path("/nodes/{id}/replication/attach") @Transactional @RolesAllowed({Roles.SUPER_ADMIN, Roles.OPERATOR, Roles.SITE_ADMIN})
    public Operation attach(@PathParam("id") UUID id) {
        DatabaseNode n = node(id);
        if (n.expectedRole != NodeRole.REPLICA && n.expectedRole != NodeRole.DELAYED_REPLICA) {
            throw new BadRequestException("Seul un nœud déclaré réplique peut être rattaché");
        }
        DatabaseNode primary = backups.primaryOf(n.topologyId);
        if (primary == null) throw new BadRequestException("Aucun primaire déclaré dans la topologie");
        String[] hp = com.prestige.replica.service.BackupService.hostPortOf(primary.addressLabel);
        return replication.attach(n, primary, hp, me.username());
    }

    @GET @Path("/nodes/{id}/repair-plan") @Transactional
    public ReplicationService.RepairPlan repairPlan(@PathParam("id") UUID id) {
        return replication.repairPlan(node(id), Instant.now());
    }

    @POST @Path("/topologies/{id}/failover/plan") @Transactional
    public ReplicationService.FailoverPlan failoverPlan(@PathParam("id") UUID id, FailoverPlanIn in) {
        return replication.failoverPlan(topology(id), in == null ? null : in.targetNodeId(), java.time.Instant.now(), null);
    }

    @POST @Path("/topologies/{id}/failover") @Transactional @RolesAllowed({Roles.SUPER_ADMIN, Roles.OPERATOR, Roles.SITE_ADMIN})
    public Operation failover(@PathParam("id") UUID id, @Valid FailoverIn in) {
        return replication.requestFailover(topology(id), in.targetNodeId(), in.fencingConfirmed(), in.topologyConfirmation(), me.username());
    }

    @POST @Path("/topologies/{id}/drill") @Transactional @RolesAllowed({Roles.SUPER_ADMIN, Roles.OPERATOR, Roles.SITE_ADMIN})
    public Operation drill(@PathParam("id") UUID id, @Valid DrillIn in) {
        return replication.requestDrill(topology(id), node(in.targetNodeId()), in.notBefore(), in.topologyConfirmation(), me.username());
    }

    @PUT @Path("/topologies/{id}/drill-policy") @Transactional @RolesAllowed({Roles.SUPER_ADMIN, Roles.SITE_ADMIN})
    public Topology drillPolicy(@PathParam("id") UUID id, @Valid DrillPolicyIn in) {
        Topology t = topology(id);
        int before = t.drillMaxAgeDays;
        t.drillMaxAgeDays = in.drillMaxAgeDays();
        audit.record(me.username(), "TOPOLOGY_DRILL_POLICY", "topology:" + t.id, "SUCCESS", t.tenantId, t.siteId,
                "rappel d'exercice " + before + " → " + t.drillMaxAgeDays + " jours");
        return t;
    }

    @POST @Path("/topologies/{id}/failover/declare-manual") @Transactional @RolesAllowed({Roles.SUPER_ADMIN, Roles.SITE_ADMIN})
    public void declareManual(@PathParam("id") UUID id, @Valid ManualFailoverIn in) {
        replication.declareManualFailover(topology(id), node(in.newPrimaryNodeId()), in.topologyConfirmation(), me.username());
    }

    @POST @Path("/topologies/{id}/switchover/plan") @Transactional
    public ReplicationService.SwitchoverPlan switchoverPlan(@PathParam("id") UUID id, @Valid SwitchoverPlanIn in) {
        return replication.plan(topology(id), node(in.targetNodeId()), Instant.now());
    }

    @POST @Path("/topologies/{id}/switchover") @Transactional @RolesAllowed({Roles.SUPER_ADMIN, Roles.OPERATOR, Roles.SITE_ADMIN})
    public Operation switchover(@PathParam("id") UUID id, @Valid SwitchoverIn in) {
        return replication.requestSwitchover(topology(id), node(in.targetNodeId()), in.reattachOldPrimary(), in.topologyConfirmation(), me.username());
    }

    // ---- Suivi -------------------------------------------------------------

    @GET @Path("/backups") @Transactional
    public List<Backup> list(@QueryParam("topologyId") UUID topologyId, @QueryParam("includeDeleted") boolean includeDeleted) {
        CurrentUser.ScopeFilter f = me.filter("siteId");
        String q = f.clause();
        if (topologyId != null) { q += " and topologyId = :topo"; f.params().put("topo", topologyId); }
        if (!includeDeleted) { q += " and status <> :deleted"; f.params().put("deleted", Backup.Status.DELETED); }
        return Backup.find(q + " ORDER BY startedAt DESC", f.params()).page(0, 500).list();
    }

    @GET @Path("/backups/{id}/restore-tests") @Transactional
    public List<RestoreTest> tests(@PathParam("id") UUID id) {
        return RestoreTest.list("backupId = ?1 ORDER BY at DESC", backup(id).id);
    }

    @GET @Path("/operations") @Transactional
    public List<Operation> operations(@QueryParam("status") String status, @QueryParam("nodeId") UUID nodeId,
                                      @QueryParam("topologyId") UUID topologyId, @QueryParam("type") OperationType type,
                                      @QueryParam("limit") @DefaultValue("200") int limit) {
        CurrentUser.ScopeFilter f = me.filter("siteId");
        String q = f.clause();
        if (status != null && !status.isBlank()) {
            if ("active".equals(status)) {
                q += " and status in (:active)";
                f.params().put("active", List.of(OperationStatus.AWAITING_APPROVAL, OperationStatus.QUEUED, OperationStatus.RUNNING));
            } else {
                q += " and status = :st";
                f.params().put("st", OperationStatus.valueOf(status));
            }
        }
        if (nodeId != null) { q += " and nodeId = :node"; f.params().put("node", nodeId); }
        if (topologyId != null) { q += " and topologyId = :topo"; f.params().put("topo", topologyId); }
        if (type != null) { q += " and type = :type"; f.params().put("type", type); }
        else {
            // Archivages réussis (toutes les quelques minutes) : visibles via le filtre de type et la page Sauvegardes.
            q += " and not (type = :archive and status = :ok)";
            f.params().put("archive", OperationType.BINLOG_ARCHIVE);
            f.params().put("ok", OperationStatus.SUCCEEDED);
        }
        List<Operation> list = Operation.<Operation>find(q + " ORDER BY requestedAt DESC", f.params()).page(0, Math.max(1, Math.min(limit, 1000))).list();
        for (Operation o : list) {
            // Résultats de recherche (données de la pharmacie) : seulement dans le détail, selon les droits.
            if (o.type == OperationType.BINLOG_SEARCH && o.result != null) {
                Operation.getEntityManager().detach(o);
                o.result = null;
            }
        }
        return list;
    }

    @GET @Path("/operations/{id}") @Transactional
    public OperationView operation(@PathParam("id") UUID id) {
        Operation op = op(id);
        if (op.type == OperationType.BINLOG_SEARCH && op.result != null && !canReadData()) {
            Operation.getEntityManager().detach(op);
            op.result = ops.write(maskSearch(ops.read(op.result)));
        }
        return view(op, true);
    }

    /** Valeurs des lignes visibles par les rôles d'exploitation et d'audit ; masquées pour les autres. */
    boolean canReadData() {
        return me.hasRole(Roles.SUPER_ADMIN) || me.hasRole(Roles.SITE_ADMIN) || me.hasRole(Roles.OPERATOR) || me.hasRole(Roles.AUDITOR);
    }

    static com.fasterxml.jackson.databind.JsonNode maskSearch(com.fasterxml.jackson.databind.JsonNode r) {
        if (!(r instanceof com.fasterxml.jackson.databind.node.ObjectNode o)) return r;
        o.put("masked", true);
        for (com.fasterxml.jackson.databind.JsonNode tx : o.path("transactions")) {
            ((com.fasterxml.jackson.databind.node.ObjectNode) tx).remove("queries");
            for (com.fasterxml.jackson.databind.JsonNode c : tx.path("changes")) {
                var co = (com.fasterxml.jackson.databind.node.ObjectNode) c;
                co.remove("sample");
                if (co.has("statement")) co.put("statement", "(masqué)");
            }
        }
        return o;
    }

    @POST @Path("/topologies/{id}/binlog-search") @Transactional @RolesAllowed({Roles.SUPER_ADMIN, Roles.OPERATOR, Roles.SITE_ADMIN})
    public Operation binlogSearch(@PathParam("id") UUID id, com.prestige.replica.service.BinlogService.SearchRequest in) {
        if (in == null) throw new BadRequestException("Paramètres manquants");
        return binlogs.requestSearch(topology(id), in, me.username());
    }

    static OperationView view(Operation op, boolean withSteps) {
        DatabaseNode n = DatabaseNode.findById(op.nodeId);
        Site s = Site.findById(op.siteId);
        Topology t = op.topologyId == null ? null : Topology.findById(op.topologyId);
        List<OperationView> steps = withSteps
                ? Operation.<Operation>list("parentId = ?1 ORDER BY requestedAt", op.id).stream().map(c -> view(c, false)).toList()
                : List.of();
        return new OperationView(op, OperationEvent.list("operationId = ?1 ORDER BY id", op.id), n == null ? null : n.name,
                s == null ? null : s.code, s == null ? null : s.name, t == null ? null : t.name, steps);
    }

    @POST @Path("/operations/{id}/approve") @Transactional @RolesAllowed({Roles.SUPER_ADMIN, Roles.APPROVER})
    public Operation approve(@PathParam("id") UUID id, @Valid ApproveIn in) {
        Operation op = op(id);
        return ops.approve(op, me.username(), me.hasRole(Roles.APPROVER) || me.hasRole(Roles.SUPER_ADMIN), in.confirmation());
    }

    @POST @Path("/operations/{id}/cancel") @Transactional @RolesAllowed({Roles.SUPER_ADMIN, Roles.OPERATOR, Roles.SITE_ADMIN, Roles.APPROVER})
    public Operation cancel(@PathParam("id") UUID id) {
        return ops.cancel(op(id), me.username());
    }

    // ---- Périmètre ---------------------------------------------------------

    private Operation op(UUID id) {
        Operation op = Operation.findById(id);
        if (op == null) throw new NotFoundException();
        me.checkAccess(op.tenantId, op.siteId);
        return op;
    }

    private DatabaseNode node(UUID id) {
        DatabaseNode n = DatabaseNode.findById(id);
        if (n == null) throw new NotFoundException("Nœud inconnu");
        me.checkAccess(n.tenantId, n.siteId);
        return n;
    }

    private Backup backup(UUID id) {
        Backup b = Backup.findById(id);
        if (b == null) throw new NotFoundException("Sauvegarde inconnue");
        me.checkAccess(b.tenantId, b.siteId);
        return b;
    }

    private Topology topology(UUID id) {
        Topology t = Topology.findById(id);
        if (t == null) throw new NotFoundException();
        me.checkAccess(t.tenantId, t.siteId);
        return t;
    }
}
