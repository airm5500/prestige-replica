package com.prestige.replica.api;

import com.prestige.replica.domain.*;
import com.prestige.replica.security.CurrentUser;
import com.prestige.replica.service.AuditService;
import com.prestige.replica.service.Tokens;
import io.quarkus.security.Authenticated;
import jakarta.annotation.security.RolesAllowed;
import jakarta.inject.Inject;
import jakarta.transaction.Transactional;
import jakarta.validation.Valid;
import jakarta.ws.rs.*;
import jakarta.ws.rs.core.MediaType;
import java.util.List;
import java.util.UUID;

/** Inventaire manuel : tenants, sites (pharmacies), topologies et nœuds. */
@Path("/api/v1")
@Produces(MediaType.APPLICATION_JSON)
@Consumes(MediaType.APPLICATION_JSON)
@Authenticated
public class InventoryResource {
    @Inject CurrentUser me;
    @Inject AuditService audit;

    // ---- Tenants -------------------------------------------------------
    @GET @Path("/tenants")
    public List<Tenant> tenants() {
        if (me.isGlobal()) return Tenant.listAll();
        return me.tenantScope() == null ? List.of() : Tenant.list("id", me.tenantScope());
    }

    @POST @Path("/tenants") @Transactional @RolesAllowed(Roles.SUPER_ADMIN)
    public Tenant createTenant(@Valid Dtos.TenantIn in) {
        if (!me.isGlobal()) throw new ForbiddenException("Seul un super-administrateur global crée des tenants");
        if (Tenant.count("code", in.code()) > 0) throw new ClientErrorException("Code déjà utilisé", 409);
        Tenant t = new Tenant();
        t.code = in.code();
        t.name = in.name();
        t.persist();
        audit.record(me.username(), "TENANT_CREATE", "tenant:" + t.id, "SUCCESS", t.id, null, in.name());
        return t;
    }

    // ---- Sites ---------------------------------------------------------
    @GET @Path("/sites")
    public List<Site> sites() {
        CurrentUser.ScopeFilter f = me.filter("id");
        return Site.list(f.clause() + " ORDER BY name", f.params());
    }

    @POST @Path("/sites") @Transactional @RolesAllowed(Roles.SUPER_ADMIN)
    public Site createSite(@Valid Dtos.SiteIn in) {
        me.checkAccess(in.tenantId(), null);
        if (Tenant.findById(in.tenantId()) == null) throw new NotFoundException("Tenant inconnu");
        if (Site.count("tenantId = ?1 and code = ?2", in.tenantId(), in.code()) > 0) throw new ClientErrorException("Code de site déjà utilisé", 409);
        Site s = new Site();
        s.tenantId = in.tenantId();
        s.code = in.code().toUpperCase();
        s.name = in.name();
        if (in.environment() != null) s.environment = in.environment();
        if (in.color() != null) s.color = in.color();
        if (in.timezone() != null) s.timezone = in.timezone();
        s.persist();
        audit.record(me.username(), "SITE_CREATE", "site:" + s.id, "SUCCESS", s.tenantId, s.id, s.name + " [" + s.code + "]");
        return s;
    }

    // ---- Topologies ----------------------------------------------------
    @GET @Path("/topologies")
    public List<Topology> topologies(@QueryParam("siteId") UUID siteId) {
        CurrentUser.ScopeFilter f = me.filter("siteId");
        String q = f.clause();
        if (siteId != null) {
            q += " and siteId = :site";
            f.params().put("site", siteId);
        }
        return Topology.list(q + " ORDER BY name", f.params());
    }

    @POST @Path("/topologies") @Transactional @RolesAllowed({Roles.SUPER_ADMIN, Roles.SITE_ADMIN})
    public Topology createTopology(@Valid Dtos.TopologyIn in) {
        Site site = site(in.siteId());
        Topology t = new Topology();
        t.tenantId = site.tenantId;
        t.siteId = site.id;
        apply(t, in);
        t.persist();
        audit.record(me.username(), "TOPOLOGY_CREATE", "topology:" + t.id, "SUCCESS", t.tenantId, t.siteId, t.name);
        return t;
    }

    @PUT @Path("/topologies/{id}") @Transactional @RolesAllowed({Roles.SUPER_ADMIN, Roles.SITE_ADMIN})
    public Topology updateTopology(@PathParam("id") UUID id, @Valid Dtos.TopologyIn in) {
        Topology t = Topology.findById(id);
        if (t == null) throw new NotFoundException();
        me.checkAccess(t.tenantId, t.siteId);
        if (!t.siteId.equals(in.siteId())) throw new BadRequestException("Une topologie ne change pas de site");
        boolean wasMaintenance = t.maintenance;
        apply(t, in);
        audit.record(me.username(), "TOPOLOGY_UPDATE", "topology:" + t.id, "SUCCESS", t.tenantId, t.siteId,
                String.format("lag %d/%d s, disque %d/%d %%, maintenance %s→%s", t.lagWarnSeconds, t.lagCritSeconds,
                        t.diskWarnPct, t.diskCritPct, wasMaintenance, t.maintenance));
        return t;
    }

    private static void apply(Topology t, Dtos.TopologyIn in) {
        t.name = in.name();
        if (in.mode() != null) t.mode = in.mode();
        if (in.lagWarnSeconds() != null) t.lagWarnSeconds = in.lagWarnSeconds();
        if (in.lagCritSeconds() != null) t.lagCritSeconds = in.lagCritSeconds();
        if (in.diskWarnPct() != null) t.diskWarnPct = in.diskWarnPct();
        if (in.diskCritPct() != null) t.diskCritPct = in.diskCritPct();
        if (in.agentSilenceSeconds() != null) t.agentSilenceSeconds = in.agentSilenceSeconds();
        if (in.maintenance() != null) t.maintenance = in.maintenance();
        if (t.lagCritSeconds < t.lagWarnSeconds) throw new BadRequestException("Seuil critique de retard inférieur au seuil d'avertissement");
        if (t.diskCritPct > t.diskWarnPct) throw new BadRequestException("Seuil critique disque supérieur au seuil d'avertissement");
    }

    // ---- Nœuds ---------------------------------------------------------
    /**
     * Enregistrement manuel d'un nœud MariaDB. Renvoie le jeton de l'agent une
     * seule fois ; seule son empreinte SHA-256 est conservée.
     */
    @POST @Path("/nodes/register") @Transactional @RolesAllowed({Roles.SUPER_ADMIN, Roles.SITE_ADMIN})
    public Dtos.NodeRegistered register(@Valid Dtos.NodeIn in) {
        Site site = site(in.siteId());
        if (in.topologyId() != null) {
            Topology t = Topology.findById(in.topologyId());
            if (t == null || !t.siteId.equals(site.id)) throw new BadRequestException("Topologie inconnue pour ce site");
        }
        if (DatabaseNode.count("siteId = ?1 and name = ?2", site.id, in.name()) > 0) throw new ClientErrorException("Nom de nœud déjà utilisé sur ce site", 409);
        String token = Tokens.newAgentToken();
        DatabaseNode n = new DatabaseNode();
        n.tenantId = site.tenantId;
        n.siteId = site.id;
        n.topologyId = in.topologyId();
        n.name = in.name();
        n.expectedRole = in.expectedRole();
        n.addressLabel = in.addressLabel();
        n.agentTokenHash = Tokens.sha256(token);
        n.agentTokenPrefix = Tokens.prefix(token);
        n.persist();
        audit.record(me.username(), "NODE_REGISTER", "node:" + n.id, "SUCCESS", n.tenantId, n.siteId,
                n.name + " (" + n.expectedRole + "), jeton " + n.agentTokenPrefix + "…");
        return new Dtos.NodeRegistered(n.id, token, "Copiez ce jeton dans le fichier agentTokenFile de l'agent : il ne sera plus affiché.");
    }

    @POST @Path("/nodes/{id}/rotate-token") @Transactional @RolesAllowed({Roles.SUPER_ADMIN, Roles.SITE_ADMIN})
    public Dtos.NodeRegistered rotate(@PathParam("id") UUID id) {
        DatabaseNode n = DatabaseNode.findById(id);
        if (n == null) throw new NotFoundException();
        me.checkAccess(n.tenantId, n.siteId);
        String old = n.agentTokenPrefix;
        String token = Tokens.newAgentToken();
        n.agentTokenHash = Tokens.sha256(token);
        n.agentTokenPrefix = Tokens.prefix(token);
        audit.record(me.username(), "NODE_TOKEN_ROTATE", "node:" + n.id, "SUCCESS", n.tenantId, n.siteId,
                "ancien " + old + "… révoqué, nouveau " + n.agentTokenPrefix + "…");
        return new Dtos.NodeRegistered(n.id, token, "L'ancien jeton est révoqué immédiatement.");
    }

    private Site site(UUID id) {
        Site s = Site.findById(id);
        if (s == null) throw new NotFoundException("Site inconnu");
        me.checkAccess(s.tenantId, s.id);
        return s;
    }
}
