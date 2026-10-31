package com.prestige.replica.alert;

import com.fasterxml.jackson.annotation.JsonIgnoreProperties;
import java.time.Instant;
import java.util.List;

/**
 * Contrat JSON émis par l'agent Go (agent/internal/report). Les champs inconnus
 * sont ignorés pour rester compatible avec une version d'agent plus récente.
 */
@JsonIgnoreProperties(ignoreUnknown = true)
public record AgentReport(
        int schemaVersion,
        String agentVersion,
        Instant collectedAt,
        String hostname,
        String os,
        String arch,
        List<String> capabilities,
        boolean reachable,
        Server server,
        Binlogs binlogs,
        List<Channel> channels,
        List<Disk> disks,
        List<String> errors,
        /** État du routeur de connexion embarqué (Lot 4), absent s'il n'est pas activé. */
        com.fasterxml.jackson.databind.JsonNode router) {

    public List<Channel> channelsOrEmpty() { return channels == null ? List.of() : channels; }
    public List<Disk> disksOrEmpty() { return disks == null ? List.of() : disks; }
    public List<String> capabilitiesOrEmpty() { return capabilities == null ? List.of() : capabilities; }
    public List<String> errorsOrEmpty() { return errors == null ? List.of() : errors; }

    @JsonIgnoreProperties(ignoreUnknown = true)
    public record Server(
            String version, String versionComment, long serverId, boolean readOnly, boolean logBin,
            boolean logSlaveUpdates, String binlogFormat, String binlogRowImage, long gtidDomainId,
            boolean gtidStrictMode, String gtidCurrentPos, String gtidBinlogPos, String gtidSlavePos,
            long binlogExpireSeconds, long syncBinlog, long innodbFlushLogAtTrxCommit,
            boolean semiSyncMasterEnabled, boolean semiSyncSlaveEnabled, String timeZone,
            String systemTimeZone, String dataDir, String haveSsl, long uptimeSeconds,
            long threadsConnected, long maxConnections, long questions, long binlogBytesWritten,
            List<String> databases) {}

    @JsonIgnoreProperties(ignoreUnknown = true)
    public record Binlogs(int count, long totalBytes, String first, String last) {}

    @JsonIgnoreProperties(ignoreUnknown = true)
    public record Channel(
            String connectionName, String masterHost, long masterPort, long masterServerId,
            String ioRunning, String sqlRunning, Long secondsBehindMaster, long sqlDelay,
            Long sqlRemainingDelay, long lastIoErrno, String lastIoError, long lastSqlErrno,
            String lastSqlError, String usingGtid, String gtidIoPos, String masterLogFile,
            long readMasterLogPos, String relayMasterLogFile, long execMasterLogPos, long relayLogSpace,
            String slaveSqlRunningState, String masterSslAllowed, String replicateDoDb,
            String replicateIgnoreDb, String replicateDoTable, String replicateIgnoreTable,
            String replicateWildDoTable, String replicateWildIgnoreTable, long retriedTransactions) {

        public boolean ioUp() { return "Yes".equalsIgnoreCase(ioRunning); }
        public boolean sqlUp() { return "Yes".equalsIgnoreCase(sqlRunning); }
        public String label() { return connectionName == null || connectionName.isBlank() ? "(défaut)" : connectionName; }
        public boolean hasFilters() {
            return notBlank(replicateDoDb) || notBlank(replicateIgnoreDb) || notBlank(replicateDoTable)
                    || notBlank(replicateIgnoreTable) || notBlank(replicateWildDoTable) || notBlank(replicateWildIgnoreTable);
        }
        private static boolean notBlank(String s) { return s != null && !s.isBlank(); }
    }

    @JsonIgnoreProperties(ignoreUnknown = true)
    public record Disk(String path, long totalBytes, long freeBytes) {
        public double freePct() { return totalBytes <= 0 ? 100.0 : 100.0 * freeBytes / totalBytes; }
    }
}
