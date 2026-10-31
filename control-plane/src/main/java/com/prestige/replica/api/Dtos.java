package com.prestige.replica.api;

import com.prestige.replica.alert.AgentReport;
import com.prestige.replica.domain.*;
import jakarta.validation.Valid;
import jakarta.validation.constraints.*;
import java.time.Instant;
import java.util.List;
import java.util.UUID;

/** Objets d'échange de l'API REST v1. */
public final class Dtos {
    private Dtos() {}

    public record ReportBatch(@NotNull @Size(max = 500) List<@Valid AgentReport> reports) {}
    public record IngestResult(int accepted, Instant serverTime) {}

    public record TenantIn(@NotBlank @Pattern(regexp = "[A-Za-z0-9_-]{2,32}") String code, @NotBlank @Size(max = 200) String name) {}
    public record SiteIn(@NotNull UUID tenantId, @NotBlank @Pattern(regexp = "[A-Za-z0-9_-]{2,16}") String code,
                         @NotBlank @Size(max = 200) String name, @Size(max = 32) String environment,
                         @Pattern(regexp = "#[0-9a-fA-F]{6}") String color, @Size(max = 64) String timezone) {}
    public record TopologyIn(@NotNull UUID siteId, @NotBlank @Size(max = 120) String name,
                             @Pattern(regexp = "[ABCD]") String mode,
                             @Min(1) Integer lagWarnSeconds, @Min(1) Integer lagCritSeconds,
                             @Min(1) @Max(99) Integer diskWarnPct, @Min(1) @Max(99) Integer diskCritPct,
                             @Min(30) Integer agentSilenceSeconds, Boolean maintenance) {}
    public record NodeIn(@NotNull UUID siteId, UUID topologyId, @NotBlank @Size(max = 120) String name,
                         @NotNull NodeRole expectedRole, @Size(max = 200) String addressLabel) {}
    /** Le jeton n'est renvoyé qu'une seule fois, à la création ou à la rotation. */
    public record NodeRegistered(UUID nodeId, String agentToken, String warning) {}

    public record UserIn(@NotBlank @Pattern(regexp = "[A-Za-z0-9._@-]{3,100}") String username,
                         @NotBlank @Size(min = 12, max = 200) String password, @NotBlank String role,
                         UUID tenantId, UUID siteId, @Size(max = 200) String displayName) {}
    public record UserOut(UUID id, String username, String role, UUID tenantId, UUID siteId, String displayName, boolean enabled) {
        static UserOut of(AppUser u) { return new UserOut(u.id, u.username, u.role, u.tenantId, u.siteId, u.displayName, u.enabled); }
    }

    public record ChannelIn(@NotNull UUID tenantId, UUID siteId, @NotNull NotificationChannel.Type type,
                            @NotBlank @Size(max = 500) String target, Severity minSeverity) {}
    public record AckIn(@Size(max = 100) String assignee) {}

    public record ChannelView(String name, String source, String ioRunning, String sqlRunning, Long lagSeconds,
                              long sqlDelay, String usingGtid, String gtidIoPos, long lastIoErrno, String lastIoError,
                              long lastSqlErrno, String lastSqlError, long relayLogBytes, String sqlState) {
        static ChannelView of(AgentReport.Channel c) {
            return new ChannelView(c.label(), c.masterHost() + ":" + c.masterPort(), c.ioRunning(), c.sqlRunning(),
                    c.secondsBehindMaster(), c.sqlDelay(), c.usingGtid(), c.gtidIoPos(), c.lastIoErrno(), c.lastIoError(),
                    c.lastSqlErrno(), c.lastSqlError(), c.relayLogSpace(), c.slaveSqlRunningState());
        }
    }

    public record NodeSummary(UUID id, String name, NodeRole expectedRole, UUID topologyId, UUID siteId, String addressLabel,
                              String status, Instant lastSeenAt, Instant lastReportAt, boolean reachable, String version,
                              Long serverId, Boolean readOnly, String gtidCurrentPos, String gtidSlavePos,
                              List<ChannelView> channels, Double minDiskFreePct, String agentVersion, String agentTokenPrefix,
                              long openIncidents, String worstSeverity, List<String> capabilities,
                              Instant replicationPausedAt, String replicationPausedBy, String replicationPauseReason,
                              boolean fenceRequired, String fenceReason, com.fasterxml.jackson.databind.JsonNode router) {}

    public record TopologySummary(UUID id, String name, String mode, boolean maintenance, String status,
                                  String primary, Long rpoSecondsEstimate, List<NodeSummary> nodes) {}

    public record SiteSummary(UUID id, UUID tenantId, String code, String name, String environment, String color,
                              String status, long openIncidents, long criticalIncidents, List<TopologySummary> topologies,
                              List<NodeSummary> unassignedNodes) {}
}
