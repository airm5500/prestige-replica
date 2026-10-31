package com.prestige.replica.alert;

import com.prestige.replica.domain.NodeRole;
import com.prestige.replica.domain.Severity;
import java.time.Duration;
import java.time.Instant;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;

/**
 * Règles d'alerte du Lot 1 (§5 « Surveillance et alertes »). Fonction pure :
 * mêmes entrées, mêmes constats. Aucune règle ne déclenche d'action sur MariaDB.
 */
public final class AlertRules {

    /** Seuils issus de la topologie. */
    public record Thresholds(int lagWarnSeconds, int lagCritSeconds, int diskWarnPct, int diskCritPct, int agentSilenceSeconds) {
        public static final Thresholds DEFAULT = new Thresholds(60, 300, 20, 10, 90);
    }

    /** Erreurs IO MariaDB signalant des binlogs purgés ou une position GTID introuvable. */
    private static final java.util.Set<Long> BINLOG_GONE_ERRNOS = java.util.Set.of(1236L);

    private AlertRules() {}

    public static List<Finding> evaluate(NodeRole role, AgentReport r, Instant lastSeenAt, Instant now, Thresholds t) {
        List<Finding> out = new ArrayList<>();
        if (lastSeenAt == null) {
            out.add(Finding.of("AGENT_NEVER_SEEN", Severity.WARNING, "L'agent ne s'est encore jamais connecté"));
            return out;
        }
        long silence = Duration.between(lastSeenAt, now).toSeconds();
        if (silence > t.agentSilenceSeconds()) {
            out.add(new Finding("AGENT_SILENT", "", Severity.CRITICAL,
                    "Agent silencieux depuis " + human(silence) + " (site déconnecté ou agent arrêté)", null));
            // Le dernier rapport est périmé : on ne réévalue pas son contenu.
            return out;
        }
        if (r == null) {
            return out;
        }
        if (!r.reachable()) {
            out.add(new Finding("MARIADB_UNREACHABLE", "", Severity.CRITICAL, "MariaDB injoignable depuis l'agent local",
                    String.join("\n", r.errorsOrEmpty())));
            evaluateDisks(r, t, out);
            return out;
        }
        if (!r.errorsOrEmpty().isEmpty()) {
            out.add(new Finding("COLLECT_ERROR", "", Severity.WARNING, "Collecte partielle : " + r.errorsOrEmpty().get(0),
                    String.join("\n", r.errorsOrEmpty())));
        }
        AgentReport.Server s = r.server();
        boolean replicaRole = role == NodeRole.REPLICA || role == NodeRole.DELAYED_REPLICA;

        if (s != null) {
            if (role == NodeRole.PRIMARY && !s.logBin()) {
                out.add(Finding.of("BINLOG_DISABLED", Severity.CRITICAL, "Journal binaire désactivé sur le primaire : aucune réplique ni PITR possible"));
            }
            if (role == NodeRole.PRIMARY && s.readOnly()) {
                out.add(Finding.of("PRIMARY_READ_ONLY", Severity.WARNING, "Le primaire est en read_only : l'application ne peut pas écrire"));
            }
            if (replicaRole && !s.readOnly()) {
                out.add(Finding.of("REPLICA_WRITABLE", Severity.WARNING, "Réplique accessible en écriture (read_only=OFF) : risque de divergence"));
            }
            if (s.logBin() && s.binlogFormat() != null && !"ROW".equalsIgnoreCase(s.binlogFormat())) {
                out.add(Finding.of("BINLOG_FORMAT", Severity.INFO, "binlog_format=" + s.binlogFormat() + " : ROW est recommandé pour la réplication, l'audit et le flashback"));
            }
            if (s.serverId() == 0 || (s.serverId() == 1 && replicaRole)) {
                out.add(Finding.of("SERVER_ID", Severity.WARNING, "server_id=" + s.serverId() + " : valeur par défaut, à rendre unique dans la topologie"));
            }
            if (s.logBin() && s.binlogExpireSeconds() > 0 && s.binlogExpireSeconds() < 86400) {
                out.add(Finding.of("BINLOG_RETENTION", Severity.WARNING, "Rétention des binlogs inférieure à 24 h : une coupure prolongée imposera une reconstruction"));
            }
        }

        if (replicaRole && r.channelsOrEmpty().isEmpty()) {
            out.add(Finding.of("REPLICA_NO_CHANNEL", Severity.CRITICAL, "Nœud déclaré réplique sans canal de réplication configuré"));
        }
        if (role == NodeRole.PRIMARY && !r.channelsOrEmpty().isEmpty()) {
            out.add(Finding.of("PRIMARY_HAS_CHANNEL", Severity.INFO, "Le primaire possède un canal de réplication entrant (topologie circulaire ou relais ?)"));
        }

        for (AgentReport.Channel c : r.channelsOrEmpty()) {
            String key = c.label();
            String where = "canal " + key + " ← " + c.masterHost() + ":" + c.masterPort();
            if (c.lastIoErrno() != 0 && BINLOG_GONE_ERRNOS.contains(c.lastIoErrno())) {
                out.add(new Finding("BINLOG_PURGED", key, Severity.CRITICAL,
                        "Binlogs nécessaires introuvables sur la source (" + where + ") : reconstruction requise",
                        "Erreur IO " + c.lastIoErrno() + " : " + c.lastIoError()));
            } else if (c.lastIoErrno() != 0) {
                out.add(new Finding("REPLICATION_IO_ERROR", key, Severity.CRITICAL,
                        "Erreur IO " + c.lastIoErrno() + " sur le " + where, c.lastIoError()));
            }
            if (c.lastSqlErrno() != 0) {
                out.add(new Finding("REPLICATION_SQL_ERROR", key, Severity.CRITICAL,
                        "Erreur SQL " + c.lastSqlErrno() + " sur le " + where, c.lastSqlError()));
            }
            if (!c.ioUp()) {
                out.add(new Finding("IO_THREAD_STOPPED", key, Severity.CRITICAL,
                        "Thread IO " + (c.ioRunning() == null ? "inconnu" : c.ioRunning()) + " sur le " + where, null));
            }
            if (!c.sqlUp()) {
                out.add(new Finding("SQL_THREAD_STOPPED", key, Severity.CRITICAL,
                        "Thread SQL arrêté sur le " + where, c.slaveSqlRunningState()));
            }
            if (c.ioUp() && c.sqlUp() && c.secondsBehindMaster() != null) {
                long lag = effectiveLag(role, c);
                if (lag >= t.lagCritSeconds()) {
                    out.add(new Finding("REPLICATION_LAG", key, Severity.CRITICAL, "Retard de " + human(lag) + " sur le " + where, null));
                } else if (lag >= t.lagWarnSeconds()) {
                    out.add(new Finding("REPLICATION_LAG", key, Severity.WARNING, "Retard de " + human(lag) + " sur le " + where, null));
                }
            }
            if (c.usingGtid() != null && "No".equalsIgnoreCase(c.usingGtid())) {
                out.add(new Finding("GTID_NOT_USED", key, Severity.WARNING,
                        "Le " + where + " utilise des positions fichier/offset et non GTID (MASTER_USE_GTID=slave_pos recommandé)", null));
            }
            if (c.hasFilters()) {
                out.add(new Finding("REPLICATION_FILTERS", key, Severity.INFO,
                        "Filtres de réplication actifs sur le " + where + " : la réplique n'est pas une copie complète", null));
            }
            if (role == NodeRole.DELAYED_REPLICA && c.sqlDelay() == 0) {
                out.add(new Finding("DELAY_MISSING", key, Severity.WARNING,
                        "Réplique différée sans MASTER_DELAY sur le " + where + " : aucune fenêtre de protection logique", null));
            }
        }
        evaluateDisks(r, t, out);
        return out;
    }

    /** Pour une réplique différée, on retranche le délai volontaire. */
    static long effectiveLag(NodeRole role, AgentReport.Channel c) {
        long lag = c.secondsBehindMaster() == null ? 0 : c.secondsBehindMaster();
        if (role == NodeRole.DELAYED_REPLICA && c.sqlDelay() > 0) {
            lag = Math.max(0, lag - c.sqlDelay());
        }
        return lag;
    }

    private static void evaluateDisks(AgentReport r, Thresholds t, List<Finding> out) {
        for (AgentReport.Disk d : r.disksOrEmpty()) {
            double pct = d.freePct();
            String msg = String.format(java.util.Locale.FRANCE, "Espace libre %.1f %% sur %s", pct, d.path());
            if (pct < t.diskCritPct()) {
                out.add(new Finding("DISK_LOW", d.path(), Severity.CRITICAL, msg, null));
            } else if (pct < t.diskWarnPct()) {
                out.add(new Finding("DISK_LOW", d.path(), Severity.WARNING, msg, null));
            }
        }
    }

    /** Contrôles portant sur l'ensemble d'une topologie. */
    public static List<Finding> evaluateTopology(Map<String, Long> serverIdsByNodeName) {
        List<Finding> out = new ArrayList<>();
        Map<Long, List<String>> byId = new java.util.TreeMap<>();
        serverIdsByNodeName.forEach((name, id) -> {
            if (id != null && id > 0) byId.computeIfAbsent(id, k -> new ArrayList<>()).add(name);
        });
        byId.forEach((id, names) -> {
            if (names.size() > 1) {
                java.util.Collections.sort(names);
                out.add(new Finding("DUPLICATE_SERVER_ID", String.valueOf(id), Severity.CRITICAL,
                        "server_id=" + id + " partagé par " + String.join(", ", names), null));
            }
        });
        return out;
    }

    static String human(long seconds) {
        if (seconds < 120) return seconds + " s";
        if (seconds < 7200) return (seconds / 60) + " min";
        return (seconds / 3600) + " h " + ((seconds % 3600) / 60) + " min";
    }
}
