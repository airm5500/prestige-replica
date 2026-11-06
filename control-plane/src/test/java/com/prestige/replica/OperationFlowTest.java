package com.prestige.replica;

import static io.restassured.RestAssured.given;
import static org.hamcrest.Matchers.*;

import io.quarkus.test.junit.QuarkusTest;
import io.restassured.http.ContentType;
import io.restassured.specification.RequestSpecification;
import java.time.Instant;
import java.util.List;
import java.util.Map;
import org.junit.jupiter.api.MethodOrderer;
import org.junit.jupiter.api.Order;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.TestMethodOrder;

/** Lot 2 : sauvegarde, restauration de contrôle, construction de réplique avec double approbation. */
@QuarkusTest
@TestMethodOrder(MethodOrderer.OrderAnnotation.class)
class OperationFlowTest {
    static String tenant, site, topo, primary, replica, primaryToken, replicaToken, backupId, backupOp, provisionOp;

    static RequestSpecification admin() {
        return given().auth().preemptive().basic("admin", "test-admin-password").contentType(ContentType.JSON);
    }
    static RequestSpecification as(String u) {
        return given().auth().preemptive().basic(u, "mot-de-passe-tres-long").contentType(ContentType.JSON);
    }
    static RequestSpecification agent(String token) {
        return given().header("Authorization", "Bearer " + token).contentType(ContentType.JSON);
    }

    static void report(String token, long serverId) {
        agent(token).body(Map.of("reports", List.of(Map.of(
                "schemaVersion", 1, "agentVersion", "0.2.0", "collectedAt", Instant.now().toString(), "reachable", true,
                "server", Map.of("version", "10.6.18-MariaDB", "serverId", serverId, "logBin", true, "readOnly", serverId != 1),
                "channels", List.of(), "disks", List.of(), "errors", List.of()))))
                .post("/api/v1/agent/reports").then().statusCode(200);
    }

    /** Simule l'agent : récupère l'opération, la démarre et publie le résultat. */
    static String runJob(String token, String expectedType, String status, Map<String, Object> result) {
        var job = agent(token).get("/api/v1/agent/jobs").then().statusCode(200).body("size()", is(1))
                .body("[0].type", is(expectedType)).extract();
        String id = job.path("[0].id");
        agent(token).body(Map.of()).post("/api/v1/agent/jobs/" + id + "/start").then().statusCode(204);
        agent(token).body(Map.of()).post("/api/v1/agent/jobs/" + id + "/start").then().statusCode(409);
        agent(token).body(Map.of("level", "INFO", "message", "étape de test")).post("/api/v1/agent/jobs/" + id + "/events").then().statusCode(204);
        agent(token).body(Map.of("status", status, "error", status.equals("SUCCEEDED") ? "" : "échec simulé", "result", result))
                .post("/api/v1/agent/jobs/" + id + "/finish").then().statusCode(204);
        return id;
    }

    @Test @Order(1)
    void setup() {
        tenant = admin().body(Map.of("code", "ops-t", "name", "Ops")).post("/api/v1/tenants").then().statusCode(200).extract().path("id");
        site = admin().body(Map.of("tenantId", tenant, "code", "OPS1", "name", "Pharmacie Ops")).post("/api/v1/sites").then().statusCode(200).extract().path("id");
        topo = admin().body(Map.of("siteId", site, "name", "prod-ops")).post("/api/v1/topologies").then().statusCode(200).extract().path("id");
        var p = admin().body(Map.of("siteId", site, "topologyId", topo, "name", "srv-a", "expectedRole", "PRIMARY", "addressLabel", "192.168.1.10:3306"))
                .post("/api/v1/nodes/register").then().statusCode(200).extract();
        primary = p.path("nodeId"); primaryToken = p.path("agentToken");
        var r = admin().body(Map.of("siteId", site, "topologyId", topo, "name", "srv-b", "expectedRole", "REPLICA", "addressLabel", "192.168.1.11:3306"))
                .post("/api/v1/nodes/register").then().statusCode(200).extract();
        replica = r.path("nodeId"); replicaToken = r.path("agentToken");
        report(primaryToken, 1);
        report(replicaToken, 2);
        for (String u : List.of("operateur", "approbateur")) {
            admin().body(Map.of("username", u, "password", "mot-de-passe-tres-long", "role", u.equals("operateur") ? "OPERATOR" : "APPROVER", "tenantId", tenant))
                    .post("/api/v1/users").then().statusCode(200);
        }
    }

    @Test @Order(2)
    void preflightCarriesSuggestedServerIdAndSourceVersion() {
        as("operateur").post("/api/v1/nodes/" + replica + "/preflight").then().statusCode(200).body("status", is("QUEUED"));
        var job = agent(replicaToken).get("/api/v1/agent/jobs").then().extract();
        job.response().then().body("[0].params.role", is("REPLICA")).body("[0].params.suggestedServerId", is(2))
                .body("[0].params.sourceVersion", is("10.6.18-MariaDB"));
        // L'agent du primaire ne voit pas les opérations de la réplique.
        agent(primaryToken).get("/api/v1/agent/jobs").then().body("size()", is(0));
        runJob(replicaToken, "PREFLIGHT", "SUCCEEDED", Map.of("ready", true, "checks", List.of()));
    }

    @Test @Order(3)
    void policyAndBackupMissingAlert() {
        admin().body(Map.of("backupEnabled", true, "backupNodeId", replica, "restoreNodeId", replica, "backupHour", 2,
                "backupKeep", 2, "restoreTestWeekday", 7, "backupMaxAgeHours", 26)).put("/api/v1/topologies/" + topo + "/backup-policy").then().statusCode(200);
        report(replicaToken, 2); // déclenche l'évaluation de la topologie
        admin().get("/api/v1/incidents").then().body("type", hasItems("BACKUP_MISSING", "RESTORE_TEST_STALE"));
    }

    @Test @Order(4)
    void backupThenRestoreTest() {
        backupOp = as("operateur").post("/api/v1/nodes/" + replica + "/backups").then().statusCode(200).extract().path("id");
        as("operateur").post("/api/v1/nodes/" + replica + "/backups").then().statusCode(409); // pas de doublon
        var jobs = agent(replicaToken).get("/api/v1/agent/jobs").then().extract();
        String relDir = jobs.path("[0].params.relativeDir");
        org.junit.jupiter.api.Assertions.assertTrue(relDir.startsWith("tenants/" + tenant + "/sites/" + site + "/backups/"));
        runJob(replicaToken, "BACKUP_FULL", "SUCCEEDED", Map.of("manifest", Map.of(
                "sizeBytes", 123456, "sha256", "a".repeat(64), "keyId", "6688ecd16f1fde35", "gtid", "0-1-42", "serverVersion", "10.6.18-MariaDB")));
        backupId = admin().get("/api/v1/backups?topologyId=" + topo).then().statusCode(200)
                .body("[0].status", is("COMPLETED")).body("[0].gtid", is("0-1-42")).body("[0].verifyStatus", is("NOT_VERIFIED"))
                .extract().path("[0].id");
        admin().get("/api/v1/operations/" + backupOp).then().body("events.message", hasItem("étape de test"));

        // Construction refusée tant que la sauvegarde n'est pas prouvée restaurable.
        as("operateur").body(Map.of("targetNodeId", replica, "backupId", backupId, "confirmDataDir", "D:/MariaDB/data", "topologyConfirmation", "prod-ops"))
                .post("/api/v1/topologies/" + topo + "/replicas/provision").then().statusCode(400).body(containsString("non vérifiée"));

        as("operateur").body(Map.of()).post("/api/v1/backups/" + backupId + "/verify").then().statusCode(200);
        runJob(replicaToken, "RESTORE_TEST", "SUCCEEDED", Map.of("gtid", "0-1-42", "timings", Map.of("totalSeconds", 95)));
        admin().get("/api/v1/backups?topologyId=" + topo).then().body("[0].verifyStatus", is("VERIFIED")).body("[0].restoreSeconds", is(95));
        report(replicaToken, 2);
        admin().get("/api/v1/incidents").then().body("type", not(hasItems("BACKUP_MISSING")));
    }

    @Test @Order(5)
    void provisionNeedsTopologyNameAndIndependentApproval() {
        var body = new java.util.HashMap<String, Object>(Map.of("targetNodeId", replica, "backupId", backupId,
                "confirmDataDir", "D:/MariaDB/data", "topologyConfirmation", "mauvais-nom"));
        as("operateur").body(body).post("/api/v1/topologies/" + topo + "/replicas/provision").then().statusCode(400);
        body.put("targetNodeId", primary);
        body.put("topologyConfirmation", "prod-ops");
        as("operateur").body(body).post("/api/v1/topologies/" + topo + "/replicas/provision").then().statusCode(400)
                .body(containsString("réplique"));
        body.put("targetNodeId", replica);
        provisionOp = as("operateur").body(body).post("/api/v1/topologies/" + topo + "/replicas/provision").then().statusCode(200)
                .body("status", is("AWAITING_APPROVAL")).extract().path("id");
        // Rien n'est envoyé à l'agent avant approbation.
        agent(replicaToken).get("/api/v1/agent/jobs").then().body("size()", is(0));
        // Un exploitant ne peut pas approuver ; le demandeur non plus.
        as("operateur").body(Map.of("confirmation", "OPS1")).post("/api/v1/operations/" + provisionOp + "/approve").then().statusCode(403);
        as("approbateur").body(Map.of("confirmation", "MAUVAIS")).post("/api/v1/operations/" + provisionOp + "/approve").then().statusCode(400);
        as("approbateur").body(Map.of("confirmation", "ops1")).post("/api/v1/operations/" + provisionOp + "/approve").then().statusCode(200)
                .body("status", is("QUEUED")).body("approvedBy", is("approbateur"));
        var job = agent(replicaToken).get("/api/v1/agent/jobs").then().extract();
        job.response().then().body("[0].params.sourceHost", is("192.168.1.10")).body("[0].params.sourcePort", is(3306))
                .body("[0].params.sha256", is("a".repeat(64)));
        runJob(replicaToken, "PROVISION_REPLICA", "INTERVENTION_REQUIRED", Map.of());
        report(replicaToken, 2);
        admin().get("/api/v1/incidents").then().body("type", hasItem("OPERATION_INTERVENTION"));
        admin().get("/api/v1/audit?action=OPERATION_APPROVE").then().body("result", hasItems("DENIED", "SUCCESS"));
    }

    @Test @Order(6)
    void otherTenantCannotSeeOrApprove() {
        as("approbateur").get("/api/v1/operations?status=active").then().statusCode(200);
        // L'administrateur du test d'isolation n'existe que dans l'autre classe : on crée un approbateur d'un autre tenant.
        String other = admin().body(Map.of("code", "ops-x", "name", "Autre")).post("/api/v1/tenants").then().extract().path("id");
        admin().body(Map.of("username", "approb.autre", "password", "mot-de-passe-tres-long", "role", "APPROVER", "tenantId", other))
                .post("/api/v1/users").then().statusCode(200);
        as("approb.autre").get("/api/v1/operations/" + provisionOp).then().statusCode(404);
        as("approb.autre").get("/api/v1/backups").then().body("size()", is(0));
    }
}
