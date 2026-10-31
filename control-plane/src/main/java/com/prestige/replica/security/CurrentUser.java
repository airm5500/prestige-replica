package com.prestige.replica.security;

import com.prestige.replica.domain.AppUser;
import com.prestige.replica.domain.Roles;
import io.quarkus.security.identity.SecurityIdentity;
import jakarta.enterprise.context.RequestScoped;
import jakarta.inject.Inject;
import jakarta.ws.rs.ForbiddenException;
import jakarta.ws.rs.NotFoundException;
import java.util.UUID;

/**
 * Périmètre de l'utilisateur courant. Toutes les vérifications d'isolation
 * tenant/site sont faites ici, côté serveur, indépendamment de l'interface.
 */
@RequestScoped
public class CurrentUser {
    @Inject SecurityIdentity identity;
    private AppUser user;

    public AppUser user() {
        if (user == null) {
            user = AppUser.findByUsername(identity.getPrincipal().getName());
            if (user == null || !user.enabled) throw new ForbiddenException("Compte désactivé");
        }
        return user;
    }

    public String username() { return identity.isAnonymous() ? "anonyme" : identity.getPrincipal().getName(); }

    public boolean isGlobal() { return user().tenantId == null && Roles.SUPER_ADMIN.equals(user().role); }

    /** Tenant imposé à l'utilisateur, ou null pour un super-administrateur global. */
    public UUID tenantScope() { return isGlobal() ? null : user().tenantId; }

    /** Site imposé à l'utilisateur, ou null s'il voit tous les sites de son tenant. */
    public UUID siteScope() { return user().siteId; }

    /**
     * Vérifie l'accès à une ressource. Renvoie 404 plutôt que 403 pour ne pas
     * révéler l'existence de ressources d'un autre tenant.
     */
    public void checkAccess(UUID tenantId, UUID siteId) {
        if (isGlobal()) return;
        if (user().tenantId == null || !user().tenantId.equals(tenantId)) throw new NotFoundException();
        if (user().siteId != null && siteId != null && !user().siteId.equals(siteId)) throw new NotFoundException();
    }

    public boolean hasRole(String role) { return identity.hasRole(role); }

    /** Clause JPQL et paramètres limitant une requête au périmètre de l'utilisateur. */
    public record ScopeFilter(String clause, java.util.Map<String, Object> params) {}

    public ScopeFilter filter(String siteField) {
        java.util.Map<String, Object> p = new java.util.HashMap<>();
        StringBuilder sb = new StringBuilder("1 = 1");
        if (tenantScope() != null) {
            sb.append(" and tenantId = :scopeTenant");
            p.put("scopeTenant", tenantScope());
        } else if (!isGlobal()) {
            sb.append(" and 1 = 0");
        }
        if (siteScope() != null && siteField != null) {
            sb.append(" and ").append(siteField).append(" = :scopeSite");
            p.put("scopeSite", siteScope());
        }
        return new ScopeFilter(sb.toString(), p);
    }
}
