package com.prestige.replica.notify;

import com.fasterxml.jackson.databind.ObjectMapper;
import com.prestige.replica.domain.HealthIncident;
import com.prestige.replica.domain.NotificationChannel;
import com.prestige.replica.domain.Site;
import io.quarkus.mailer.Mail;
import io.quarkus.mailer.reactive.ReactiveMailer;
import jakarta.enterprise.context.ApplicationScoped;
import jakarta.inject.Inject;
import java.net.URI;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;
import java.time.Duration;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.concurrent.CompletableFuture;
import org.jboss.logging.Logger;

/**
 * Envoi des notifications. Les connecteurs sont remplaçables : courriel SMTP
 * local, ou webhook (passerelle SMS/WhatsApp, outil interne). L'envoi est
 * asynchrone et n'interrompt jamais l'ingestion des rapports.
 */
@ApplicationScoped
public class Notifier {
    private static final Logger LOG = Logger.getLogger(Notifier.class);

    @Inject ReactiveMailer mailer;
    @Inject ObjectMapper json;

    private final HttpClient http = HttpClient.newBuilder().connectTimeout(Duration.ofSeconds(5)).build();

    public enum Event { OPENED, ESCALATED, RESOLVED }

    /** À appeler dans une transaction : lit les canaux puis envoie en arrière-plan. */
    public void notify(HealthIncident inc, Event event) {
        List<NotificationChannel> channels = NotificationChannel
                .list("tenantId = ?1 and enabled = true and (siteId is null or siteId = ?2)", inc.tenantId, inc.siteId);
        if (channels.isEmpty()) return;
        Site site = Site.findById(inc.siteId);
        String siteLabel = site == null ? "?" : site.name + " [" + site.code + "]";
        String subject = String.format("[PRM %s] %s — %s", inc.severity, label(event), siteLabel);
        String body = String.format("%s%n%nSite : %s%nType : %s%nSévérité : %s%nDébut : %s%nIncident : %s%n",
                inc.message, siteLabel, inc.type, inc.severity, inc.openedAt, inc.id);
        Map<String, Object> payload = new LinkedHashMap<>();
        payload.put("event", event.name());
        payload.put("incidentId", inc.id.toString());
        payload.put("type", inc.type);
        payload.put("severity", inc.severity.name());
        payload.put("message", inc.message);
        payload.put("site", siteLabel);
        payload.put("openedAt", inc.openedAt.toString());
        for (NotificationChannel ch : channels) {
            if (event != Event.RESOLVED && !inc.severity.atLeast(ch.minSeverity)) continue;
            switch (ch.type) {
                case EMAIL -> mailer.send(Mail.withText(ch.target, subject, body))
                        .subscribe().with(v -> {}, e -> LOG.warnf("Échec courriel vers %s : %s", ch.target, e.getMessage()));
                case WEBHOOK -> sendWebhook(ch.target, payload);
            }
        }
    }

    private void sendWebhook(String url, Map<String, Object> payload) {
        CompletableFuture.runAsync(() -> {
            try {
                HttpRequest req = HttpRequest.newBuilder(URI.create(url)).timeout(Duration.ofSeconds(10))
                        .header("Content-Type", "application/json")
                        .POST(HttpRequest.BodyPublishers.ofString(json.writeValueAsString(payload))).build();
                HttpResponse<Void> resp = http.send(req, HttpResponse.BodyHandlers.discarding());
                if (resp.statusCode() >= 300) LOG.warnf("Webhook %s : HTTP %d", url, resp.statusCode());
            } catch (Exception e) {
                LOG.warnf("Échec webhook %s : %s", url, e.getMessage());
            }
        });
    }

    private static String label(Event e) {
        return switch (e) {
            case OPENED -> "Nouvel incident";
            case ESCALATED -> "Incident aggravé";
            case RESOLVED -> "Incident résolu";
        };
    }
}
