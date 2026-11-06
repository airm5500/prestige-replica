package com.prestige.replica.api;

import com.prestige.replica.domain.*;
import com.prestige.replica.security.CurrentUser;
import com.prestige.replica.service.AuditService;
import io.quarkus.security.Authenticated;
import jakarta.annotation.security.RolesAllowed;
import jakarta.inject.Inject;
import jakarta.transaction.Transactional;
import jakarta.validation.Valid;
import jakarta.ws.rs.*;
import jakarta.ws.rs.core.MediaType;
import java.time.Instant;
import java.util.List;
import java.util.UUID;

@Path("/api/v1/incidents")
@Produces(MediaType.APPLICATION_JSON)
@Consumes(MediaType.APPLICATION_JSON)
@Authenticated
public class IncidentResource {
    @Inject CurrentUser me;
    @Inject AuditService audit;

    @GET @Transactional
    public List<HealthIncident> list(@QueryParam("status") @DefaultValue("open") String status,
                                     @QueryParam("siteId") UUID siteId, @QueryParam("limit") @DefaultValue("200") int limit) {
        CurrentUser.ScopeFilter f = me.filter("siteId");
        String q = f.clause();
        if ("open".equalsIgnoreCase(status)) {
            q += " and status <> :resolved";
            f.params().put("resolved", IncidentStatus.RESOLVED);
        }
        if (siteId != null) { q += " and siteId = :site"; f.params().put("site", siteId); }
        return HealthIncident.find(q + " ORDER BY openedAt DESC", f.params()).page(0, Math.max(1, Math.min(limit, 1000))).list();
    }

    @POST @Path("/{id}/acknowledge") @Transactional
    @RolesAllowed({Roles.SUPER_ADMIN, Roles.OPERATOR, Roles.SITE_ADMIN})
    public HealthIncident acknowledge(@PathParam("id") UUID id, @Valid Dtos.AckIn in) {
        HealthIncident i = HealthIncident.findById(id);
        if (i == null) throw new NotFoundException();
        me.checkAccess(i.tenantId, i.siteId);
        if (i.status == IncidentStatus.RESOLVED) throw new ClientErrorException("Incident déjà résolu", 409);
        i.status = IncidentStatus.ACKNOWLEDGED;
        i.acknowledgedAt = Instant.now();
        i.acknowledgedBy = me.username();
        i.assignee = in == null || in.assignee() == null || in.assignee().isBlank() ? me.username() : in.assignee();
        audit.record(me.username(), "INCIDENT_ACK", "incident:" + i.id, "SUCCESS", i.tenantId, i.siteId,
                i.type + " — responsable " + i.assignee);
        return i;
    }
}
