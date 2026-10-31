package com.prestige.replica.domain;

import io.quarkus.hibernate.orm.panache.PanacheEntityBase;
import jakarta.persistence.*;
import java.time.Instant;
import java.util.UUID;

/**
 * Entrée du catalogue. Le fichier reste dans le stockage du site ; la console
 * ne conserve que ses métadonnées (§19.3), jamais la clé de chiffrement.
 */
@Entity
@Table(name = "backup")
public class Backup extends PanacheEntityBase {
    public enum Status { RUNNING, COMPLETED, FAILED, DELETED }
    public enum Verify { NOT_VERIFIED, VERIFIED, FAILED }

    @Id public UUID id = UUID.randomUUID();
    @Column(name = "tenant_id") public UUID tenantId;
    @Column(name = "site_id") public UUID siteId;
    @Column(name = "topology_id") public UUID topologyId;
    @Column(name = "node_id") public UUID nodeId;
    @Column(name = "operation_id") public UUID operationId;
    public String kind = "FULL";
    @Column(name = "relative_dir") public String relativeDir;
    @Enumerated(EnumType.STRING) public Status status;
    @Column(name = "started_at") public Instant startedAt;
    @Column(name = "finished_at") public Instant finishedAt;
    @Column(name = "size_bytes") public Long sizeBytes;
    public String sha256;
    @Column(name = "key_id") public String keyId;
    public String gtid;
    @Column(name = "server_version") public String serverVersion;
    @Enumerated(EnumType.STRING) @Column(name = "verify_status") public Verify verifyStatus = Verify.NOT_VERIFIED;
    @Column(name = "verified_at") public Instant verifiedAt;
    @Column(name = "restore_seconds") public Integer restoreSeconds;
}
