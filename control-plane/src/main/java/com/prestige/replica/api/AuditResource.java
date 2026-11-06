package com.prestige.replica.api;

import com.prestige.replica.domain.AuditEvent;
import com.prestige.replica.domain.Roles;
import com.prestige.replica.security.CurrentUser;
import com.prestige.replica.service.AuditService;
import jakarta.annotation.security.RolesAllowed;
import jakarta.inject.Inject;
import jakarta.transaction.Transactional;
import jakarta.ws.rs.*;
import jakarta.ws.rs.core.MediaType;
import jakarta.ws.rs.core.Response;
import java.util.List;

/** Consultation, export et vérification de l'intégrité du journal d'audit. */
@Path("/api/v1/audit")
@Produces(MediaType.APPLICATION_JSON)
@RolesAllowed({Roles.SUPER_ADMIN, Roles.AUDITOR, Roles.SITE_ADMIN, Roles.OPERATOR, Roles.APPROVER})
public class AuditResource {
    @Inject CurrentUser me;
    @Inject AuditService audit;

    @GET @Transactional
    public List<AuditEvent> list(@QueryParam("actor") String actor, @QueryParam("action") String action,
                                 @QueryParam("beforeId") Long beforeId, @QueryParam("limit") @DefaultValue("100") int limit) {
        return query(actor, action, beforeId, Math.max(1, Math.min(limit, 1000)));
    }

    @GET @Path("/export") @Produces("text/csv") @Transactional
    public Response export(@QueryParam("actor") String actor, @QueryParam("action") String action) {
        StringBuilder sb = new StringBuilder("id;date_utc;acteur;action;ressource;resultat;details;empreinte\n");
        for (AuditEvent e : query(actor, action, null, 100_000)) {
            sb.append(e.id).append(';').append(e.at).append(';').append(csv(e.actor)).append(';').append(csv(e.action))
              .append(';').append(csv(e.resource)).append(';').append(e.result).append(';').append(csv(e.details))
              .append(';').append(e.hash).append('\n');
        }
        audit.record(me.username(), "AUDIT_EXPORT", null, "SUCCESS", me.tenantScope(), me.siteScope(), null);
        return Response.ok(sb.toString()).header("Content-Disposition", "attachment; filename=audit.csv").build();
    }

    /** Vérifie toute la chaîne ; réservé aux rôles globaux pour ne rien divulguer entre tenants. */
    @GET @Path("/verify") @RolesAllowed({Roles.SUPER_ADMIN, Roles.AUDITOR})
    public AuditService.Verification verify() {
        return audit.verify();
    }

    private List<AuditEvent> query(String actor, String action, Long beforeId, int limit) {
        CurrentUser.ScopeFilter f = me.filter("siteId");
        String q = f.clause();
        if (actor != null && !actor.isBlank()) { q += " and actor = :actor"; f.params().put("actor", actor); }
        if (action != null && !action.isBlank()) { q += " and action = :action"; f.params().put("action", action); }
        if (beforeId != null) { q += " and id < :before"; f.params().put("before", beforeId); }
        return AuditEvent.find(q + " ORDER BY id DESC", f.params()).page(0, limit).list();
    }

    private static String csv(String s) {
        if (s == null) return "";
        String v = s.replace("\"", "\"\"").replace("\n", " ");
        // Neutralise les formules lors d'une ouverture dans un tableur.
        if (!v.isEmpty() && "=+-@".indexOf(v.charAt(0)) >= 0) v = "'" + v;
        return "\"" + v + "\"";
    }
}
