package com.prestige.replica;

import static io.restassured.RestAssured.given;
import static org.hamcrest.Matchers.*;

import com.prestige.replica.domain.Topology;
import com.prestige.replica.service.ReplicationService;
import io.quarkus.narayana.jta.QuarkusTransaction;
import io.quarkus.test.junit.QuarkusTest;
import io.restassured.http.ContentType;
import io.restassured.specification.RequestSpecification;
import jakarta.inject.Inject;
import java.time.Duration;
import java.time.Instant;
import java.util.*;
import org.junit.jupiter.api.Assertions;
import org.junit.jupiter.api.MethodOrderer;
import org.junit.jupiter.api.Order;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.TestMethodOrder;

/** Lot 4 : exercice de bascule aller-retour, programmé, mesuré, et rappel d'exercice. */
@QuarkusTest
@TestMethodOrder(MethodOrderer.OrderAnnotation.class)
class DrillFlowTest {
    static String topo, drill;
    static final Map<String, String> ids = new HashMap<>(), tokens = new HashMap<>();
    @Inject ReplicationService replication;

    static RequestSpecification admin() {
        return given().auth().preemptive().basic("admin", "test-admin-password").contentType(ContentType.JSON);
    }
    static RequestSpecification as(String u) {
        return given().auth().preemptive().basic(u, "mot-de-passe-tres-long").contentType(ContentType.JSON);
    }
    static RequestSpecification agent(String node) {
        return given().header("Authorization", "Bearer " + tokens.get(node)).contentType(ContentType.JSON);
    }

    static void report(String node, long serverId, String source) {
        Map<String, Object> server = new HashMap<>(Map.of("version", "10.6.18-MariaDB", "serverId", serverId, "logBin", true,
                "logSlaveUpdates", true, "gtidStrictMode", true, "readOnly", source != null, "binlogExpireSeconds", 604800));
        List<Object> channels = new ArrayList<>();
        if (source != null) {
            String[] hp = source.split(":");
            channels.add(Map.of("connectionName", "", "masterHost", hp[0], "masterPort", Integer.parseInt(hp[1]),
                    "ioRunning", "Yes", "sqlRunning", "Yes", "secondsBehindMaster", 0, "usingGtid", "Slave_Pos", "sqlDelay", 0));
        }
        Map<String, Object> r = new HashMap<>(Map.of("schemaVersion", 1, "agentVersion", "0.4.0", "collectedAt", Instant.now().toString(),
                "reachable", true, "capabilities", List.of("replicationControl"), "server", server,
                "channels", channels, "disks", List.of(), "errors", List.of()));
        agent(node).body(Map.of("reports", List.of(r))).post("/api/v1/agent/reports").then().statusCode(200);
    }

    static Map<String, Object> run(String node, String type, Map<String, Object> result) {
        var job = agent(node).get("/api/v1/agent/jobs").then().statusCode(200).body("size()", is(1)).body("[0].type", is(type)).extract();
        String id = job.path("[0].id");
        agent(node).body(Map.of()).post("/api/v1/agent/jobs/" + id + "/start").then().statusCode(204);
        agent(node).body(Map.of("status", "SUCCEEDED", "error", "", "result", result)).post("/api/v1/agent/jobs/" + id + "/finish").then().statusCode(204);
        return job.path("[0].params");
    }

    static void noJob(String node) {
        agent(node).get("/api/v1/agent/jobs").then().body("size()", is(0));
    }

    @Test @Order(1)
    void setup() {
        String tenant = admin().body(Map.of("code", "exercice-t", "name", "Exercice")).post("/api/v1/tenants").then().statusCode(200).extract().path("id");
        String site = admin().body(Map.of("tenantId", tenant, "code", "EXO", "name", "Pharmacie Exercice")).post("/api/v1/sites").then().statusCode(200).extract().path("id");
        topo = admin().body(Map.of("siteId", site, "name", "prod-exercice")).post("/api/v1/topologies").then().statusCode(200).extract().path("id");
        String[][] nodes = {{"A", "PRIMARY", "10.0.2.1:3306"}, {"B", "REPLICA", "10.0.2.2:3306"}};
        for (String[] n : nodes) {
            var r = admin().body(Map.of("siteId", site, "topologyId", topo, "name", n[0], "expectedRole", n[1], "addressLabel", n[2]))
                    .post("/api/v1/nodes/register").then().statusCode(200).extract();
            ids.put(n[0], r.path("nodeId"));
            tokens.put(n[0], r.path("agentToken"));
        }
        for (String u : List.of("op.exo", "appro.exo")) {
            admin().body(Map.of("username", u, "password", "mot-de-passe-tres-long", "role", u.startsWith("op") ? "OPERATOR" : "APPROVER", "tenantId", tenant))
                    .post("/api/v1/users").then().statusCode(200);
        }
        report("A", 1, null);
        report("B", 2, "10.0.2.1:3306");
    }

    @Test @Order(2)
    void scheduledDrillWaitsForItsTime() {
        Instant at = Instant.now().plus(Duration.ofHours(1));
        as("op.exo").body(Map.of("targetNodeId", ids.get("B"), "notBefore", Instant.now().plus(Duration.ofDays(40)).toString(), "topologyConfirmation", "prod-exercice"))
                .post("/api/v1/topologies/" + topo + "/drill").then().statusCode(400);
        drill = as("op.exo").body(Map.of("targetNodeId", ids.get("B"), "notBefore", at.toString(), "topologyConfirmation", "prod-exercice"))
                .post("/api/v1/topologies/" + topo + "/drill").then().statusCode(200).body("status", is("AWAITING_APPROVAL")).extract().path("id");
        as("appro.exo").body(Map.of("confirmation", "EXO")).post("/api/v1/operations/" + drill + "/approve").then().statusCode(200).body("status", is("QUEUED"));
        replication.advanceDrills(Instant.now());
        noJob("A");
        admin().get("/api/v1/operations/" + drill).then().body("operation.status", is("QUEUED"))
                .body("events.message", hasItem(containsString("Exercice programmé")));
        replication.advanceDrills(at.plusSeconds(1));
        admin().get("/api/v1/operations/" + drill).then().body("operation.status", is("RUNNING"));
    }

    @Test @Order(3)
    void outboundThenReturnAfterHomeCaughtUp() {
        run("A", "SWITCHOVER_DEMOTE", Map.of("demoted", true, "gtid", "0-1-300", "killedConnections", 2));
        run("B", "SWITCHOVER_PROMOTE", Map.of("promoted", true, "gtid", "0-1-300"));
        Map<String, Object> pa = run("A", "REPLICA_ATTACH", Map.of());
        Assertions.assertEquals("10.0.2.2", pa.get("sourceHost"));
        admin().get("/api/v1/operations/" + drill).then().body("operation.status", is("RUNNING"))
                .body("events.message", hasItem(containsString("Aller réussi")));
        // Le dernier rapport de A le montre encore primaire : le retour attend.
        replication.advanceDrills(Instant.now());
        noJob("B");
        report("A", 1, "10.0.2.2:3306");
        report("B", 2, null);
        replication.advanceDrills(Instant.now());
        run("B", "SWITCHOVER_DEMOTE", Map.of("demoted", true, "gtid", "0-2-310", "killedConnections", 0));
        Map<String, Object> pp = run("A", "SWITCHOVER_PROMOTE", Map.of("promoted", true, "gtid", "0-2-310"));
        Assertions.assertEquals("0-2-310", pp.get("gtid"));
        run("B", "REPLICA_ATTACH", Map.of());
        admin().get("/api/v1/operations/" + drill).then().body("operation.status", is("SUCCEEDED"))
                .body("operation.result", containsString("outbound")).body("operation.result", containsString("\"phase\": 2"))
                .body("steps.size()", is(6));
        admin().get("/api/v1/nodes/" + ids.get("A")).then().body("summary.expectedRole", is("PRIMARY"));
        admin().get("/api/v1/nodes/" + ids.get("B")).then().body("summary.expectedRole", is("REPLICA"));
        admin().get("/api/v1/topologies").then().body("find { it.id == '" + topo + "' }.maintenance", is(false));
        admin().get("/api/v1/audit?action=DRILL_SUCCEEDED").then().body("size()", is(1));
    }

    @Test @Order(4)
    void reminderWhenNoRecentDrill() {
        UUID t = UUID.fromString(topo);
        var soon = QuarkusTransaction.requiringNew().call(() -> replication.drillFindings(Topology.findById(t), Instant.now().plus(Duration.ofDays(10))));
        Assertions.assertTrue(soon.isEmpty());
        var late = QuarkusTransaction.requiringNew().call(() -> replication.drillFindings(Topology.findById(t), Instant.now().plus(Duration.ofDays(100))));
        Assertions.assertEquals("DRILL_OVERDUE", late.get(0).type());
        as("op.exo").body(Map.of("drillMaxAgeDays", 0)).put("/api/v1/topologies/" + topo + "/drill-policy").then().statusCode(403);
        admin().body(Map.of("drillMaxAgeDays", 0)).put("/api/v1/topologies/" + topo + "/drill-policy").then().statusCode(200);
        var off = QuarkusTransaction.requiringNew().call(() -> replication.drillFindings(Topology.findById(t), Instant.now().plus(Duration.ofDays(100))));
        Assertions.assertTrue(off.isEmpty());
    }
}
