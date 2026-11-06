package com.prestige.replica.domain;

import io.quarkus.hibernate.orm.panache.PanacheEntityBase;
import jakarta.persistence.*;
import java.time.Instant;
import java.util.UUID;
import org.hibernate.annotations.JdbcTypeCode;
import org.hibernate.type.SqlTypes;

/** Résultat d'une restauration vérifiée dans une instance isolée. */
@Entity
@Table(name = "restore_test")
public class RestoreTest extends PanacheEntityBase {
    @Id public UUID id = UUID.randomUUID();
    @Column(name = "tenant_id") public UUID tenantId;
    @Column(name = "backup_id") public UUID backupId;
    @Column(name = "operation_id") public UUID operationId;
    @Column(name = "node_id") public UUID nodeId;
    public String status;
    public Instant at;
    @Column(name = "total_seconds") public Integer totalSeconds;
    public String gtid;
    @JdbcTypeCode(SqlTypes.JSON) public String details;
}
