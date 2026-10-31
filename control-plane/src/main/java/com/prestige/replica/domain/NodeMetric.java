package com.prestige.replica.domain;

import io.quarkus.hibernate.orm.panache.PanacheEntityBase;
import jakarta.persistence.*;
import java.time.Instant;
import java.util.UUID;

/** Métrique horodatée extraite d'un rapport d'agent. */
@Entity
@Table(name = "node_metric")
public class NodeMetric extends PanacheEntityBase {
    @Id @GeneratedValue(strategy = GenerationType.IDENTITY) public Long id;
    @Column(name = "tenant_id") public UUID tenantId;
    @Column(name = "node_id") public UUID nodeId;
    @Column(name = "collected_at") public Instant collectedAt;
    @Column(name = "received_at") public Instant receivedAt = Instant.now();
    public boolean reachable;
    @Column(name = "lag_seconds") public Long lagSeconds;
    @Column(name = "io_running") public Boolean ioRunning;
    @Column(name = "sql_running") public Boolean sqlRunning;
    @Column(name = "binlog_bytes") public Long binlogBytes;
    @Column(name = "relay_log_bytes") public Long relayLogBytes;
    @Column(name = "min_disk_free_pct") public Double minDiskFreePct;
    @Column(name = "threads_connected") public Long threadsConnected;
    public Long questions;
    @Column(name = "binlog_bytes_written") public Long binlogBytesWritten;
    @Column(name = "gtid_current_pos") public String gtidCurrentPos;
}
