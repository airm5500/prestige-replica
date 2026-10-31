package com.prestige.replica.domain;

import io.quarkus.hibernate.orm.panache.PanacheEntityBase;
import jakarta.persistence.*;
import java.time.Instant;
import java.util.UUID;

/** Entrée du journal d'audit append-only, chaînée par empreinte. */
@Entity
@Table(name = "audit_event")
public class AuditEvent extends PanacheEntityBase {
    @Id @GeneratedValue(strategy = GenerationType.IDENTITY) public Long id;
    public Instant at;
    @Column(name = "tenant_id") public UUID tenantId;
    @Column(name = "site_id") public UUID siteId;
    public String actor;
    public String action;
    public String resource;
    public String result;
    public String details;
    @Column(name = "prev_hash") public String prevHash;
    public String hash;
}
