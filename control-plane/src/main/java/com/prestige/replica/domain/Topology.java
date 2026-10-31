package com.prestige.replica.domain;

import io.quarkus.hibernate.orm.panache.PanacheEntityBase;
import jakarta.persistence.*;
import java.time.Instant;
import java.util.UUID;

/** Groupe primaire/répliques et seuils d'alerte associés. */
@Entity
@Table(name = "replication_topology")
public class Topology extends PanacheEntityBase {
    @Id public UUID id = UUID.randomUUID();
    @Column(name = "tenant_id") public UUID tenantId;
    @Column(name = "site_id") public UUID siteId;
    public String name;
    /** Mode de la spécification : A, B, C ou D. */
    public String mode = "A";
    public boolean maintenance;
    @Column(name = "lag_warn_seconds") public int lagWarnSeconds = 60;
    @Column(name = "lag_crit_seconds") public int lagCritSeconds = 300;
    @Column(name = "disk_warn_pct") public int diskWarnPct = 20;
    @Column(name = "disk_crit_pct") public int diskCritPct = 10;
    @Column(name = "agent_silence_seconds") public int agentSilenceSeconds = 90;
    @Column(name = "created_at") public Instant createdAt = Instant.now();
}
