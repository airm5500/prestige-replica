package com.prestige.replica.api;

import com.prestige.replica.domain.*;
import com.prestige.replica.security.CurrentUser;
import com.prestige.replica.service.AuditService;
import com.prestige.replica.service.BackupService;
import com.prestige.replica.service.OperationService;
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

    public record ApproveIn(@NotBlank String confirmation) {}
    public record RestoreTestIn(UUID nodeId) {}
    public record ProvisionIn(@NotNull UUID targetNodeId, @NotNull UUID backupId, @NotBlank @Size(max = 500) String confirmDataDir,
                              @Min(0) @Max(604800) Integer delaySeconds, @NotBlank String topologyConfirmation) {}
    public record PolicyIn(boolean backupEnabled, UUID backupNodeId, UUID restoreNodeId, @Min(0) @Max(23) int backupHour,
                           @Min(1) @Max(365) int backupKeep, @Min(1) @Max(7) int restoreTestWeekday, @Min(2) @Max(240) int backupMaxAgeHours) {}
    public record OperationView(Operation operation, List<OperationEvent> events, String nodeName, String siteCode) {}

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
        return Operation.find(q + " ORDER BY requestedAt DESC", f.params()).page(0, Math.max(1, Math.min(limit, 1000))).list();
    }

    @GET @Path("/operations/{id}") @Transactional
    public OperationView operation(@PathParam("id") UUID id) {
        Operation op = op(id);
        DatabaseNode n = DatabaseNode.findById(op.nodeId);
        Site s = Site.findById(op.siteId);
        return new OperationView(op, OperationEvent.list("operationId = ?1 ORDER BY id", op.id), n == null ? null : n.name, s == null ? null : s.code);
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
