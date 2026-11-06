package com.prestige.replica;

import static io.restassured.RestAssured.given;
import static org.hamcrest.Matchers.*;

import io.quarkus.test.junit.QuarkusTest;
import io.restassured.http.ContentType;
import io.restassured.specification.RequestSpecification;
import java.time.Instant;
import java.util.*;
import org.junit.jupiter.api.Assertions;
import org.junit.jupiter.api.MethodOrderer;
import org.junit.jupiter.api.Order;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.TestMethodOrder;

/** Lot 4 : bascule d'urgence assistée avec isolement de l'ancien primaire. */
@QuarkusTest
@TestMethodOrder(MethodOrderer.OrderAnnotation.class)
class FailoverFlowTest {
    static String topo;
    static final Map<String, String> ids = new HashMap<>(), tokens = new HashMap<>();

    static RequestSpecification admin() {
        return given().auth().preemptive().basic("admin", "test-admin-password").contentType(ContentType.JSON);
    }
    static RequestSpecification as(String u) {
        return given().auth().preemptive().basic(u, "mot-de-passe-tres-long").contentType(ContentType.JSON);
    }
    static RequestSpecification agent(String node) {
        return given().header("Authorization", "Bearer " + tokens.get(node)).contentType(ContentType.JSON);
    }

    static void primary(String node, long serverId, String pos, boolean readOnly) {
        Map<String, Object> server = new HashMap<>(Map.of("version", "10.6.18-MariaDB", "serverId", serverId, "logBin", true,
                "readOnly", readOnly, "gtidBinlogPos", pos, "gtidCurrentPos", pos));
        send(node, true, server, List.of());
    }
    static void replica(String node, long serverId, String ioPos) {
        Map<String, Object> server = new HashMap<>(Map.of("version", "10.6.18-MariaDB", "serverId", serverId, "logBin", true, "readOnly", true));
        Map<String, Object> c = new HashMap<>(Map.of("connectionName", "", "masterHost", "10.0.1.1", "masterPort", 3306,
                "ioRunning", "Connecting", "sqlRunning", "Yes", "secondsBehindMaster", 0, "usingGtid", "Slave_Pos", "gtidIoPos", ioPos));
        send(node, true, server, List.of(c));
    }
    static void down(String node) {
        send(node, false, null, List.of());
    }
    static void send(String node, boolean reachable, Map<String, Object> server, List<Object> channels) {
        Map<String, Object> r = new HashMap<>(Map.of("schemaVersion", 1, "agentVersion", "0.4.0", "collectedAt", Instant.now().toString(),
                "reachable", reachable, "capabilities", List.of("replicationControl", "provisioning"),
                "channels", channels, "disks", List.of(), "errors", List.of()));
        if (server != null) r.put("server", server);
        agent(node).body(Map.of("reports", List.of(r))).post("/api/v1/agent/reports").then().statusCode(200);
    }

    static Map<String, Object> run(String node, String type, String status, Map<String, Object> result) {
        var job = agent(node).get("/api/v1/agent/jobs").then().statusCode(200).body("size()", is(1)).body("[0].type", is(type)).extract();
        String id = job.path("[0].id");
        Map<String, Object> params = job.path("[0].params");
        agent(node).body(Map.of()).post("/api/v1/agent/jobs/" + id + "/start").then().statusCode(204);
        agent(node).body(Map.of("status", status, "error", status.equals("SUCCEEDED") ? "" : "service introuvable", "result", result))
                .post("/api/v1/agent/jobs/" + id + "/finish").then().statusCode(204);
        return params;
    }

    static String request(Map<String, Object> body) {
        String op = as("op.urgence").body(body).post("/api/v1/topologies/" + topo + "/failover").then().statusCode(200)
                .body("status", is("AWAITING_APPROVAL")).extract().path("id");
        as("appro.urgence").body(Map.of("confirmation", "URG")).post("/api/v1/operations/" + op + "/approve").then().statusCode(200);
        return op;
    }

    @Test @Order(1)
    void setup() {
        String tenant = admin().body(Map.of("code", "urgence-t", "name", "Urgence")).post("/api/v1/tenants").then().statusCode(200).extract().path("id");
        String site = admin().body(Map.of("tenantId", tenant, "code", "URG", "name", "Pharmacie Urgence")).post("/api/v1/sites").then().statusCode(200).extract().path("id");
        topo = admin().body(Map.of("siteId", site, "name", "prod-urgence")).post("/api/v1/topologies").then().statusCode(200).extract().path("id");
        String[][] nodes = {{"A", "PRIMARY", "10.0.1.1:3306"}, {"B", "REPLICA", "10.0.1.2:3306"}, {"C", "REPLICA", "10.0.1.3:3306"}};
        for (String[] n : nodes) {
            var r = admin().body(Map.of("siteId", site, "topologyId", topo, "name", n[0], "expectedRole", n[1], "addressLabel", n[2]))
                    .post("/api/v1/nodes/register").then().statusCode(200).extract();
            ids.put(n[0], r.path("nodeId"));
            tokens.put(n[0], r.path("agentToken"));
        }
        for (String u : List.of("op.urgence", "appro.urgence")) {
            admin().body(Map.of("username", u, "password", "mot-de-passe-tres-long", "role", u.startsWith("op") ? "OPERATOR" : "APPROVER", "tenantId", tenant))
                    .post("/api/v1/users").then().statusCode(200);
        }
        primary("A", 1, "0-1-500", false);
        replica("B", 2, "0-1-480");
        replica("C", 3, "0-1-497");
    }

    @Test @Order(2)
    void refusedWhilePrimaryAnswers() {
        as("op.urgence").body(Map.of()).post("/api/v1/topologies/" + topo + "/failover/plan").then().statusCode(200)
                .body("ready", is(false)).body("checks.find { it.id == 'primary-alive' }.status", is("FAIL"));
    }

    @Test @Order(3)
    void planPicksMostAdvancedReplicaAndEstimatesLoss() {
        down("A"); // l'agent répond, MariaDB non
        as("op.urgence").body(Map.of()).post("/api/v1/topologies/" + topo + "/failover/plan").then().statusCode(200)
                .body("ready", is(true)).body("target", is("C")).body("oldAgentAlive", is(true)).body("manualFencingRequired", is(false))
                .body("lossEstimate", containsString("Au moins 3 transaction")).body("repointed", contains("B"));
        as("op.urgence").body(Map.of("targetNodeId", ids.get("B"))).post("/api/v1/topologies/" + topo + "/failover/plan").then()
                .body("checks.find { it.id == 'target-best' }.status", is("WARN"));
    }

    @Test @Order(4)
    void fenceFailureWithoutConfirmationChangesNothing() {
        as("op.urgence").body(Map.of("topologyConfirmation", "mauvais")).post("/api/v1/topologies/" + topo + "/failover").then().statusCode(400);
        String op = request(Map.of("topologyConfirmation", "prod-urgence"));
        Map<String, Object> p = run("A", "FENCE", "FAILED", Map.of("fenced", false));
        Assertions.assertEquals(true, p.get("stopService"));
        admin().get("/api/v1/operations/" + op).then().body("operation.status", is("FAILED"))
                .body("operation.error", containsString("Rien n'a été modifié"));
        agent("C").get("/api/v1/agent/jobs").then().body("size()", is(0));
        admin().get("/api/v1/nodes/" + ids.get("A")).then().body("summary.expectedRole", is("PRIMARY"));
        admin().get("/api/v1/topologies").then().body("find { it.id == '" + topo + "' }.maintenance", is(false));
    }

    @Test @Order(5)
    void failoverFencesPromotesAndRepoints() {
        down("A");
        String op = request(Map.of("topologyConfirmation", "prod-urgence"));
        admin().get("/api/v1/topologies").then().body("find { it.id == '" + topo + "' }.maintenance", is(true));
        agent("C").get("/api/v1/agent/jobs").then().body("size()", is(0)); // la promotion attend l'isolement
        run("A", "FENCE", "SUCCEEDED", Map.of("fenced", true, "mariadbDown", true, "serviceStopped", true));
        Map<String, Object> pp = run("C", "SWITCHOVER_PROMOTE", "SUCCEEDED", Map.of("promoted", true, "gtid", "0-1-497"));
        Assertions.assertEquals(true, pp.get("waitRelay"));
        admin().get("/api/v1/nodes/" + ids.get("C")).then().body("summary.expectedRole", is("PRIMARY"));
        admin().get("/api/v1/nodes/" + ids.get("A")).then().body("summary.expectedRole", is("REPLICA"));
        Map<String, Object> pa = run("B", "REPLICA_ATTACH", "SUCCEEDED", Map.of());
        Assertions.assertEquals("10.0.1.3", pa.get("sourceHost"));
        agent("A").get("/api/v1/agent/jobs").then().body("size()", is(0)); // jamais rattaché tel quel
        admin().get("/api/v1/operations/" + op).then().body("operation.status", is("SUCCEEDED"))
                .body("operation.result", containsString("lossEstimate")).body("steps.size()", is(3));
        admin().get("/api/v1/topologies").then().body("find { it.id == '" + topo + "' }.maintenance", is(false));
        admin().get("/api/v1/audit?action=FAILOVER_SUCCEEDED").then().body("size()", greaterThanOrEqualTo(1));
        as("op.urgence").get("/api/v1/nodes/" + ids.get("A") + "/repair-plan").then()
                .body("diagnosis", containsString("isolé")).body("actions.id", contains("REBUILD"));
    }

    @Test @Order(6)
    void fencedServerComingBackWritableIsForcedReadOnly() {
        primary("A", 1, "0-1-503", false); // redémarré à la main, en écriture
        Map<String, Object> p = run("A", "FENCE", "SUCCEEDED", Map.of("fenced", true, "readOnly", true));
        Assertions.assertEquals(false, p.get("stopService"));
        admin().get("/api/v1/incidents?status=open").then()
                .body("findAll { it.nodeId == '" + ids.get("A") + "' }.type", hasItem("NODE_FENCED"));
        primary("A", 1, "0-1-503", true);
        agent("A").get("/api/v1/agent/jobs").then().body("size()", is(0));
    }

    @Test @Order(7)
    void manualFailoverDoneWithoutConsoleCanBeDeclared() {
        String b = ids.get("B");
        Map<String, Object> body = new HashMap<>(Map.of("newPrimaryNodeId", b, "topologyConfirmation", "prod-urgence"));
        as("op.urgence").body(body).post("/api/v1/topologies/" + topo + "/failover/declare-manual").then().statusCode(403);
        admin().body(body).post("/api/v1/topologies/" + topo + "/failover/declare-manual").then().statusCode(400)
                .body("message", containsString("ne se présente pas comme primaire"));
        down("C");
        primary("B", 2, "0-2-12", false); // promu à la main avec « prm-agent promote »
        admin().body(body).post("/api/v1/topologies/" + topo + "/failover/declare-manual").then().statusCode(204);
        admin().get("/api/v1/nodes/" + b).then().body("summary.expectedRole", is("PRIMARY"));
        admin().get("/api/v1/nodes/" + ids.get("C")).then().body("summary.expectedRole", is("REPLICA")).body("summary.fenceRequired", is(true));
        admin().get("/api/v1/audit?action=FAILOVER_MANUAL_DECLARED").then().body("size()", is(1));
    }
}
