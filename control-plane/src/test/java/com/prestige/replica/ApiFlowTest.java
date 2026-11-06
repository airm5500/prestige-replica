package com.prestige.replica;

import static io.restassured.RestAssured.given;
import static org.hamcrest.Matchers.*;
import static org.junit.jupiter.api.Assertions.*;

import io.quarkus.test.junit.QuarkusTest;
import io.restassured.http.ContentType;
import io.restassured.specification.RequestSpecification;
import jakarta.inject.Inject;
import java.time.Instant;
import java.util.List;
import java.util.Map;
import javax.sql.DataSource;
import org.junit.jupiter.api.MethodOrderer;
import org.junit.jupiter.api.Order;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.TestMethodOrder;

/** Parcours complet du Lot 1 contre PostgreSQL réel. */
@QuarkusTest
@TestMethodOrder(MethodOrderer.OrderAnnotation.class)
class ApiFlowTest {
    @Inject DataSource ds;

    static String tenantA, tenantB, siteA, siteB, topo, nodeId, token;

    static RequestSpecification admin() {
        return given().auth().preemptive().basic("admin", "test-admin-password").contentType(ContentType.JSON);
    }

    static Map<String, Object> report(String sqlRunning, long sqlErrno) {
        Map<String, Object> m = new java.util.HashMap<>();
        m.put("schemaVersion", 1);
        m.put("agentVersion", "0.1.0");
        m.put("collectedAt", Instant.now().toString());
        m.put("hostname", "pharma-b");
        m.put("os", "linux");
        m.put("arch", "amd64");
        m.put("reachable", true);
        m.put("server", Map.of("version", "10.11.14-MariaDB", "serverId", 2, "readOnly", true, "logBin", true,
                "binlogFormat", "ROW", "gtidCurrentPos", "0-1-42", "gtidSlavePos", "0-1-42"));
        m.put("channels", List.of(Map.of("connectionName", "", "masterHost", "10.0.0.1", "masterPort", 3306,
                "ioRunning", "Yes", "sqlRunning", sqlRunning, "secondsBehindMaster", 0, "usingGtid", "Slave_Pos",
                "lastSqlErrno", sqlErrno, "lastSqlError", sqlErrno == 0 ? "" : "Duplicate entry '7' for key 'PRIMARY'")));
        m.put("disks", List.of(Map.of("path", "/var/lib/mysql", "totalBytes", 1000, "freeBytes", 500)));
        m.put("errors", List.of());
        return m;
    }

    @Test @Order(1)
    void healthEndpoints() {
        given().get("/health/live").then().statusCode(200);
        given().get("/health/ready").then().statusCode(200);
        given().get("/api/v1/fleet").then().statusCode(401);
    }

    @Test @Order(2)
    void inventory() {
        tenantA = admin().body(Map.of("code", "groupe-a", "name", "Groupe A")).post("/api/v1/tenants").then().statusCode(200).extract().path("id");
        tenantB = admin().body(Map.of("code", "groupe-b", "name", "Groupe B")).post("/api/v1/tenants").then().statusCode(200).extract().path("id");
        siteA = admin().body(Map.of("tenantId", tenantA, "code", "ph01", "name", "Pharmacie du Centre")).post("/api/v1/sites")
                .then().statusCode(200).body("code", is("PH01")).extract().path("id");
        siteB = admin().body(Map.of("tenantId", tenantB, "code", "ph02", "name", "Pharmacie du Port")).post("/api/v1/sites").then().statusCode(200).extract().path("id");
        topo = admin().body(Map.of("siteId", siteA, "name", "prestige-prod", "mode", "A")).post("/api/v1/topologies").then().statusCode(200).extract().path("id");
        var reg = admin().body(Map.of("siteId", siteA, "topologyId", topo, "name", "replica-1", "expectedRole", "REPLICA"))
                .post("/api/v1/nodes/register").then().statusCode(200).body("agentToken", startsWith("prm_")).extract();
        nodeId = reg.path("nodeId");
        token = reg.path("agentToken");
        // Le jeton n'est jamais restitué ensuite.
        admin().get("/api/v1/nodes/" + nodeId).then().statusCode(200).body(not(containsString(token)));
    }

    @Test @Order(3)
    void agentRejectsBadToken() {
        given().contentType(ContentType.JSON).header("Authorization", "Bearer prm_faux").body(Map.of("reports", List.of(report("Yes", 0))))
                .post("/api/v1/agent/reports").then().statusCode(401);
    }

    @Test @Order(4)
    void sqlThreadStopOpensThenResolvesIncident() {
        given().contentType(ContentType.JSON).header("Authorization", "Bearer " + token)
                .body(Map.of("reports", List.of(report("No", 1062)))).post("/api/v1/agent/reports")
                .then().statusCode(200).body("accepted", is(1));
        admin().get("/api/v1/incidents").then().statusCode(200)
                .body("type", hasItems("SQL_THREAD_STOPPED", "REPLICATION_SQL_ERROR"))
                .body("severity", everyItem(is("CRITICAL")));
        admin().get("/api/v1/fleet").then().statusCode(200)
                .body("find { it.code == 'PH01' }.status", is("CRITICAL"));

        String incId = admin().get("/api/v1/incidents").then().extract().path("[0].id");
        admin().body(Map.of("assignee", "astreinte")).post("/api/v1/incidents/" + incId + "/acknowledge")
                .then().statusCode(200).body("status", is("ACKNOWLEDGED")).body("assignee", is("astreinte"));

        given().contentType(ContentType.JSON).header("Authorization", "Bearer " + token)
                .body(Map.of("reports", List.of(report("Yes", 0)))).post("/api/v1/agent/reports").then().statusCode(200);
        admin().get("/api/v1/incidents").then().statusCode(200).body("size()", is(0));
        admin().get("/api/v1/incidents?status=all").then().body("status", everyItem(is("RESOLVED")));
        admin().get("/api/v1/fleet").then().body("find { it.code == 'PH01' }.status", is("HEALTHY"))
                .body("find { it.code == 'PH01' }.topologies[0].rpoSecondsEstimate", is(0));
    }

    @Test @Order(5)
    void tenantIsolation() {
        admin().body(Map.of("username", "gerant.b", "password", "mot-de-passe-long-b", "role", "SITE_ADMIN",
                "tenantId", tenantB, "siteId", siteB)).post("/api/v1/users").then().statusCode(200);
        var b = given().auth().preemptive().basic("gerant.b", "mot-de-passe-long-b").contentType(ContentType.JSON);
        b.get("/api/v1/fleet").then().statusCode(200).body("code", contains("PH02"));
        given().auth().preemptive().basic("gerant.b", "mot-de-passe-long-b").get("/api/v1/nodes/" + nodeId).then().statusCode(404);
        given().auth().preemptive().basic("gerant.b", "mot-de-passe-long-b").get("/api/v1/incidents?status=all").then().body("size()", is(0));
        given().auth().preemptive().basic("gerant.b", "mot-de-passe-long-b").contentType(ContentType.JSON)
                .body(Map.of("siteId", siteA, "name", "intrus", "expectedRole", "PRIMARY"))
                .post("/api/v1/nodes/register").then().statusCode(404);
        given().auth().preemptive().basic("gerant.b", "mot-de-passe-long-b").contentType(ContentType.JSON)
                .body(Map.of("code", "x1", "name", "x")).post("/api/v1/tenants").then().statusCode(403);
    }

    @Test @Order(6)
    void auditChainIsValidAndImmutable() throws Exception {
        admin().get("/api/v1/audit").then().statusCode(200).body("action", hasItems("NODE_REGISTER", "INCIDENT_ACK", "SITE_CREATE"));
        admin().get("/api/v1/audit/verify").then().statusCode(200).body("valid", is(true));
        try (var c = ds.getConnection(); var st = c.createStatement()) {
            var ex = assertThrows(java.sql.SQLException.class, () -> st.executeUpdate("UPDATE audit_event SET actor = 'pirate'"));
            assertTrue(ex.getMessage().contains("ajout seul"));
        }
    }

    @Test @Order(7)
    void rotatedTokenRevokesOldOne() {
        String fresh = admin().post("/api/v1/nodes/" + nodeId + "/rotate-token").then().statusCode(200).extract().path("agentToken");
        assertNotEquals(token, fresh);
        given().contentType(ContentType.JSON).header("Authorization", "Bearer " + token)
                .body(Map.of("reports", List.of(report("Yes", 0)))).post("/api/v1/agent/reports").then().statusCode(401);
    }
}
