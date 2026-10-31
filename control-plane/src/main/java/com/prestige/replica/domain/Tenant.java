package com.prestige.replica.domain;

import io.quarkus.hibernate.orm.panache.PanacheEntityBase;
import jakarta.persistence.*;
import java.time.Instant;
import java.util.UUID;

/** Client ou organisation : frontière d'isolation stricte (§19.2). */
@Entity
@Table(name = "tenant")
public class Tenant extends PanacheEntityBase {
    @Id public UUID id = UUID.randomUUID();
    public String code;
    public String name;
    @Column(name = "created_at") public Instant createdAt = Instant.now();
}
