package com.prestige.replica.domain;

import io.quarkus.hibernate.orm.panache.PanacheEntityBase;
import jakarta.persistence.*;
import java.time.Instant;
import java.util.UUID;
import org.hibernate.annotations.JdbcTypeCode;
import org.hibernate.type.SqlTypes;

/** Workflow demandé : planifier → approuver → exécuter → vérifier → auditer (§10). */
@Entity
@Table(name = "operation")
public class Operation extends PanacheEntityBase {
    @Id public UUID id = UUID.randomUUID();
    @Column(name = "tenant_id") public UUID tenantId;
    @Column(name = "site_id") public UUID siteId;
    @Column(name = "topology_id") public UUID topologyId;
    /** Nœud dont l'agent exécute l'opération. */
    @Column(name = "node_id") public UUID nodeId;
    @Enumerated(EnumType.STRING) public OperationType type;
    @Enumerated(EnumType.STRING) public OperationStatus status;
    @JdbcTypeCode(SqlTypes.JSON) public String params;
    public String summary;
    @Column(name = "requires_approval") public boolean requiresApproval;
    @Column(name = "requested_by") public String requestedBy;
    @Column(name = "requested_at") public Instant requestedAt;
    @Column(name = "approved_by") public String approvedBy;
    @Column(name = "approved_at") public Instant approvedAt;
    @Column(name = "started_at") public Instant startedAt;
    @Column(name = "finished_at") public Instant finishedAt;
    @Column(name = "last_event_at") public Instant lastEventAt;
    public String error;
    @JdbcTypeCode(SqlTypes.JSON) public String result;
    /** Opération parente (étape d'une bascule). */
    @Column(name = "parent_id") public UUID parentId;
}
