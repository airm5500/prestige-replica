package com.prestige.replica.service;

import com.prestige.replica.domain.AppUser;
import com.prestige.replica.domain.Roles;
import io.quarkus.elytron.security.common.BcryptUtil;
import io.quarkus.runtime.StartupEvent;
import jakarta.enterprise.context.ApplicationScoped;
import jakarta.enterprise.event.Observes;
import jakarta.inject.Inject;
import jakarta.transaction.Transactional;
import java.util.Optional;
import org.eclipse.microprofile.config.inject.ConfigProperty;
import org.jboss.logging.Logger;

/**
 * Crée le premier super-administrateur lorsque la base est vide. Le mot de
 * passe provient d'une variable d'environnement et n'est jamais journalisé.
 */
@ApplicationScoped
public class Bootstrap {
    private static final Logger LOG = Logger.getLogger(Bootstrap.class);

    @ConfigProperty(name = "prm.bootstrap.admin-username", defaultValue = "admin")
    String adminUsername;
    @ConfigProperty(name = "prm.bootstrap.admin-password")
    Optional<String> adminPassword;

    @Inject AuditService audit;

    @Transactional
    void onStart(@Observes StartupEvent ev) {
        if (AppUser.count() > 0) return;
        if (adminPassword.isEmpty() || adminPassword.get().length() < 12) {
            LOG.warn("Aucun utilisateur : définissez PRM_BOOTSTRAP_ADMIN_PASSWORD (12 caractères minimum) pour créer le premier administrateur");
            return;
        }
        AppUser u = new AppUser();
        u.username = adminUsername;
        u.passwordHash = BcryptUtil.bcryptHash(adminPassword.get());
        u.role = Roles.SUPER_ADMIN;
        u.displayName = "Administrateur initial";
        u.persist();
        audit.record("system", "USER_BOOTSTRAP", "user:" + u.username, "SUCCESS", null, null, null);
        LOG.infof("Super-administrateur initial « %s » créé", adminUsername);
    }
}
