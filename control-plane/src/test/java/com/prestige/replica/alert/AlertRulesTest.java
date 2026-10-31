package com.prestige.replica.alert;

import static org.junit.jupiter.api.Assertions.*;

import com.prestige.replica.domain.NodeRole;
import com.prestige.replica.domain.Severity;
import java.time.Instant;
import java.util.List;
import java.util.Map;
import org.junit.jupiter.api.Test;

class AlertRulesTest {
    static final Instant NOW = Instant.parse("2026-10-31T12:00:00Z");
    static final AlertRules.Thresholds T = AlertRules.Thresholds.DEFAULT;

    static AgentReport.Server server(boolean readOnly, boolean logBin, String format, long serverId) {
        return new AgentReport.Server("10.11.14-MariaDB", "", serverId, readOnly, logBin, true, format, "FULL", 0, true,
                "0-1-10", "0-1-10", "0-1-10", 604800, 1, 1, false, false, "SYSTEM", "UTC", "/var/lib/mysql", "YES",
                100, 5, 151, 1000, 1000, List.of());
    }

    static AgentReport.Channel channel(String io, String sql, Long lag, long ioErr, long sqlErr, long delay, String gtid) {
        return new AgentReport.Channel("", "10.0.0.1", 3306, 1, io, sql, lag, delay, null, ioErr, ioErr == 0 ? "" : "err",
                sqlErr, sqlErr == 0 ? "" : "Duplicate entry", gtid, "0-1-10", "binlog.000001", 100, "binlog.000001", 100,
                4096, "waiting", "No", "", "", "", "", "", "", 0);
    }

    static AgentReport report(AgentReport.Server s, List<AgentReport.Channel> ch, List<AgentReport.Disk> disks) {
        return new AgentReport(1, "0.1.0", NOW, "h", "linux", "amd64", List.of(), true, s, null, ch, disks, List.of());
    }

    static List<String> types(List<Finding> f) { return f.stream().map(Finding::type).toList(); }

    @Test
    void healthyReplicaHasNoFinding() {
        var r = report(server(true, true, "ROW", 2), List.of(channel("Yes", "Yes", 0L, 0, 0, 0, "Slave_Pos")), List.of(new AgentReport.Disk("/", 100, 50)));
        assertEquals(List.of(), AlertRules.evaluate(NodeRole.REPLICA, r, NOW, NOW, T));
    }

    @Test
    void stoppedSqlThreadIsCritical() {
        var r = report(server(true, true, "ROW", 2), List.of(channel("Yes", "No", null, 0, 1062, 0, "Slave_Pos")), List.of());
        var f = AlertRules.evaluate(NodeRole.REPLICA, r, NOW, NOW, T);
        assertTrue(types(f).containsAll(List.of("SQL_THREAD_STOPPED", "REPLICATION_SQL_ERROR")));
        assertTrue(f.stream().allMatch(x -> x.severity() == Severity.CRITICAL));
    }

    @Test
    void binlogPurgedDetected() {
        var r = report(server(true, true, "ROW", 2), List.of(channel("No", "Yes", null, 1236, 0, 0, "Slave_Pos")), List.of());
        var f = types(AlertRules.evaluate(NodeRole.REPLICA, r, NOW, NOW, T));
        assertTrue(f.contains("BINLOG_PURGED"));
        assertFalse(f.contains("REPLICATION_IO_ERROR"));
    }

    @Test
    void lagThresholds() {
        var warn = report(server(true, true, "ROW", 2), List.of(channel("Yes", "Yes", 90L, 0, 0, 0, "Slave_Pos")), List.of());
        var crit = report(server(true, true, "ROW", 2), List.of(channel("Yes", "Yes", 400L, 0, 0, 0, "Slave_Pos")), List.of());
        assertEquals(Severity.WARNING, AlertRules.evaluate(NodeRole.REPLICA, warn, NOW, NOW, T).get(0).severity());
        assertEquals(Severity.CRITICAL, AlertRules.evaluate(NodeRole.REPLICA, crit, NOW, NOW, T).get(0).severity());
    }

    @Test
    void delayedReplicaSubtractsIntendedDelay() {
        var r = report(server(true, true, "ROW", 3), List.of(channel("Yes", "Yes", 21_630L, 0, 0, 21_600, "Slave_Pos")), List.of());
        assertEquals(List.of(), AlertRules.evaluate(NodeRole.DELAYED_REPLICA, r, NOW, NOW, T));
        var noDelay = report(server(true, true, "ROW", 3), List.of(channel("Yes", "Yes", 0L, 0, 0, 0, "Slave_Pos")), List.of());
        assertEquals(List.of("DELAY_MISSING"), types(AlertRules.evaluate(NodeRole.DELAYED_REPLICA, noDelay, NOW, NOW, T)));
    }

    @Test
    void silentAgentSuppressesStaleFindings() {
        var r = report(server(false, true, "ROW", 2), List.of(channel("No", "No", null, 0, 0, 0, "Slave_Pos")), List.of());
        var f = AlertRules.evaluate(NodeRole.REPLICA, r, NOW.minusSeconds(600), NOW, T);
        assertEquals(List.of("AGENT_SILENT"), types(f));
    }

    @Test
    void primaryChecks() {
        var r = report(server(true, false, "MIXED", 1), List.of(), List.of(new AgentReport.Disk("/data", 100, 5)));
        var f = types(AlertRules.evaluate(NodeRole.PRIMARY, r, NOW, NOW, T));
        assertTrue(f.containsAll(List.of("BINLOG_DISABLED", "PRIMARY_READ_ONLY", "DISK_LOW")));
        assertFalse(f.contains("BINLOG_FORMAT"), "format sans objet si binlog désactivé");
    }

    @Test
    void replicaChecks() {
        var r = report(server(false, true, "ROW", 0), List.of(), List.of());
        var f = types(AlertRules.evaluate(NodeRole.REPLICA, r, NOW, NOW, T));
        assertTrue(f.containsAll(List.of("REPLICA_WRITABLE", "REPLICA_NO_CHANNEL", "SERVER_ID")));
    }

    @Test
    void unreachableMariaDb() {
        var r = new AgentReport(1, "0.1.0", NOW, "h", "linux", "amd64", List.of(), false, null, null, List.of(), List.of(), List.of("connexion refusée"));
        assertEquals(List.of("MARIADB_UNREACHABLE"), types(AlertRules.evaluate(NodeRole.PRIMARY, r, NOW, NOW, T)));
    }

    @Test
    void duplicateServerIds() {
        var f = AlertRules.evaluateTopology(Map.of("a", 1L, "b", 1L, "c", 2L));
        assertEquals(1, f.size());
        assertTrue(f.get(0).message().contains("a, b"));
    }
}
