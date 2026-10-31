package com.prestige.replica.service;

import com.prestige.replica.alert.Finding;
import com.prestige.replica.domain.HealthIncident;
import com.prestige.replica.domain.IncidentStatus;
import com.prestige.replica.notify.Notifier;
import jakarta.enterprise.context.ApplicationScoped;
import jakarta.inject.Inject;
import jakarta.transaction.Transactional;
import java.time.Instant;
import java.util.HashMap;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.UUID;

/**
 * Rapproche les constats courants des incidents ouverts : ouverture,
 * aggravation, mise à jour et résolution automatique lorsque la condition
 * disparaît. Un incident acquitté reste acquitté tant qu'il n'est pas résolu.
 */
@ApplicationScoped
public class IncidentService {
    @Inject Notifier notifier;

    /** Portée d'un rapprochement : un nœud, ou une topologie entière. */
    public record Scope(UUID tenantId, UUID siteId, UUID topologyId, UUID nodeId, String prefix) {
        public static Scope node(UUID tenantId, UUID siteId, UUID topologyId, UUID nodeId) {
            return new Scope(tenantId, siteId, topologyId, nodeId, "node:" + nodeId + ":");
        }
        public static Scope topology(UUID tenantId, UUID siteId, UUID topologyId) {
            return new Scope(tenantId, siteId, topologyId, null, "topo:" + topologyId + ":");
        }
    }

    @Transactional(Transactional.TxType.MANDATORY)
    public void reconcile(Scope scope, List<Finding> findings, Instant now) {
        List<HealthIncident> open = HealthIncident.list("dedupKey like ?1 and status <> ?2", scope.prefix() + "%", IncidentStatus.RESOLVED);
        Map<String, HealthIncident> byKey = new HashMap<>();
        open.forEach(i -> byKey.put(i.dedupKey, i));
        Set<String> current = new HashSet<>();

        for (Finding f : findings) {
            String key = scope.prefix() + f.type() + ":" + f.key();
            current.add(key);
            HealthIncident inc = byKey.get(key);
            if (inc == null) {
                inc = new HealthIncident();
                inc.tenantId = scope.tenantId();
                inc.siteId = scope.siteId();
                inc.topologyId = scope.topologyId();
                inc.nodeId = scope.nodeId();
                inc.type = f.type();
                inc.dedupKey = key;
                inc.severity = f.severity();
                inc.message = truncate(f.message(), 1000);
                inc.details = f.details();
                inc.openedAt = now;
                inc.lastSeenAt = now;
                inc.persist();
                notifier.notify(inc, Notifier.Event.OPENED);
            } else {
                boolean escalated = f.severity().ordinal() > inc.severity.ordinal();
                inc.severity = f.severity();
                inc.message = truncate(f.message(), 1000);
                inc.details = f.details();
                inc.lastSeenAt = now;
                if (escalated) notifier.notify(inc, Notifier.Event.ESCALATED);
            }
        }
        for (HealthIncident inc : open) {
            if (!current.contains(inc.dedupKey)) {
                inc.status = IncidentStatus.RESOLVED;
                inc.resolvedAt = now;
                notifier.notify(inc, Notifier.Event.RESOLVED);
            }
        }
    }

    private static String truncate(String s, int max) {
        return s == null || s.length() <= max ? s : s.substring(0, max - 1) + "…";
    }
}
