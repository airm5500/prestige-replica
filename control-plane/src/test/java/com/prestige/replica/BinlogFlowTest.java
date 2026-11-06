package com.prestige.replica;

import static io.restassured.RestAssured.given;
import static org.hamcrest.Matchers.*;

import com.prestige.replica.alert.Finding;
import com.prestige.replica.domain.Backup;
import com.prestige.replica.domain.Topology;
import com.prestige.replica.service.BinlogService;
import io.quarkus.narayana.jta.QuarkusTransaction;
import io.quarkus.test.junit.QuarkusTest;
import io.restassured.http.ContentType;
import io.restassured.specification.RequestSpecification;
import jakarta.inject.Inject;
import java.time.Duration;
import java.time.Instant;
import java.time.temporal.ChronoUnit;
import java.util.*;
import org.junit.jupiter.api.Assertions;
import org.junit.jupiter.api.MethodOrderer;
import org.junit.jupiter.api.Order;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.TestMethodOrder;

/** Archivage continu des binlogs, catalogue, constats, et restauration à un instant précis. */
@QuarkusTest
@TestMethodOrder(MethodOrderer.OrderAnnotation.class)
class BinlogFlowTest {
    static String topo;
    static final Map<String, String> ids = new HashMap<>(), tokens = new HashMap<>();
    static final Instant NOW = Instant.now().truncatedTo(ChronoUnit.SECONDS);
    @Inject BinlogService binlogs;

    static RequestSpecification admin() {
        return given().auth().preemptive().basic("admin", "test-admin-password").contentType(ContentType.JSON);
    }
    static RequestSpecification as(String u) {
        return given().auth().preemptive().basic(u, "mot-de-passe-tres-long").contentType(ContentType.JSON);
    }
    static RequestSpecification agent(String node) {
        return given().header("Authorization", "Bearer " + tokens.get(node)).contentType(ContentType.JSON);
    }

    static void report(String node, long serverId, String source, List<String> caps) {
        Map<String, Object> server = new HashMap<>(Map.of("version", "10.6.18-MariaDB", "serverId", serverId, "logBin", true,
                "logSlaveUpdates", true, "gtidStrictMode", true, "readOnly", source != null, "binlogExpireSeconds", 604800));
        List<Object> channels = new ArrayList<>();
        if (source != null) {
            String[] hp = source.split(":");
            channels.add(Map.of("connectionName", "", "masterHost", hp[0], "masterPort", Integer.parseInt(hp[1]),
                    "ioRunning", "Yes", "sqlRunning", "Yes", "secondsBehindMaster", 0, "usingGtid", "Slave_Pos", "sqlDelay", 0));
        }
        Map<String, Object> r = new HashMap<>(Map.of("schemaVersion", 1, "agentVersion", "0.5.0", "collectedAt", Instant.now().toString(),
                "reachable", true, "capabilities", caps, "server", server,
                "channels", channels, "disks", List.of(), "errors", List.of()));
        agent(node).body(Map.of("reports", List.of(r))).post("/api/v1/agent/reports").then().statusCode(200);
    }

    @SuppressWarnings("unchecked")
    static Map<String, Object> run(String node, String type, String status, Map<String, Object> result) {
        var job = agent(node).get("/api/v1/agent/jobs").then().statusCode(200).body("size()", is(1)).body("[0].type", is(type)).extract();
        String id = job.path("[0].id");
        agent(node).body(Map.of()).post("/api/v1/agent/jobs/" + id + "/start").then().statusCode(204);
        agent(node).body(Map.of("status", status, "error", status.equals("SUCCEEDED") ? "" : "échec simulé", "result", result))
                .post("/api/v1/agent/jobs/" + id + "/finish").then().statusCode(204);
        return job.path("[0].params");
    }

    static void noJob(String node) {
        agent(node).get("/api/v1/agent/jobs").then().body("size()", is(0));
    }

    static Map<String, Object> file(String name, String first, String last, Instant from, Instant to, boolean complete) {
        Map<String, Object> m = new HashMap<>(Map.of("file", name, "relativePath", "x/1/" + name + ".prmseal", "sizeBytes", 1000,
                "sha256", "ab" + name.hashCode(), "firstGtid", first, "lastGtid", last, "complete", complete));
        m.put("firstEventAt", from.toString());
        m.put("lastEventAt", to.toString());
        return m;
    }

    List<Finding> findings(Instant at) {
        return QuarkusTransaction.requiringNew().call(() -> binlogs.findings(Topology.findById(UUID.fromString(topo)), at));
    }

    @Test @Order(1)
    void setup() {
        String tenant = admin().body(Map.of("code", "journaux-t", "name", "Journaux")).post("/api/v1/tenants").then().statusCode(200).extract().path("id");
        String site = admin().body(Map.of("tenantId", tenant, "code", "JRN", "name", "Pharmacie Journaux")).post("/api/v1/sites").then().statusCode(200).extract().path("id");
        topo = admin().body(Map.of("siteId", site, "name", "prod-journaux")).post("/api/v1/topologies").then().statusCode(200).extract().path("id");
        String[][] nodes = {{"A", "PRIMARY", "10.0.3.1:3306"}, {"B", "REPLICA", "10.0.3.2:3306"}};
        for (String[] n : nodes) {
            var r = admin().body(Map.of("siteId", site, "topologyId", topo, "name", n[0], "expectedRole", n[1], "addressLabel", n[2]))
                    .post("/api/v1/nodes/register").then().statusCode(200).extract();
            ids.put(n[0], r.path("nodeId"));
            tokens.put(n[0], r.path("agentToken"));
        }
        admin().body(Map.of("username", "op.jrn", "password", "mot-de-passe-tres-long", "role", "OPERATOR", "tenantId", tenant))
                .post("/api/v1/users").then().statusCode(200);
        report("A", 1, null, List.of());
        report("B", 2, "10.0.3.1:3306", List.of("binlogArchive", "restoreTest"));
    }

    @Test @Order(2)
    void archivingIsScheduledAndCatalogued() {
        // Opérateur : réglage réservé aux administrateurs.
        as("op.jrn").body(Map.of("binlogArchiveEnabled", true, "binlogArchiveNodeId", ids.get("B"), "binlogArchiveMinutes", 5))
                .put("/api/v1/topologies/" + topo + "/binlog-policy").then().statusCode(403);
        binlogs.schedule(Instant.now());
        noJob("B");
        admin().body(Map.of("binlogArchiveEnabled", true, "binlogArchiveNodeId", ids.get("B"), "binlogArchiveMinutes", 5))
                .put("/api/v1/topologies/" + topo + "/binlog-policy").then().statusCode(200).body("binlogArchiveEnabled", is(true));
        binlogs.schedule(Instant.now());
        Map<String, Object> p = run("B", "BINLOG_ARCHIVE", "SUCCEEDED", Map.of("files", List.of(
                file("mysql-bin.000001", "0-1-1", "0-1-49", NOW.minus(Duration.ofHours(6)), NOW.minus(Duration.ofHours(4)).minus(Duration.ofMinutes(5)), true),
                file("mysql-bin.000002", "0-1-50", "0-1-120", NOW.minus(Duration.ofHours(4)), NOW.minus(Duration.ofHours(2)), true),
                file("mysql-bin.000003", "0-1-121", "0-1-200", NOW.minus(Duration.ofHours(2)), NOW.minus(Duration.ofMinutes(10)), false)),
                "gap", "", "active", "mysql-bin.000003", "deleted", List.of()));
        Assertions.assertEquals("10.0.3.1", p.get("sourceHost"));
        Assertions.assertEquals(1, ((Number) p.get("serverId")).intValue());
        Assertions.assertEquals(List.of(), p.get("archived"));
        admin().get("/api/v1/topologies/" + topo + "/binlogs").then().statusCode(200)
                .body("files.size()", is(3)).body("totalBytes", is(3000)).body("gapAt", nullValue());
        // Pas avant l'intervalle ; ensuite seuls les fichiers complets sont déjà acquis.
        binlogs.schedule(Instant.now());
        noJob("B");
        binlogs.schedule(Instant.now().plus(Duration.ofMinutes(6)));
        Map<String, Object> p2 = run("B", "BINLOG_ARCHIVE", "SUCCEEDED", Map.of("files", List.of(
                file("mysql-bin.000003", "0-1-121", "0-1-210", NOW.minus(Duration.ofHours(2)), NOW.minus(Duration.ofMinutes(5)), true)),
                "gap", "", "active", "", "deleted", List.of()));
        Assertions.assertEquals(List.of("mysql-bin.000001", "mysql-bin.000002"), p2.get("archived"));
        admin().get("/api/v1/topologies/" + topo + "/binlogs").then().body("files.size()", is(3))
                .body("files.find { it.fileName == 'mysql-bin.000003' }.complete", is(true))
                .body("files.find { it.fileName == 'mysql-bin.000003' }.lastGtid", is("0-1-210"));
        Assertions.assertTrue(findings(Instant.now()).isEmpty());
        // Plus de nouvelles pendant une heure : constat de retard.
        Assertions.assertTrue(findings(Instant.now().plus(Duration.ofHours(1))).stream().anyMatch(f -> f.type().equals("BINLOG_ARCHIVE_LATE")));
    }

    @Test @Order(3)
    void pointInTimeRestorePlan() {
        Instant stop = NOW.minus(Duration.ofHours(1));
        admin().body(Map.of("stopAt", stop.toString())).post("/api/v1/topologies/" + topo + "/pitr/plan").then().statusCode(200)
                .body("ready", is(false)).body("checks.find { it.id == 'backup' }.status", is("FAIL"));
        QuarkusTransaction.requiringNew().run(() -> {
            Topology t = Topology.findById(UUID.fromString(topo));
            t.restoreNodeId = UUID.fromString(ids.get("B"));
            Backup b = new Backup();
            b.tenantId = t.tenantId;
            b.siteId = t.siteId;
            b.topologyId = t.id;
            b.nodeId = UUID.fromString(ids.get("B"));
            b.relativeDir = "tenants/x/backups/b1";
            b.status = Backup.Status.COMPLETED;
            b.startedAt = NOW.minus(Duration.ofHours(3));
            b.finishedAt = NOW.minus(Duration.ofHours(3)).plus(Duration.ofMinutes(10));
            b.sha256 = "abc";
            b.gtid = "0-1-100";
            b.verifyStatus = Backup.Verify.VERIFIED;
            b.persist();
        });
        admin().body(Map.of("stopAt", stop.toString())).post("/api/v1/topologies/" + topo + "/pitr/plan").then().statusCode(200)
                .body("ready", is(true)).body("backupGtid", is("0-1-100")).body("executor", is("B"))
                .body("binlogs.fileName", contains("mysql-bin.000002", "mysql-bin.000003"))
                .body("checks.find { it.id == 'archive' }.status", is("OK"));
        // Au-delà de l'archive : avertissement, pas de blocage.
        admin().body(Map.of("stopAt", NOW.minus(Duration.ofMinutes(1)).toString())).post("/api/v1/topologies/" + topo + "/pitr/plan").then()
                .body("ready", is(true)).body("checks.find { it.id == 'archive' }.status", is("WARN"));
        admin().body(Map.of("stopAt", NOW.plus(Duration.ofHours(1)).toString())).post("/api/v1/topologies/" + topo + "/pitr/plan").then()
                .body("ready", is(false));
    }

    @Test @Order(4)
    void pointInTimeRestoreRequest() {
        Instant stop = NOW.minus(Duration.ofHours(1));
        as("op.jrn").body(Map.of("stopAt", stop.toString(), "extract", List.of("prestige.vente; DROP"), "reason", "suppression"))
                .post("/api/v1/topologies/" + topo + "/pitr").then().statusCode(400);
        as("op.jrn").body(Map.of("stopAt", stop.toString(), "extract", List.of("prestige.vente"), "reason", ""))
                .post("/api/v1/topologies/" + topo + "/pitr").then().statusCode(400);
        String op = as("op.jrn").body(Map.of("stopAt", stop.toString(), "extract", List.of("prestige.vente", "prestige.vente"), "reason", "ventes supprimées par erreur"))
                .post("/api/v1/topologies/" + topo + "/pitr").then().statusCode(200).body("status", is("QUEUED")).extract().path("id");
        var job = agent("B").get("/api/v1/agent/jobs").then().body("size()", is(1)).body("[0].type", is("PITR_RESTORE")).extract();
        Assertions.assertEquals(op, job.path("[0].id"));
        Assertions.assertEquals("0-1-100", job.path("[0].params.startGtid"));
        Assertions.assertEquals(List.of("prestige.vente"), job.path("[0].params.extract"));
        Assertions.assertEquals(stop.toString(), job.path("[0].params.stopAt"));
        Assertions.assertEquals(List.of("x/1/mysql-bin.000002.prmseal", "x/1/mysql-bin.000003.prmseal"), job.path("[0].params.binlogs.relativePath"));
        String dir = job.path("[0].params.extractDir");
        Assertions.assertTrue(dir.contains("/recoveries/"), dir);
        admin().get("/api/v1/audit").then().body("action", hasItem("OPERATION_REQUEST"));
    }

    @Test @Order(5)
    void gapAndExpiry() {
        // Les journaux de l'archive les plus anciens précèdent la seule sauvegarde : purge proposée au prochain passage.
        binlogs.schedule(Instant.now().plus(Duration.ofMinutes(20)));
        // B exécute encore la restauration : l'archivage attend son tour dans la file de B.
        var jobs = agent("B").get("/api/v1/agent/jobs").then().extract();
        String pitr = jobs.path("[0].id");
        agent("B").body(Map.of()).post("/api/v1/agent/jobs/" + pitr + "/start").then().statusCode(204);
        agent("B").body(Map.of("status", "SUCCEEDED", "error", "", "result", Map.of("reachedGtid", "0-1-150", "transactions", 50)))
                .post("/api/v1/agent/jobs/" + pitr + "/finish").then().statusCode(204);
        Map<String, Object> p = run("B", "BINLOG_ARCHIVE", "SUCCEEDED", Map.of("files", List.of(),
                "gap", "mysql-bin.000004 purgé par le primaire avant archivage", "deleted", List.of("x/1/mysql-bin.000001.prmseal")));
        Assertions.assertEquals(List.of("x/1/mysql-bin.000001.prmseal"), p.get("expired"));
        admin().get("/api/v1/topologies/" + topo + "/binlogs").then().body("files.size()", is(2)).body("gapAt", notNullValue());
        Assertions.assertTrue(findings(Instant.now()).stream().anyMatch(f -> f.type().equals("BINLOG_ARCHIVE_GAP")));
    }

    @Inject com.prestige.replica.service.Jobs jobs;

    @Test @Order(6)
    @SuppressWarnings("unchecked")
    void searchInArchivedJournals() {
        String tenant = admin().get("/api/v1/topologies").then().extract().path("find { it.id == '" + topo + "' }.tenantId");
        admin().body(Map.of("username", "lecteur.jrn", "password", "mot-de-passe-tres-long", "role", "READ_ONLY", "tenantId", tenant))
                .post("/api/v1/users").then().statusCode(200);
        Map<String, Object> q = new HashMap<>(Map.of("from", NOW.minus(Duration.ofHours(1)).toString(), "to", NOW.toString(),
                "tables", List.of("prestige.vente"), "types", List.of("DELETE"), "text", "", "reason", "ventes disparues"));
        as("lecteur.jrn").body(q).post("/api/v1/topologies/" + topo + "/binlog-search").then().statusCode(403);
        q.put("reason", "");
        as("op.jrn").body(q).post("/api/v1/topologies/" + topo + "/binlog-search").then().statusCode(400);
        q.put("reason", "ventes disparues");
        q.put("types", List.of("TRUNCATE"));
        as("op.jrn").body(q).post("/api/v1/topologies/" + topo + "/binlog-search").then().statusCode(400);
        q.put("types", List.of("DELETE"));
        q.put("from", NOW.minus(Duration.ofDays(40)).toString());
        as("op.jrn").body(q).post("/api/v1/topologies/" + topo + "/binlog-search").then().statusCode(400);
        // Période antérieure à l'archive : rien à lire.
        q.put("from", NOW.minus(Duration.ofDays(3)).toString());
        q.put("to", NOW.minus(Duration.ofDays(2)).toString());
        as("op.jrn").body(q).post("/api/v1/topologies/" + topo + "/binlog-search").then().statusCode(400);
        q.put("from", NOW.minus(Duration.ofHours(1)).toString());
        q.put("to", NOW.toString());
        String op = as("op.jrn").body(q).post("/api/v1/topologies/" + topo + "/binlog-search").then().statusCode(200)
                .body("type", is("BINLOG_SEARCH")).extract().path("id");
        Map<String, Object> p = run("B", "BINLOG_SEARCH", "SUCCEEDED", Map.of("total", 1, "rows", 3, "transactions", List.of(Map.of(
                "gtid", "0-1-150", "at", NOW.minus(Duration.ofMinutes(30)).toString(), "serverId", 1, "file", "mysql-bin.000003", "pos", 4242,
                "queries", List.of("DELETE FROM vente WHERE jour = '2026-10-30'"),
                "changes", List.of(Map.of("type", "DELETE", "database", "prestige", "table", "vente", "rows", 3,
                        "sample", List.of(Map.of("before", List.of("@1=17", "@2='Dupont'")))))))));
        // Seul le fichier encore actif (couvrant la dernière heure) est lu.
        Assertions.assertEquals(List.of("x/1/mysql-bin.000003.prmseal"), ((List<Map<String, Object>>) p.get("binlogs")).stream().map(m -> m.get("relativePath")).toList());
        Assertions.assertTrue(((String) p.get("resultDir")).contains("/searches/"));
        // Liste : jamais de données ; détail : selon le rôle.
        admin().get("/api/v1/operations?type=BINLOG_SEARCH").then().body("find { it.id == '" + op + "' }.result", nullValue());
        as("op.jrn").get("/api/v1/operations/" + op).then().body("operation.result", containsString("Dupont"));
        as("lecteur.jrn").get("/api/v1/operations/" + op).then().body("operation.result", not(containsString("Dupont")))
                .body("operation.result", not(containsString("2026-10-30"))).body("operation.result", containsString("\"masked\":true"));
        // Le masquage ne modifie pas ce qui est enregistré.
        as("op.jrn").get("/api/v1/operations/" + op).then().body("operation.result", containsString("Dupont"));
        admin().get("/api/v1/audit").then().body("details", hasItem(containsString("ventes disparues")));
        jobs.purgeSearchResults(Instant.now().plus(Duration.ofDays(1)));
        as("op.jrn").get("/api/v1/operations/" + op).then().body("operation.result", not(containsString("Dupont")));
    }

    @Test @Order(7)
    void restoreExactlyBeforeATransaction() {
        Instant at = NOW.minus(Duration.ofMinutes(150));
        Map<String, Object> q = new HashMap<>(Map.of("stopAt", at.toString(), "extract", List.of("prestige.vente"), "reason", "avant la suppression",
                "stopBefore", Map.of("relativePath", "x/1/mysql-bin.000002.prmseal", "position", 4242, "gtid", "0-1-90")));
        // La sauvegarde de départ (0-1-100) contient déjà cette transaction.
        as("op.jrn").body(q).post("/api/v1/topologies/" + topo + "/pitr").then().statusCode(400).body("message", containsString("contient déjà"));
        q.put("stopBefore", Map.of("relativePath", "x/1/autre.000009.prmseal", "position", 4242, "gtid", "0-1-110"));
        as("op.jrn").body(q).post("/api/v1/topologies/" + topo + "/pitr").then().statusCode(400);
        q.put("stopBefore", Map.of("relativePath", "x/1/mysql-bin.000002.prmseal", "position", 4242, "gtid", "0-1-110"));
        as("op.jrn").body(q).post("/api/v1/topologies/" + topo + "/pitr").then().statusCode(200).body("summary", containsString("juste avant la transaction 0-1-110"));
        var job = agent("B").get("/api/v1/agent/jobs").then().body("[0].type", is("PITR_RESTORE")).extract();
        Assertions.assertEquals(List.of("x/1/mysql-bin.000002.prmseal"), job.path("[0].params.binlogs.relativePath"));
        Assertions.assertEquals(4242, (int) job.path("[0].params.stopBefore.position"));
    }
}
