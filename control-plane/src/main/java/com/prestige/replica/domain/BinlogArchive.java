package com.prestige.replica.domain;

import io.quarkus.hibernate.orm.panache.PanacheEntityBase;
import jakarta.persistence.*;
import java.time.Instant;
import java.util.UUID;

/** Binlog archivé (chiffré dans le stockage du site ; seules les métadonnées sont ici). */
@Entity
@Table(name = "binlog_archive")
public class BinlogArchive extends PanacheEntityBase {
    @Id public UUID id = UUID.randomUUID();
    @Column(name = "tenant_id") public UUID tenantId;
    @Column(name = "site_id") public UUID siteId;
    @Column(name = "topology_id") public UUID topologyId;
    @Column(name = "server_id") public long serverId;
    @Column(name = "file_name") public String fileName;
    @Column(name = "relative_path") public String relativePath;
    @Column(name = "size_bytes") public Long sizeBytes;
    public String sha256;
    @Column(name = "first_gtid") public String firstGtid;
    @Column(name = "last_gtid") public String lastGtid;
    @Column(name = "first_event_at") public Instant firstEventAt;
    @Column(name = "last_event_at") public Instant lastEventAt;
    public boolean complete;
    @Column(name = "archived_at") public Instant archivedAt;
}
