package com.prestige.replica;

import static io.restassured.RestAssured.given;

import com.prestige.replica.domain.HealthIncident;
import com.prestige.replica.domain.IncidentStatus;
import io.quarkus.narayana.jta.QuarkusTransaction;
import io.quarkus.test.junit.QuarkusTest;
import io.restassured.http.ContentType;
import java.time.Instant;
import java.util.*;
import java.util.concurrent.*;
import org.junit.jupiter.api.Assertions;
import org.junit.jupiter.api.Test;

/** Rapports simultanés d'un même nœud : jamais d'erreur 500 ni d'incident en double. */
@QuarkusTest
class ConcurrentReportsTest {

    @Test
    void simultaneousReportsOpenOneIncident() throws Exception {
        var admin = given().auth().preemptive().basic("admin", "test-admin-password").contentType(ContentType.JSON);
        String tenant = admin.body(Map.of("code", "concur-t", "name", "Concurrence")).post("/api/v1/tenants").then().statusCode(200).extract().path("id");
        String site = given().auth().preemptive().basic("admin", "test-admin-password").contentType(ContentType.JSON)
                .body(Map.of("tenantId", tenant, "code", "CCR", "name", "Pharmacie Concurrence")).post("/api/v1/sites").then().statusCode(200).extract().path("id");
        var reg = given().auth().preemptive().basic("admin", "test-admin-password").contentType(ContentType.JSON)
                .body(Map.of("siteId", site, "name", "srv", "expectedRole", "PRIMARY", "addressLabel", "10.0.9.1:3306"))
                .post("/api/v1/nodes/register").then().statusCode(200).extract();
        String token = reg.path("agentToken");
        String nodeId = reg.path("nodeId");
        Map<String, Object> r = Map.of("schemaVersion", 1, "agentVersion", "0.5.0", "collectedAt", Instant.now().toString(),
                "reachable", false, "capabilities", List.of(), "channels", List.of(), "disks", List.of(), "errors", List.of("connexion refusée"));
        ExecutorService pool = Executors.newFixedThreadPool(8);
        List<Future<Integer>> codes = new ArrayList<>();
        CountDownLatch go = new CountDownLatch(1);
        for (int i = 0; i < 8; i++) {
            codes.add(pool.submit(() -> {
                go.await();
                return given().header("Authorization", "Bearer " + token).contentType(ContentType.JSON)
                        .body(Map.of("reports", List.of(r))).post("/api/v1/agent/reports").statusCode();
            }));
        }
        go.countDown();
        for (Future<Integer> c : codes) Assertions.assertEquals(200, c.get(30, TimeUnit.SECONDS));
        pool.shutdown();
        long open = QuarkusTransaction.requiringNew().call(() -> HealthIncident.count("nodeId = ?1 and status <> ?2",
                UUID.fromString(nodeId), IncidentStatus.RESOLVED));
        Assertions.assertTrue(open >= 1, "un incident attendu");
        long distinctTypes = QuarkusTransaction.requiringNew().call(() -> HealthIncident.<HealthIncident>list("nodeId = ?1 and status <> ?2",
                UUID.fromString(nodeId), IncidentStatus.RESOLVED).stream().map(i -> i.type).distinct().count());
        Assertions.assertEquals(distinctTypes, open, "aucun incident en double");
    }
}
