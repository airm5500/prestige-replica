package com.prestige.replica.service;

import com.fasterxml.jackson.databind.JsonNode;
import com.prestige.replica.alert.AgentReport;
import com.prestige.replica.alert.Finding;
import com.prestige.replica.domain.*;
import jakarta.enterprise.context.ApplicationScoped;
import jakarta.inject.Inject;
import jakarta.transaction.Transactional;
import jakarta.ws.rs.BadRequestException;
import java.time.Duration;
import java.time.Instant;
import java.time.ZoneId;
import java.time.ZonedDateTime;
import java.util.*;
import java.util.regex.Pattern;

/**
 * Contrôles de cohérence (§7) : chaque réplique compare son schéma et ses données à ceux du primaire,
 * par tranches de clé primaire et à débit limité. Les écarts sont signalés, jamais corrigés : la
 * proposition est de reconstruire la réplique depuis une sauvegarde vérifiée.
 */
@ApplicationScoped
public class ConsistencyService {
    @Inject OperationService ops;
    @Inject MonitoringService monitoring;

    static final Pattern DB = Pattern.compile("^[A-Za-z0-9_$]{1,64}$");
    static final Pattern EXCLUDE = Pattern.compile("^[A-Za-z0-9_$]{1,64}\\.([A-Za-z0-9_$]{1,64}|\\*)$");
    static final Pattern TABLE = Pattern.compile("^[A-Za-z0-9_$]{1,64}\\.[A-Za-z0-9_$]{1,64}$");

    public static List<String> split(String s) {
        if (s == null || s.isBlank()) return List.of();
        return Arrays.stream(s.split("[\\s,;]+")).map(String::trim).filter(x -> !x.isEmpty()).distinct().toList();
    }

    public static void validateLists(List<String> dbs, List<String> exclude) {
        for (String d : dbs) {
            if (!DB.matcher(d).matches() || Set.of("mysql", "information_schema", "performance_schema", "sys").contains(d.toLowerCase())) {
                throw new BadRequestException("Base invalide : « " + d + " »");
            }
        }
        for (String e : exclude) if (!EXCLUDE.matcher(e).matches()) throw new BadRequestException("Exclusion invalide : « " + e + " » (base.table ou base.*)");
    }

    @Transactional(Transactional.TxType.MANDATORY)
    public Operation request(DatabaseNode replica, String mode, String startAfter, String actor) {
        if (replica.topologyId == null) throw new BadRequestException("Serveur hors topologie");
        Topology t = Topology.findById(replica.topologyId);
        if (replica.expectedRole == NodeRole.PRIMARY) throw new BadRequestException("Le contrôle se lance sur une réplique : elle se compare au primaire");
        DatabaseNode primary = DatabaseNode.find("topologyId = ?1 and expectedRole = ?2", t.id, NodeRole.PRIMARY).firstResult();
        if (primary == null) throw new BadRequestException("Aucun primaire déclaré dans la topologie");
        AgentReport r = monitoring.parse(replica.lastReport);
        if (r == null || !r.capabilitiesOrEmpty().contains("consistencyCheck")) {
            throw new BadRequestException("Contrôles non activés sur " + replica.name + " (consistencyCheck dans agent.json)");
        }
        String m = mode == null || mode.isBlank() ? t.consistencyMode : mode;
        if (!m.equals("FULL") && !m.equals("SCHEMA")) throw new BadRequestException("Mode inconnu");
        if (startAfter != null && !startAfter.isBlank() && !TABLE.matcher(startAfter).matches()) throw new BadRequestException("Point de reprise invalide");
        String[] hp = BackupService.hostPort(primary.addressLabel);
        Map<String, Object> p = new LinkedHashMap<>();
        p.put("sourceHost", hp[0]);
        p.put("sourcePort", Integer.parseInt(hp[1]));
        p.put("mode", m);
        p.put("databases", split(t.consistencyDatabases));
        p.put("exclude", split(t.consistencyExclude));
        p.put("chunkRows", 1000);
        p.put("pauseFactor", 1.0);
        p.put("maxMinutes", 240);
        if (startAfter != null && !startAfter.isBlank()) p.put("startAfter", startAfter);
        String what = (m.equals("SCHEMA") ? "schéma" : "schéma et données") + (startAfter == null || startAfter.isBlank() ? "" : ", reprise après " + startAfter);
        return ops.request(OperationType.CONSISTENCY_CHECK, replica, p, "Cohérence de " + replica.name + " avec " + primary.name + " (" + what + ")", actor);
    }

    /** Contrôle hebdomadaire de chaque réplique, à l'heure choisie (heure de la pharmacie). */
    @Transactional
    public void schedule(Instant now) {
        for (Topology t : Topology.<Topology>list("consistencyEnabled = true and maintenance = false")) {
            if (ops.switchoverRunning(t.id)) continue;
            ZoneId zone = BackupService.zone(Site.findById(t.siteId));
            ZonedDateTime local = now.atZone(zone);
            if (local.getDayOfWeek().getValue() != t.consistencyWeekday || local.getHour() != t.consistencyHour) continue;
            Instant dayStart = local.toLocalDate().atStartOfDay(zone).toInstant();
            for (DatabaseNode n : DatabaseNode.<DatabaseNode>list("topologyId = ?1 and expectedRole <> ?2", t.id, NodeRole.PRIMARY)) {
                AgentReport r = monitoring.parse(n.lastReport);
                if (r == null || !r.capabilitiesOrEmpty().contains("consistencyCheck") || n.fenceRequired) continue;
                // Un contrôle manuel du jour (souvent partiel : schéma seul, reprise) ne remplace pas le contrôle planifié.
                if (Operation.count("nodeId = ?1 and type = ?2 and requestedAt >= ?3 and requestedBy = ?4", n.id, OperationType.CONSISTENCY_CHECK,
                        dayStart, OperationService.SYSTEM) > 0) continue;
                if (Operation.count("nodeId = ?1 and type = ?2 and status in ?3", n.id, OperationType.CONSISTENCY_CHECK,
                        List.of(OperationStatus.QUEUED, OperationStatus.RUNNING)) > 0) continue;
                try {
                    request(n, null, null, OperationService.SYSTEM);
                } catch (RuntimeException e) {
                    // adresse manquante, opération déjà en cours : signalé par l'alerte « contrôle en retard »
                }
            }
        }
    }

    public record NodeStatus(UUID nodeId, String node, String status, Operation last, Instant lastOkAt, boolean rebuiltSince) {}

    /**
     * État d'une réplique d'après ses contrôles terminés, du plus récent au plus ancien. Une reprise partielle sans écart
     * (« reprendre après … ») ne fait pas oublier un écart trouvé avant elle ; une reconstruction réussie, si.
     */
    @Transactional(Transactional.TxType.MANDATORY)
    public NodeStatus status(DatabaseNode n) {
        List<Operation> done = Operation.<Operation>find("nodeId = ?1 and type = ?2 and status in ?3 ORDER BY finishedAt DESC", n.id,
                OperationType.CONSISTENCY_CHECK, List.of(OperationStatus.SUCCEEDED, OperationStatus.FAILED, OperationStatus.INTERVENTION_REQUIRED))
                .page(0, 50).list();
        Operation last = null;
        for (Operation o : done) {
            boolean partialOk = o.status == OperationStatus.SUCCEEDED && "OK".equals(ops.read(o.result).path("status").asText())
                    && !ops.read(o.params).path("startAfter").asText("").isEmpty();
            if (last == null || !partialOk) { last = o; if (!partialOk) break; }
        }
        // Dernier contrôle complet sans écart : complet, sans reprise, et au moins aussi large que la politique (données si FULL).
        Topology t = n.topologyId == null ? null : Topology.findById(n.topologyId);
        boolean needData = t == null || !"SCHEMA".equals(t.consistencyMode);
        Operation okOp = done.stream().filter(o -> {
            if (o.status != OperationStatus.SUCCEEDED) return false;
            JsonNode r = ops.read(o.result), p = ops.read(o.params);
            return "OK".equals(r.path("status").asText()) && r.path("completed").asBoolean(true) && p.path("startAfter").asText("").isEmpty()
                    && (!needData || !"SCHEMA".equals(p.path("mode").asText()));
        }).findFirst().orElse(null);
        Operation rebuilt = Operation.find("nodeId = ?1 and type = ?2 and status = ?3 ORDER BY finishedAt DESC", n.id,
                OperationType.PROVISION_REPLICA, OperationStatus.SUCCEEDED).firstResult();
        boolean since = last != null && rebuilt != null && rebuilt.finishedAt.isAfter(last.finishedAt);
        String st = last == null ? "NONE" : last.status != OperationStatus.SUCCEEDED ? "FAILED" : ops.read(last.result).path("status").asText("UNKNOWN");
        if (since) st = "REBUILT";
        return new NodeStatus(n.id, n.name, st, last, okOp == null ? null : okOp.finishedAt, since);
    }

    /** Dernier contrôle divergent d'une réplique (utilisé par les plans de bascule). */
    @Transactional(Transactional.TxType.MANDATORY)
    public String divergence(DatabaseNode n) {
        NodeStatus s = status(n);
        if (!"DIVERGENT".equals(s.status())) return null;
        JsonNode sum = ops.read(s.last().result).path("summary");
        return n.name + " différait du primaire au dernier contrôle (" + s.last().finishedAt.toString().substring(0, 16).replace('T', ' ') + " UTC) : "
                + sum.path("divergentTables").asInt() + " table(s), " + sum.path("schemaDifferences").asInt() + " différence(s) de schéma";
    }

    @Transactional(Transactional.TxType.MANDATORY)
    public List<Finding> findings(Topology t, Instant now) {
        List<Finding> out = new ArrayList<>();
        for (DatabaseNode n : DatabaseNode.<DatabaseNode>list("topologyId = ?1 and expectedRole <> ?2", t.id, NodeRole.PRIMARY)) {
            NodeStatus s = status(n);
            if ("DIVERGENT".equals(s.status())) {
                JsonNode sum = ops.read(s.last().result).path("summary");
                out.add(new Finding("CONSISTENCY_DIVERGENT", n.name, Severity.CRITICAL,
                        n.name + " n'est plus une copie fidèle du primaire : " + sum.path("divergentTables").asInt() + " table(s) aux données différentes, "
                                + sum.path("schemaDifferences").asInt() + " différence(s) de schéma",
                        "Ne pas basculer vers cette réplique sans vérification. Proposition : la reconstruire depuis une sauvegarde vérifiée "
                                + "(aucune correction automatique). Détail dans Cohérence."));
            }
            if (t.consistencyEnabled && t.consistencyMaxAgeDays > 0) {
                AgentReport r = monitoring.parse(n.lastReport);
                if (r == null || !r.capabilitiesOrEmpty().contains("consistencyCheck")) continue;
                Instant ref = s.lastOkAt() != null ? s.lastOkAt() : t.createdAt;
                if (ref != null && Duration.between(ref, now).toDays() >= t.consistencyMaxAgeDays && !"DIVERGENT".equals(s.status())) {
                    out.add(new Finding("CONSISTENCY_OVERDUE", n.name, Severity.WARNING,
                            "Aucun contrôle de cohérence réussi de " + n.name + " depuis " + t.consistencyMaxAgeDays + " jours",
                            s.last() != null && s.last().error != null ? s.last().error : "Lancer un contrôle depuis la page Cohérence"));
                }
            }
        }
        return out;
    }
}
