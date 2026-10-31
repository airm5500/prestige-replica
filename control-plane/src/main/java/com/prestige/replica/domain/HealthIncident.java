package com.prestige.replica.domain;

import io.quarkus.hibernate.orm.panache.PanacheEntityBase;
import jakarta.persistence.*;
import java.time.Instant;
import java.util.UUID;

/** Incident : niveau, début, accusé de réception, responsable et résolution (§5). */
@Entity
@Table(name = "health_incident")
public class HealthIncident extends PanacheEntityBase {
    @Id public UUID id = UUID.randomUUID();
    @Column(name = "tenant_id") public UUID tenantId;
    @Column(name = "site_id") public UUID siteId;
    @Column(name = "topology_id") public UUID topologyId;
    @Column(name = "node_id") public UUID nodeId;
    public String type;
    @Column(name = "dedup_key") public String dedupKey;
    @Enumerated(EnumType.STRING) public Severity severity;
    @Enumerated(EnumType.STRING) public IncidentStatus status = IncidentStatus.OPEN;
    public String message;
    public String details;
    @Column(name = "opened_at") public Instant openedAt;
    @Column(name = "last_seen_at") public Instant lastSeenAt;
    @Column(name = "acknowledged_at") public Instant acknowledgedAt;
    @Column(name = "acknowledged_by") public String acknowledgedBy;
    public String assignee;
    @Column(name = "resolved_at") public Instant resolvedAt;
}
