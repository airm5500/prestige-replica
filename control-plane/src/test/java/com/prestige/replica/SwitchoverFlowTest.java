package com.prestige.replica;

import static io.restassured.RestAssured.given;
import static org.hamcrest.Matchers.*;

import io.quarkus.test.junit.QuarkusTest;
import io.restassured.http.ContentType;
import io.restassured.specification.RequestSpecification;
import java.time.Instant;
import java.util.*;
import org.junit.jupiter.api.MethodOrderer;
import org.junit.jupiter.api.Order;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.TestMethodOrder;

/** Lot 3 : bascule orchestrée, retour arrière, pause/reprise, réparation guidée. */
@QuarkusTest
@TestMethodOrder(MethodOrderer.OrderAnnotation.class)
class SwitchoverFlowTest {
    static String tenant, site, topo;
    static final Map<String, String> ids = new HashMap<>(), tokens = new HashMap<>();
    static String switchover;

    static RequestSpecification admin() {
        return given().auth().preemptive().basic("admin", "test-admin-password").contentType(ContentType.JSON);
    }
    static RequestSpecification as(String u) {
        return given().auth().preemptive().basic(u, "mot-de-passe-tres-long").contentType(ContentType.JSON);
    }
    static RequestSpecification agent(String node) {
        return given().header("Authorization", "Bearer " + tokens.get(node)).contentType(ContentType.JSON);
    }

    /** Rapport d'agent : primaire (source == null) ou réplique de {@code source}. */
    static void report(String node, long serverId, String source, String io, String sql, long sqlErrno) {
        Map<String, Object> server = new HashMap<>(Map.of("version", "10.6.18-MariaDB", "serverId", serverId, "logBin", true,
                "logSlaveUpdates", true, "gtidStrictMode", true, "readOnly", source != null, "binlogExpireSeconds", 604800));
        List<Object> channels = new ArrayList<>();
        if (source != null) {
            String[] hp = source.split(":");
            Map<String, Object> c = new HashMap<>(Map.of("connectionName", "", "masterHost", hp[0], "masterPort", Integer.parseInt(hp[1]),
                    "ioRunning", io, "sqlRunning", sql, "secondsBehindMaster", 0, "usingGtid", "Slave_Pos", "sqlDelay", 0));
            if (sqlErrno != 0) { c.put("lastSqlErrno", sqlErrno); c.put("lastSqlError", "Duplicate entry '42' for key 'PRIMARY'"); }
            channels.add(c);
        }
        Map<String, Object> r = new HashMap<>(Map.of("schemaVersion", 1, "agentVersion", "0.3.0", "collectedAt", Instant.now().toString(),
                "reachable", true, "capabilities", List.of("replicationControl", "provisioning"), "server", server,
                "channels", channels, "disks", List.of(), "errors", List.of()));
        agent(node).body(Map.of("reports", List.of(r))).post("/api/v1/agent/reports").then().statusCode(200);
    }

    static void healthy() {
        report("A", 1, null, null, null, 0);
        report("B", 2, "10.0.0.1:3306", "Yes", "Yes", 0);
        report("C", 3, "10.0.0.1:3306", "Yes", "Yes", 0);
    }

    /** Simule l'agent : récupère l'unique opération en attente et renvoie le résultat. */
    static Map<String, Object> run(String node, String type, String status, Map<String, Object> result) {
        var job = agent(node).get("/api/v1/agent/jobs").then().statusCode(200).body("size()", is(1)).body("[0].type", is(type)).extract();
        String id = job.path("[0].id");
        Map<String, Object> params = job.path("[0].params");
        agent(node).body(Map.of()).post("/api/v1/agent/jobs/" + id + "/start").then().statusCode(204);
        agent(node).body(Map.of("status", status, "error", status.equals("SUCCEEDED") ? "" : "échec simulé", "result", result))
                .post("/api/v1/agent/jobs/" + id + "/finish").then().statusCode(204);
        return params;
    }

    static void noJob(String node) {
        agent(node).get("/api/v1/agent/jobs").then().body("size()", is(0));
    }

    @Test @Order(1)
    void setup() {
        tenant = admin().body(Map.of("code", "bascule-t", "name", "Bascule")).post("/api/v1/tenants").then().statusCode(200).extract().path("id");
        site = admin().body(Map.of("tenantId", tenant, "code", "BSC", "name", "Pharmacie Bascule")).post("/api/v1/sites").then().statusCode(200).extract().path("id");
        topo = admin().body(Map.of("siteId", site, "name", "prod-bascule")).post("/api/v1/topologies").then().statusCode(200).extract().path("id");
        String[][] nodes = {{"A", "PRIMARY", "10.0.0.1:3306"}, {"B", "REPLICA", "10.0.0.2:3306"}, {"C", "REPLICA", "10.0.0.3:3306"}};
        for (String[] n : nodes) {
            var r = admin().body(Map.of("siteId", site, "topologyId", topo, "name", n[0], "expectedRole", n[1], "addressLabel", n[2]))
                    .post("/api/v1/nodes/register").then().statusCode(200).extract();
            ids.put(n[0], r.path("nodeId"));
            tokens.put(n[0], r.path("agentToken"));
        }
        for (String u : List.of("op.bascule", "appro.bascule")) {
            admin().body(Map.of("username", u, "password", "mot-de-passe-tres-long", "role", u.startsWith("op") ? "OPERATOR" : "APPROVER", "tenantId", tenant))
                    .post("/api/v1/users").then().statusCode(200);
        }
        healthy();
    }

    @Test @Order(2)
    void planRefusesWrongTargets() {
        as("op.bascule").body(Map.of("targetNodeId", ids.get("A"))).post("/api/v1/topologies/" + topo + "/switchover/plan").then()
                .statusCode(200).body("ready", is(false));
        report("B", 2, "10.0.0.1:3306", "Yes", "No", 1062);
        as("op.bascule").body(Map.of("targetNodeId", ids.get("B"))).post("/api/v1/topologies/" + topo + "/switchover/plan").then()
                .statusCode(200).body("ready", is(false)).body("checks.find { it.id == 'target-channel' }.status", is("FAIL"));
        healthy();
        as("op.bascule").body(Map.of("targetNodeId", ids.get("B"))).post("/api/v1/topologies/" + topo + "/switchover/plan").then()
                .statusCode(200).body("ready", is(true)).body("repointed", contains("C")).body("newSource", is("10.0.0.2:3306"));
    }

    @Test @Order(3)
    void promotionFailureRollsBackWithoutDataLoss() {
        String op = as("op.bascule").body(Map.of("targetNodeId", ids.get("B"), "reattachOldPrimary", false, "topologyConfirmation", "prod-bascule"))
                .post("/api/v1/topologies/" + topo + "/switchover").then().statusCode(200).body("status", is("AWAITING_APPROVAL")).extract().path("id");
        noJob("A");
        as("appro.bascule").body(Map.of("confirmation", "BSC")).post("/api/v1/operations/" + op + "/approve").then().statusCode(200);
        run("A", "SWITCHOVER_DEMOTE", "SUCCEEDED", Map.of("demoted", true, "gtid", "0-1-100", "killedConnections", 3));
        Map<String, Object> p = run("B", "SWITCHOVER_PROMOTE", "FAILED", Map.of("promoted", false));
        org.junit.jupiter.api.Assertions.assertEquals("0-1-100", p.get("gtid"));
        run("A", "SWITCHOVER_UNDO", "SUCCEEDED", Map.of("readOnly", false));
        admin().get("/api/v1/operations/" + op).then().body("operation.status", is("FAILED"))
                .body("operation.error", containsString("sans perte")).body("steps.size()", is(3));
        admin().get("/api/v1/nodes/" + ids.get("A")).then().body("summary.expectedRole", is("PRIMARY"));
        admin().get("/api/v1/topologies").then().body("find { it.id == '" + topo + "' }.maintenance", is(false));
    }

    @Test @Order(4)
    void switchoverPromotesTargetAndRepointsReplicas() {
        healthy();
        as("op.bascule").body(Map.of("targetNodeId", ids.get("B"), "reattachOldPrimary", true, "topologyConfirmation", "mauvais"))
                .post("/api/v1/topologies/" + topo + "/switchover").then().statusCode(400);
        switchover = as("op.bascule").body(Map.of("targetNodeId", ids.get("B"), "reattachOldPrimary", true, "topologyConfirmation", "prod-bascule"))
                .post("/api/v1/topologies/" + topo + "/switchover").then().statusCode(200).extract().path("id");
        as("op.bascule").body(Map.of("confirmation", "BSC")).post("/api/v1/operations/" + switchover + "/approve").then().statusCode(403);
        as("appro.bascule").body(Map.of("confirmation", "BSC")).post("/api/v1/operations/" + switchover + "/approve").then()
                .statusCode(200).body("status", is("RUNNING"));
        admin().get("/api/v1/topologies").then().body("find { it.id == '" + topo + "' }.maintenance", is(true));
        noJob("B"); // la promotion attend la rétrogradation
        agent("B").get("/api/v1/agent/jobs").then().header("X-Prm-Poll-Seconds", "1");
        run("A", "SWITCHOVER_DEMOTE", "SUCCEEDED", Map.of("demoted", true, "gtid", "0-1-200", "killedConnections", 1));
        run("B", "SWITCHOVER_PROMOTE", "SUCCEEDED", Map.of("promoted", true, "gtid", "0-1-200"));
        admin().get("/api/v1/nodes/" + ids.get("B")).then().body("summary.expectedRole", is("PRIMARY"));
        admin().get("/api/v1/nodes/" + ids.get("A")).then().body("summary.expectedRole", is("REPLICA"));
        Map<String, Object> pc = run("C", "REPLICA_ATTACH", "SUCCEEDED", Map.of());
        org.junit.jupiter.api.Assertions.assertEquals("10.0.0.2", pc.get("sourceHost"));
        org.junit.jupiter.api.Assertions.assertEquals(3306, pc.get("sourcePort"));
        admin().get("/api/v1/operations/" + switchover).then().body("operation.status", is("RUNNING"));
        run("A", "REPLICA_ATTACH", "SUCCEEDED", Map.of());
        admin().get("/api/v1/operations/" + switchover).then().body("operation.status", is("SUCCEEDED"))
                .body("operation.result", containsString("writeUnavailableMs")).body("steps.size()", is(4))
                .body("events.message", hasItem(containsString("faire pointer Prestige vers 10.0.0.2:3306")));
        admin().get("/api/v1/topologies").then().body("find { it.id == '" + topo + "' }.maintenance", is(false));
        agent("B").get("/api/v1/agent/jobs").then().header("X-Prm-Poll-Seconds", nullValue());
        admin().get("/api/v1/audit?action=SWITCHOVER_SUCCEEDED").then().body("size()", greaterThanOrEqualTo(1));
    }

    @Test @Order(5)
    void pauseReplacesThreadAlertsByReminder() {
        String c = ids.get("C");
        as("op.bascule").body(Map.of("mode", "ALL", "reason", "")).post("/api/v1/nodes/" + c + "/replication/pause").then().statusCode(400);
        as("op.bascule").body(Map.of("mode", "ALL", "reason", "inventaire annuel")).post("/api/v1/nodes/" + c + "/replication/pause").then().statusCode(200);
        run("C", "REPLICATION_PAUSE", "SUCCEEDED", Map.of("gtidSlavePos", "0-2-210"));
        report("C", 3, "10.0.0.2:3306", "No", "No", 0);
        admin().get("/api/v1/incidents?status=open").then()
                .body("findAll { it.nodeId == '" + c + "' }.type", hasItem("REPLICATION_PAUSED"))
                .body("findAll { it.nodeId == '" + c + "' }.type", not(hasItem("SQL_THREAD_STOPPED")));
        as("op.bascule").get("/api/v1/nodes/" + c + "/repair-plan").then().body("actions[0].id", is("RESUME"));
        as("op.bascule").post("/api/v1/nodes/" + c + "/replication/resume").then().statusCode(200);
        run("C", "REPLICATION_RESUME", "SUCCEEDED", Map.of());
        report("C", 3, "10.0.0.2:3306", "Yes", "Yes", 0);
        admin().get("/api/v1/incidents?status=open").then()
                .body("findAll { it.nodeId == '" + c + "' }.type", not(hasItem("REPLICATION_PAUSED")));
    }

    @Test @Order(6)
    void repairPlanNeverOffersToSkipErrors() {
        report("C", 3, "10.0.0.2:3306", "Yes", "No", 1062);
        as("op.bascule").get("/api/v1/nodes/" + ids.get("C") + "/repair-plan").then().statusCode(200)
                .body("diagnosis", containsString("1062")).body("actions[0].id", is("REBUILD")).body("actions[0].recommended", is(true))
                .body("actions.id", not(hasItem("SKIP")));
    }
}
