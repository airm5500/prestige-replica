package com.prestige.replica.api;

import com.prestige.replica.domain.DatabaseNode;
import com.prestige.replica.security.AgentAuthenticator;
import com.prestige.replica.service.AuditService;
import com.prestige.replica.service.MonitoringService;
import io.vertx.ext.web.RoutingContext;
import jakarta.annotation.security.PermitAll;
import jakarta.inject.Inject;
import jakarta.validation.Valid;
import jakarta.ws.rs.*;
import jakarta.ws.rs.core.Context;
import jakarta.ws.rs.core.HttpHeaders;
import jakarta.ws.rs.core.MediaType;
import java.time.Instant;

/**
 * Point d'entrée des agents. Authentification par jeton propre au nœud (et
 * mTLS si configuré). L'agent ne reçoit jamais de commande en retour.
 */
@Path("/api/v1/agent")
@Produces(MediaType.APPLICATION_JSON)
@Consumes(MediaType.APPLICATION_JSON)
@PermitAll
public class AgentResource {
    @Inject AgentAuthenticator auth;
    @Inject MonitoringService monitoring;
    @Inject AuditService audit;
    @Context RoutingContext routing;

    @POST
    @Path("/reports")
    public Dtos.IngestResult reports(@HeaderParam(HttpHeaders.AUTHORIZATION) String authorization, @Valid Dtos.ReportBatch batch) {
        DatabaseNode node = auth.authenticate(authorization, routing);
        if (node.lastSeenAt == null) {
            audit.record("agent:" + node.agentTokenPrefix, "AGENT_FIRST_CONTACT", "node:" + node.id, "SUCCESS", node.tenantId, node.siteId, null);
        }
        int n = monitoring.ingest(node.id, batch.reports());
        return new Dtos.IngestResult(n, Instant.now());
    }

    // ---- Opérations (Lot 2) : l'agent vient chercher son travail ---------

    public record JobOut(java.util.UUID id, String type, com.fasterxml.jackson.databind.JsonNode params) {}
    public record JobEventIn(@jakarta.validation.constraints.Pattern(regexp = "INFO|WARN") String level,
                             @jakarta.validation.constraints.NotNull @jakarta.validation.constraints.Size(max = 2000) String message) {}
    public record JobFinishIn(@jakarta.validation.constraints.NotNull String status, String error,
                              com.fasterxml.jackson.databind.JsonNode result) {}

    @jakarta.inject.Inject com.prestige.replica.service.OperationService operations;

    @GET
    @Path("/jobs")
    public java.util.List<JobOut> jobs(@HeaderParam(HttpHeaders.AUTHORIZATION) String authorization) {
        DatabaseNode node = auth.authenticate(authorization, routing);
        return operations.pending(node).stream()
                .map(o -> new JobOut(o.id, o.type.name(), operations.read(o.params))).toList();
    }

    @POST
    @Path("/jobs/{id}/start")
    public void start(@HeaderParam(HttpHeaders.AUTHORIZATION) String authorization, @PathParam("id") java.util.UUID id) {
        operations.start(auth.authenticate(authorization, routing), id);
    }

    @POST
    @Path("/jobs/{id}/events")
    public void event(@HeaderParam(HttpHeaders.AUTHORIZATION) String authorization, @PathParam("id") java.util.UUID id, @Valid JobEventIn in) {
        operations.agentEvent(auth.authenticate(authorization, routing), id, in.level(), in.message());
    }

    @POST
    @Path("/jobs/{id}/finish")
    public void finish(@HeaderParam(HttpHeaders.AUTHORIZATION) String authorization, @PathParam("id") java.util.UUID id, @Valid JobFinishIn in) {
        operations.finish(auth.authenticate(authorization, routing), id, in.status(), in.error(), in.result());
    }
}
