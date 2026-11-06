package com.prestige.replica.domain;

import io.quarkus.hibernate.orm.panache.PanacheEntityBase;
import jakarta.persistence.*;
import java.time.Instant;
import java.util.UUID;

/** Destinataire de notifications. SMS/WhatsApp passent par un webhook (connecteur remplaçable). */
@Entity
@Table(name = "notification_channel")
public class NotificationChannel extends PanacheEntityBase {
    public enum Type { EMAIL, WEBHOOK }

    @Id public UUID id = UUID.randomUUID();
    @Column(name = "tenant_id") public UUID tenantId;
    @Column(name = "site_id") public UUID siteId;
    @Enumerated(EnumType.STRING) public Type type;
    public String target;
    @Enumerated(EnumType.STRING) @Column(name = "min_severity") public Severity minSeverity = Severity.WARNING;
    public boolean enabled = true;
    @Column(name = "created_at") public Instant createdAt = Instant.now();
}
