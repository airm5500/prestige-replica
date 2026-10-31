package com.prestige.replica.domain;

import io.quarkus.hibernate.orm.panache.PanacheEntityBase;
import jakarta.persistence.*;
import java.time.Instant;
import java.util.UUID;
import org.hibernate.annotations.JdbcTypeCode;
import org.hibernate.type.SqlTypes;

/** Serveur MariaDB inventorié et son agent local. */
@Entity
@Table(name = "database_node")
public class DatabaseNode extends PanacheEntityBase {
    @Id public UUID id = UUID.randomUUID();
    @Column(name = "tenant_id") public UUID tenantId;
    @Column(name = "site_id") public UUID siteId;
    @Column(name = "topology_id") public UUID topologyId;
    public String name;
    @Enumerated(EnumType.STRING) @Column(name = "expected_role") public NodeRole expectedRole;
    @Column(name = "address_label") public String addressLabel;
    /** SHA-256 du jeton d'agent ; le jeton lui-même n'est jamais stocké. */
    @Column(name = "agent_token_hash") public String agentTokenHash;
    @Column(name = "agent_token_prefix") public String agentTokenPrefix;
    @Column(name = "agent_version") public String agentVersion;
    @Column(name = "agent_hostname") public String agentHostname;
    @Column(name = "agent_os") public String agentOs;
    @Column(name = "last_seen_at") public Instant lastSeenAt;
    @Column(name = "last_report_at") public Instant lastReportAt;
    @JdbcTypeCode(SqlTypes.JSON) @Column(name = "last_report") public String lastReport;
    @Column(name = "created_at") public Instant createdAt = Instant.now();
}
