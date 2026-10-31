package com.prestige.replica.security;

import com.prestige.replica.domain.DatabaseNode;
import com.prestige.replica.service.Tokens;
import io.vertx.ext.web.RoutingContext;
import jakarta.enterprise.context.ApplicationScoped;
import jakarta.ws.rs.NotAuthorizedException;
import javax.net.ssl.SSLPeerUnverifiedException;
import org.eclipse.microprofile.config.inject.ConfigProperty;

/**
 * Authentifie un agent par son jeton (Bearer) et, si configuré, exige un
 * certificat client TLS vérifié (mTLS). Chaque jeton est lié à un seul nœud,
 * donc à un seul site.
 */
@ApplicationScoped
public class AgentAuthenticator {
    @ConfigProperty(name = "prm.agent.require-client-certificate", defaultValue = "false")
    boolean requireClientCertificate;

    public DatabaseNode authenticate(String authorization, RoutingContext ctx) {
        if (authorization == null || !authorization.startsWith("Bearer ")) {
            throw new NotAuthorizedException("Bearer");
        }
        String token = authorization.substring(7).trim();
        DatabaseNode node = DatabaseNode.find("agentTokenHash", Tokens.sha256(token)).firstResult();
        if (node == null) {
            throw new NotAuthorizedException("Bearer");
        }
        if (requireClientCertificate) {
            try {
                if (ctx == null || ctx.request().sslSession() == null
                        || ctx.request().sslSession().getPeerCertificates().length == 0) {
                    throw new NotAuthorizedException("mTLS");
                }
            } catch (SSLPeerUnverifiedException e) {
                throw new NotAuthorizedException("mTLS");
            }
        }
        return node;
    }
}
