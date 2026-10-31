package com.prestige.replica.service;

import com.prestige.replica.domain.AuditEvent;
import io.quarkus.hibernate.orm.panache.Panache;
import jakarta.enterprise.context.ApplicationScoped;
import jakarta.transaction.Transactional;
import java.time.Instant;
import java.time.temporal.ChronoUnit;
import java.util.List;
import java.util.UUID;

/**
 * Journal d'audit append-only. Chaque entrée contient l'empreinte de la
 * précédente : toute modification ou suppression rompt la chaîne et est
 * détectée par {@link #verify()}. La base refuse en outre UPDATE/DELETE.
 */
@ApplicationScoped
public class AuditService {
    public static final String GENESIS = "0".repeat(64);

    public record Verification(boolean valid, long checked, Long firstBrokenId) {}

    @Transactional(Transactional.TxType.REQUIRED)
    public AuditEvent record(String actor, String action, String resource, String result, UUID tenantId, UUID siteId, String details) {
        // Sérialise les écritures pour garantir une chaîne linéaire.
        Panache.getEntityManager().createNativeQuery("SELECT pg_advisory_xact_lock(424242)").getSingleResult();
        AuditEvent last = AuditEvent.find("ORDER BY id DESC").firstResult();
        AuditEvent e = new AuditEvent();
        e.at = Instant.now().truncatedTo(ChronoUnit.MICROS);
        e.actor = actor == null ? "system" : actor;
        e.action = action;
        e.resource = resource;
        e.result = result;
        e.tenantId = tenantId;
        e.siteId = siteId;
        e.details = details;
        e.prevHash = last == null ? GENESIS : last.hash;
        e.hash = hash(e);
        e.persist();
        return e;
    }

    public static String hash(AuditEvent e) {
        String canonical = String.join("\u001f",
                e.prevHash, e.at.toString(), nz(e.tenantId), nz(e.siteId), e.actor, e.action,
                nz(e.resource), e.result, nz(e.details));
        return Tokens.sha256(canonical);
    }

    private static String nz(Object o) { return o == null ? "" : o.toString(); }

    @Transactional
    public Verification verify() {
        String prev = GENESIS;
        long n = 0;
        long lastId = 0;
        while (true) {
            List<AuditEvent> page = AuditEvent.find("id > ?1 ORDER BY id", lastId).page(0, 1000).list();
            if (page.isEmpty()) break;
            for (AuditEvent e : page) {
                n++;
                if (!e.prevHash.equals(prev) || !e.hash.equals(hash(e))) {
                    return new Verification(false, n, e.id);
                }
                prev = e.hash;
                lastId = e.id;
            }
        }
        return new Verification(true, n, null);
    }
}
