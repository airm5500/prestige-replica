package com.prestige.replica.domain;

import io.quarkus.hibernate.orm.panache.PanacheEntityBase;
import io.quarkus.security.jpa.Password;
import io.quarkus.security.jpa.Roles;
import io.quarkus.security.jpa.UserDefinition;
import io.quarkus.security.jpa.Username;
import jakarta.persistence.*;
import java.time.Instant;
import java.util.UUID;

/**
 * Utilisateur de la console. tenantId nul = super-administrateur du parc ;
 * siteId renseigné = administrateur limité à une pharmacie.
 */
@Entity
@Table(name = "app_user")
@UserDefinition
public class AppUser extends PanacheEntityBase {
    @Id public UUID id = UUID.randomUUID();
    @Username public String username;
    /** Empreinte bcrypt. */
    @Password @Column(name = "password_hash") public String passwordHash;
    @Roles public String role;
    @Column(name = "tenant_id") public UUID tenantId;
    @Column(name = "site_id") public UUID siteId;
    @Column(name = "display_name") public String displayName;
    public boolean enabled = true;
    @Column(name = "created_at") public Instant createdAt = Instant.now();

    public static AppUser findByUsername(String username) {
        return find("username", username).firstResult();
    }
}
