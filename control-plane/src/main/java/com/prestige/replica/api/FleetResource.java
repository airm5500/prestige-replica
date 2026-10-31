package com.prestige.replica.api;

import com.prestige.replica.alert.AgentReport;
import com.prestige.replica.domain.*;
import com.prestige.replica.security.CurrentUser;
import com.prestige.replica.service.MonitoringService;
import io.quarkus.security.Authenticated;
import jakarta.inject.Inject;
import jakarta.transaction.Transactional;
import jakarta.ws.rs.*;
import jakarta.ws.rs.core.MediaType;
import java.time.Duration;
import java.time.Instant;
import java.util.*;

/** Vues de supervision : parc, fiche nœud, historique de métriques. */
@Path("/api/v1")
@Produces(MediaType.APPLICATION_JSON)
@Authenticated
public class FleetResource {
    @Inject CurrentUser me;
    @Inject MonitoringService monitoring;

    @GET @Path("/fleet") @Transactional
    public List<Dtos.SiteSummary> fleet() {
        CurrentUser.ScopeFilter fs = me.filter("id");
        List<Site> sites = Site.list(fs.clause() + " ORDER BY name", fs.params());
        CurrentUser.ScopeFilter f = me.filter("siteId");
        List<DatabaseNode> nodes = DatabaseNode.list(f.clause() + " ORDER BY name", f.params());
        List<Topology> topos = Topology.list(f.clause() + " ORDER BY name", f.params());
        List<HealthIncident> open = HealthIncident.list(f.clause() + " and status <> :resolved",
                with(f.params(), "resolved", IncidentStatus.RESOLVED));

        List<Dtos.SiteSummary> out = new ArrayList<>();
        for (Site s : sites) {
            List<HealthIncident> siteInc = open.stream().filter(i -> i.siteId.equals(s.id)).toList();
            List<Dtos.TopologySummary> ts = new ArrayList<>();
            for (Topology t : topos) {
                if (!t.siteId.equals(s.id)) continue;
                List<Dtos.NodeSummary> ns = nodes.stream().filter(n -> t.id.equals(n.topologyId)).map(n -> summary(n, open)).toList();
                List<HealthIncident> topoInc = siteInc.stream().filter(i -> t.id.equals(i.topologyId)).toList();
                String status = t.maintenance ? "MAINTENANCE" : status(topoInc, ns.isEmpty());
                String primary = ns.stream().filter(n -> n.expectedRole() == NodeRole.PRIMARY).map(Dtos.NodeSummary::name).findFirst().orElse(null);
                ts.add(new Dtos.TopologySummary(t.id, t.name, t.mode, t.maintenance, status, primary, rpo(ns), ns));
            }
            List<Dtos.NodeSummary> loose = nodes.stream().filter(n -> n.siteId.equals(s.id) && n.topologyId == null).map(n -> summary(n, open)).toList();
            long crit = siteInc.stream().filter(i -> i.severity == Severity.CRITICAL).count();
            out.add(new Dtos.SiteSummary(s.id, s.tenantId, s.code, s.name, s.environment, s.color,
                    status(siteInc, ts.isEmpty() && loose.isEmpty()), siteInc.size(), crit, ts, loose));
        }
        return out;
    }

    @GET @Path("/nodes") @Transactional
    public List<Dtos.NodeSummary> nodes(@QueryParam("siteId") UUID siteId) {
        CurrentUser.ScopeFilter f = me.filter("siteId");
        String q = f.clause();
        if (siteId != null) { q += " and siteId = :site"; f.params().put("site", siteId); }
        List<HealthIncident> open = HealthIncident.list(f.clause() + " and status <> :resolved", with(f.params(), "resolved", IncidentStatus.RESOLVED));
        return DatabaseNode.<DatabaseNode>list(q + " ORDER BY name", f.params()).stream().map(n -> summary(n, open)).toList();
    }

    /** Fiche nœud : résumé, dernier rapport complet et incidents ouverts. */
    @GET @Path("/nodes/{id}") @Transactional
    public Map<String, Object> node(@PathParam("id") UUID id) {
        DatabaseNode n = node0(id);
        List<HealthIncident> open = HealthIncident.list("nodeId = ?1 and status <> ?2 ORDER BY severity DESC, openedAt", n.id, IncidentStatus.RESOLVED);
        Map<String, Object> m = new LinkedHashMap<>();
        m.put("summary", summary(n, open));
        m.put("report", monitoring.parse(n.lastReport));
        m.put("incidents", open);
        m.put("site", Site.findById(n.siteId));
        m.put("topology", n.topologyId == null ? null : Topology.findById(n.topologyId));
        return m;
    }

    @GET @Path("/nodes/{id}/health") @Transactional
    public Dtos.NodeSummary health(@PathParam("id") UUID id) {
        DatabaseNode n = node0(id);
        return summary(n, HealthIncident.list("nodeId = ?1 and status <> ?2", n.id, IncidentStatus.RESOLVED));
    }

    @GET @Path("/nodes/{id}/metrics") @Transactional
    public List<NodeMetric> metrics(@PathParam("id") UUID id, @QueryParam("hours") @DefaultValue("24") int hours) {
        DatabaseNode n = node0(id);
        int h = Math.max(1, Math.min(hours, 24 * 31));
        return NodeMetric.list("nodeId = ?1 and collectedAt >= ?2 ORDER BY collectedAt", n.id, Instant.now().minus(Duration.ofHours(h)));
    }

    private DatabaseNode node0(UUID id) {
        DatabaseNode n = DatabaseNode.findById(id);
        if (n == null) throw new NotFoundException();
        me.checkAccess(n.tenantId, n.siteId);
        return n;
    }

    private Dtos.NodeSummary summary(DatabaseNode n, List<HealthIncident> openAll) {
        List<HealthIncident> inc = openAll.stream().filter(i -> n.id.equals(i.nodeId)).toList();
        AgentReport r = monitoring.parse(n.lastReport);
        AgentReport.Server s = r == null ? null : r.server();
        List<Dtos.ChannelView> ch = r == null ? List.of() : r.channelsOrEmpty().stream().map(Dtos.ChannelView::of).toList();
        Double disk = r == null ? null : r.disksOrEmpty().stream().mapToDouble(AgentReport.Disk::freePct).min().stream().boxed().findFirst().orElse(null);
        String worst = inc.stream().map(i -> i.severity).max(Comparator.naturalOrder()).map(Enum::name).orElse(null);
        return new Dtos.NodeSummary(n.id, n.name, n.expectedRole, n.topologyId, n.siteId, n.addressLabel,
                n.lastSeenAt == null ? "UNKNOWN" : status(inc, false), n.lastSeenAt, n.lastReportAt,
                r != null && r.reachable(), s == null ? null : s.version(), s == null ? null : s.serverId(),
                s == null ? null : s.readOnly(), s == null ? null : s.gtidCurrentPos(), s == null ? null : s.gtidSlavePos(),
                ch, disk, n.agentVersion, n.agentTokenPrefix, inc.size(), worst, r == null ? List.of() : r.capabilitiesOrEmpty(),
                n.replicationPausedAt, n.replicationPausedBy, n.replicationPauseReason);
    }

    /**
     * Estimation grossière du RPO : retard de la meilleure réplique saine (en
     * asynchrone, les transactions non encore reçues seraient perdues).
     */
    private static Long rpo(List<Dtos.NodeSummary> ns) {
        return ns.stream().filter(n -> n.expectedRole() == NodeRole.REPLICA)
                .flatMap(n -> n.channels().stream())
                .filter(c -> "Yes".equals(c.ioRunning()) && "Yes".equals(c.sqlRunning()) && c.lagSeconds() != null)
                .map(Dtos.ChannelView::lagSeconds).min(Long::compare).orElse(null);
    }

    static String status(List<HealthIncident> inc, boolean empty) {
        if (inc.stream().anyMatch(i -> i.severity == Severity.CRITICAL)) return "CRITICAL";
        if (inc.stream().anyMatch(i -> i.severity == Severity.WARNING)) return "DEGRADED";
        return empty ? "UNKNOWN" : "HEALTHY";
    }

    private static Map<String, Object> with(Map<String, Object> p, String k, Object v) {
        Map<String, Object> m = new HashMap<>(p);
        m.put(k, v);
        return m;
    }
}
