package com.prestige.replica;

import static io.restassured.RestAssured.given;
import static org.hamcrest.Matchers.*;

import com.prestige.replica.alert.Finding;
import com.prestige.replica.domain.Site;
import com.prestige.replica.domain.Topology;
import com.prestige.replica.service.BackupService;
import com.prestige.replica.service.ConsistencyService;
import io.quarkus.narayana.jta.QuarkusTransaction;
import io.quarkus.test.junit.QuarkusTest;
import io.restassured.http.ContentType;
import io.restassured.specification.RequestSpecification;
import jakarta.inject.Inject;
import java.time.Duration;
import java.time.Instant;
import java.time.ZonedDateTime;
import java.util.*;
import org.junit.jupiter.api.Assertions;
import org.junit.jupiter.api.MethodOrderer;
import org.junit.jupiter.api.Order;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.TestMethodOrder;

/** Contrôles de cohérence primaire / réplique : demande, réglages, planification, alertes, plans de bascule. */
@QuarkusTest
@TestMethodOrder(MethodOrderer.OrderAnnotation.class)
class ConsistencyFlowTest {
    static String topo;
    static final Map<String, String> ids = new HashMap<>(), tokens = new HashMap<>();
    @Inject ConsistencyService consistency;

    static RequestSpecification admin() {
        return given().auth().preemptive().basic("admin", "test-admin-password").contentType(ContentType.JSON);
    }
    static RequestSpecification as(String u) {
        return given().auth().preemptive().basic(u, "mot-de-passe-tres-long").contentType(ContentType.JSON);
    }

    List<Finding> findings(Instant at) {
        return QuarkusTransaction.requiringNew().call(() -> consistency.findings(Topology.findById(UUID.fromString(topo)), at));
    }

    static Map<String, Object> summary(int divergent, int schema) {
        return Map.of("tablesChecked", 5, "divergentTables", divergent, "inconclusiveTables", 0, "skippedTables", 0, "schemaDifferences", schema);
    }

    @Test @Order(1)
    void setup() {
        String tenant = admin().body(Map.of("code", "coherence-t", "name", "Cohérence")).post("/api/v1/tenants").then().statusCode(200).extract().path("id");
        String site = admin().body(Map.of("tenantId", tenant, "code", "COH", "name", "Pharmacie Cohérence")).post("/api/v1/sites").then().statusCode(200).extract().path("id");
        topo = admin().body(Map.of("siteId", site, "name", "prod-coherence")).post("/api/v1/topologies").then().statusCode(200).extract().path("id");
        String[][] nodes = {{"A", "PRIMARY", "10.0.4.1:3306"}, {"B", "REPLICA", "10.0.4.2:3306"}, {"C", "REPLICA", "10.0.4.3:3306"}};
        for (String[] n : nodes) {
            var r = admin().body(Map.of("siteId", site, "topologyId", topo, "name", n[0], "expectedRole", n[1], "addressLabel", n[2]))
                    .post("/api/v1/nodes/register").then().statusCode(200).extract();
            ids.put(n[0], r.path("nodeId"));
            tokens.put(n[0], r.path("agentToken"));
        }
        admin().body(Map.of("username", "op.coh", "password", "mot-de-passe-tres-long", "role", "OPERATOR", "tenantId", tenant))
                .post("/api/v1/users").then().statusCode(200);
        admin().body(Map.of("username", "lect.coh", "password", "mot-de-passe-tres-long", "role", "READ_ONLY", "tenantId", tenant))
                .post("/api/v1/users").then().statusCode(200);
        BinlogFlowTest.tokens.putAll(Map.of("COH-A", tokens.get("A"), "COH-B", tokens.get("B"), "COH-C", tokens.get("C")));
        BinlogFlowTest.report("COH-A", 1, null, List.of());
        BinlogFlowTest.report("COH-B", 2, "10.0.4.1:3306", List.of("consistencyCheck"));
        BinlogFlowTest.report("COH-C", 3, "10.0.4.1:3306", List.of());
    }

    @Test @Order(2)
    void requestAndRefusals() {
        // Sur le primaire : refusé ; réplique sans la capacité : refusé ; lecteur : interdit.
        as("op.coh").body(Map.of()).post("/api/v1/nodes/" + ids.get("A") + "/consistency-check").then().statusCode(400)
                .body("message", containsString("réplique"));
        as("op.coh").body(Map.of()).post("/api/v1/nodes/" + ids.get("C") + "/consistency-check").then().statusCode(400)
                .body("message", containsString("consistencyCheck"));
        as("lect.coh").body(Map.of()).post("/api/v1/nodes/" + ids.get("B") + "/consistency-check").then().statusCode(403);
        as("op.coh").body(Map.of("mode", "TOUT")).post("/api/v1/nodes/" + ids.get("B") + "/consistency-check").then().statusCode(400);
        as("op.coh").body(Map.of("startAfter", "x; drop")).post("/api/v1/nodes/" + ids.get("B") + "/consistency-check").then().statusCode(400);

        as("op.coh").body(Map.of("mode", "SCHEMA")).post("/api/v1/nodes/" + ids.get("B") + "/consistency-check").then().statusCode(200)
                .body("type", is("CONSISTENCY_CHECK")).body("status", is("QUEUED"));
        @SuppressWarnings("unchecked")
        Map<String, Object> p = BinlogFlowTest.run("COH-B", "CONSISTENCY_CHECK", "SUCCEEDED", Map.of("status", "OK", "mode", "SCHEMA",
                "summary", summary(0, 0), "completed", true));
        Assertions.assertEquals("10.0.4.1", p.get("sourceHost"));
        Assertions.assertEquals(3306, ((Number) p.get("sourcePort")).intValue());
        Assertions.assertEquals("SCHEMA", p.get("mode"));
        as("lect.coh").get("/api/v1/topologies/" + topo + "/consistency").then().statusCode(200)
                .body("replicas.size()", is(2)).body("replicas.find { it.node == 'B' }.status", is("OK"))
                .body("replicas.find { it.node == 'B' }.lastOkAt", nullValue()) // schéma seul : ne vaut pas contrôle complet
                .body("replicas.find { it.node == 'C' }.status", is("NONE"));
    }

    @Test @Order(3)
    void policyValidation() {
        Map<String, Object> pol = new HashMap<>(Map.of("consistencyEnabled", true, "consistencyWeekday", 7, "consistencyHour", 4,
                "consistencyMode", "FULL", "consistencyMaxAgeDays", 30));
        pol.put("consistencyExclude", "prestige.journal; prestige");
        admin().body(pol).put("/api/v1/topologies/" + topo + "/consistency-policy").then().statusCode(400).body("message", containsString("Exclusion"));
        pol.put("consistencyExclude", "prestige.journal, archives.*");
        pol.put("consistencyDatabases", "mysql");
        admin().body(pol).put("/api/v1/topologies/" + topo + "/consistency-policy").then().statusCode(400);
        pol.put("consistencyDatabases", "prestige");
        as("op.coh").body(pol).put("/api/v1/topologies/" + topo + "/consistency-policy").then().statusCode(403);
        admin().body(pol).put("/api/v1/topologies/" + topo + "/consistency-policy").then().statusCode(200)
                .body("consistencyExclude", is("prestige.journal, archives.*")).body("consistencyEnabled", is(true));
    }

    @Test @Order(4)
    void scheduledWeekly() {
        Instant now = Instant.now();
        ZonedDateTime local = QuarkusTransaction.requiringNew().call(() ->
                now.atZone(BackupService.zone(Site.findById(Topology.<Topology>findById(UUID.fromString(topo)).siteId))));
        // Un autre jour : rien.
        Map<String, Object> pol = new HashMap<>(Map.of("consistencyEnabled", true, "consistencyWeekday", local.getDayOfWeek().getValue() % 7 + 1,
                "consistencyHour", local.getHour(), "consistencyMode", "FULL", "consistencyMaxAgeDays", 30, "consistencyDatabases", "prestige",
                "consistencyExclude", "prestige.journal"));
        admin().body(pol).put("/api/v1/topologies/" + topo + "/consistency-policy").then().statusCode(200);
        consistency.schedule(now);
        BinlogFlowTest.noJob("COH-B");
        // Le bon jour, à la bonne heure : un contrôle complet de B (seule réplique capable), une seule fois par jour.
        pol.put("consistencyWeekday", local.getDayOfWeek().getValue());
        admin().body(pol).put("/api/v1/topologies/" + topo + "/consistency-policy").then().statusCode(200);
        consistency.schedule(now);
        consistency.schedule(now);
        BinlogFlowTest.noJob("COH-C");
        Map<String, Object> result = new HashMap<>(Map.of("status", "DIVERGENT", "mode", "FULL", "summary", summary(1, 1), "completed", true));
        result.put("tables", List.of(Map.of("table", "prestige.clients", "status", "DIVERGENT",
                "divergences", List.of(Map.of("lower", "[40]", "upper", "[50]", "keys", List.of(Map.of("key", "[42]", "issue", "DIFFERENT")))))));
        Map<String, Object> p = BinlogFlowTest.run("COH-B", "CONSISTENCY_CHECK", "SUCCEEDED", result);
        Assertions.assertEquals(List.of("prestige"), p.get("databases"));
        Assertions.assertEquals(List.of("prestige.journal"), p.get("exclude"));
        Assertions.assertEquals("FULL", p.get("mode"));
        consistency.schedule(now);
        BinlogFlowTest.noJob("COH-B");
    }

    @Test @Order(5)
    void divergenceAlertsAndBlocksNothingButWarns() {
        List<Finding> f = findings(Instant.now());
        Finding div = f.stream().filter(x -> x.type().equals("CONSISTENCY_DIVERGENT")).findFirst().orElseThrow();
        Assertions.assertEquals("B", div.key());
        Assertions.assertTrue(div.message().contains("1 table"));
        // Le plan de bascule vers B avertit, sans bloquer.
        as("op.coh").body(Map.of("targetNodeId", ids.get("B"))).post("/api/v1/topologies/" + topo + "/switchover/plan").then().statusCode(200)
                .body("checks.find { it.id == 'target-consistency' }.status", is("WARN"));
        // Pas de contrôle réussi depuis trop longtemps : B a son dernier OK il y a 0 jour, donc pas d'alerte aujourd'hui ;
        // dans 31 jours, l'alerte « en retard » ne double pas l'alerte de divergence.
        Assertions.assertTrue(findings(Instant.now().plus(Duration.ofDays(31))).stream().noneMatch(x -> x.type().equals("CONSISTENCY_OVERDUE")));
    }

    @Test @Order(6)
    void cleanCheckClosesAndOverdueLater() {
        // Une reprise partielle sans écart ne fait pas oublier l'écart trouvé avant elle.
        as("op.coh").body(Map.of("startAfter", "prestige.clients")).post("/api/v1/nodes/" + ids.get("B") + "/consistency-check").then().statusCode(200);
        Map<String, Object> resumed = BinlogFlowTest.run("COH-B", "CONSISTENCY_CHECK", "SUCCEEDED", Map.of("status", "OK", "mode", "FULL", "summary", summary(0, 0), "completed", true));
        Assertions.assertEquals("prestige.clients", resumed.get("startAfter"));
        Assertions.assertTrue(findings(Instant.now()).stream().anyMatch(x -> x.type().equals("CONSISTENCY_DIVERGENT")));
        as("op.coh").body(Map.of()).post("/api/v1/nodes/" + ids.get("B") + "/consistency-check").then().statusCode(200);
        BinlogFlowTest.run("COH-B", "CONSISTENCY_CHECK", "SUCCEEDED", Map.of("status", "OK", "mode", "FULL", "summary", summary(0, 0), "completed", true));
        Assertions.assertTrue(findings(Instant.now()).isEmpty());
        List<Finding> late = findings(Instant.now().plus(Duration.ofDays(31)));
        Assertions.assertEquals(List.of("CONSISTENCY_OVERDUE"), late.stream().map(Finding::type).toList());
    }
}
