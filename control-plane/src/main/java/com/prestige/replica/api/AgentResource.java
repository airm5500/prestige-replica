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
}
