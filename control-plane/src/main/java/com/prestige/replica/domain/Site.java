package com.prestige.replica.domain;

import io.quarkus.hibernate.orm.panache.PanacheEntityBase;
import jakarta.persistence.*;
import java.time.Instant;
import java.util.UUID;

/** Pharmacie ou emplacement physique/cloud. */
@Entity
@Table(name = "site")
public class Site extends PanacheEntityBase {
    @Id public UUID id = UUID.randomUUID();
    @Column(name = "tenant_id") public UUID tenantId;
    /** Code court ressaisi pour confirmer les actions critiques. */
    public String code;
    public String name;
    public String environment = "production";
    public String color = "#2563eb";
    public String timezone = "UTC";
    @Column(name = "created_at") public Instant createdAt = Instant.now();
}
