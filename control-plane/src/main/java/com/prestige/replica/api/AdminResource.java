package com.prestige.replica.api;

import com.prestige.replica.domain.*;
import com.prestige.replica.security.CurrentUser;
import com.prestige.replica.service.AuditService;
import io.quarkus.elytron.security.common.BcryptUtil;
import io.quarkus.security.Authenticated;
import jakarta.annotation.security.RolesAllowed;
import jakarta.inject.Inject;
import jakarta.transaction.Transactional;
import jakarta.validation.Valid;
import jakarta.ws.rs.*;
import jakarta.ws.rs.core.MediaType;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.UUID;

/** Utilisateurs, profil courant et canaux de notification. */
@Path("/api/v1")
@Produces(MediaType.APPLICATION_JSON)
@Consumes(MediaType.APPLICATION_JSON)
@Authenticated
public class AdminResource {
    @Inject CurrentUser me;
    @Inject AuditService audit;

    @GET @Path("/me") @Transactional
    public Map<String, Object> whoami() {
        AppUser u = me.user();
        Map<String, Object> m = new LinkedHashMap<>();
        m.put("username", u.username);
        m.put("displayName", u.displayName);
        m.put("role", u.role);
        m.put("tenantId", u.tenantId);
        m.put("siteId", u.siteId);
        m.put("global", me.isGlobal());
        return m;
    }

    /** Déconnexion : supprime le cookie de session chiffré. */
    @POST @Path("/logout")
    public jakarta.ws.rs.core.Response logout() {
        audit.record(me.username(), "LOGOUT", null, "SUCCESS", me.tenantScope(), null, null);
        return jakarta.ws.rs.core.Response.noContent()
                .header("Set-Cookie", "quarkus-credential=; Path=/; Max-Age=0; HttpOnly; SameSite=Strict").build();
    }

    @GET @Path("/users") @Transactional @RolesAllowed(Roles.SUPER_ADMIN)
    public List<Dtos.UserOut> users() {
        CurrentUser.ScopeFilter f = me.filter(null);
        return AppUser.<AppUser>list(f.clause() + " ORDER BY username", f.params()).stream().map(Dtos.UserOut::of).toList();
    }

    @POST @Path("/users") @Transactional @RolesAllowed(Roles.SUPER_ADMIN)
    public Dtos.UserOut createUser(@Valid Dtos.UserIn in) {
        if (!Roles.ALL.contains(in.role())) throw new BadRequestException("Rôle inconnu");
        if (in.tenantId() == null && !me.isGlobal()) throw new ForbiddenException();
        if (in.tenantId() == null && !Roles.SUPER_ADMIN.equals(in.role())) throw new BadRequestException("Un utilisateur non global doit être rattaché à un tenant");
        if (in.tenantId() != null) me.checkAccess(in.tenantId(), in.siteId());
        if (Roles.SITE_ADMIN.equals(in.role()) && in.siteId() == null) throw new BadRequestException("Un administrateur de pharmacie doit être rattaché à un site");
        if (in.siteId() != null) {
            Site s = Site.findById(in.siteId());
            if (s == null || !s.tenantId.equals(in.tenantId())) throw new BadRequestException("Site incohérent avec le tenant");
        }
        if (AppUser.findByUsername(in.username()) != null) throw new ClientErrorException("Identifiant déjà utilisé", 409);
        AppUser u = new AppUser();
        u.username = in.username();
        u.passwordHash = BcryptUtil.bcryptHash(in.password());
        u.role = in.role();
        u.tenantId = in.tenantId();
        u.siteId = in.siteId();
        u.displayName = in.displayName();
        u.persist();
        audit.record(me.username(), "USER_CREATE", "user:" + u.username, "SUCCESS", u.tenantId, u.siteId, u.role);
        return Dtos.UserOut.of(u);
    }

    @GET @Path("/notification-channels") @Transactional
    @RolesAllowed({Roles.SUPER_ADMIN, Roles.SITE_ADMIN, Roles.OPERATOR})
    public List<NotificationChannel> channels() {
        CurrentUser.ScopeFilter f = me.filter("siteId");
        return NotificationChannel.list(f.clause() + " ORDER BY createdAt", f.params());
    }

    @POST @Path("/notification-channels") @Transactional @RolesAllowed({Roles.SUPER_ADMIN, Roles.SITE_ADMIN})
    public NotificationChannel createChannel(@Valid Dtos.ChannelIn in) {
        me.checkAccess(in.tenantId(), in.siteId());
        if (in.type() == NotificationChannel.Type.WEBHOOK && !in.target().startsWith("https://") && !in.target().startsWith("http://127.0.0.1")
                && !in.target().startsWith("http://localhost")) {
            throw new BadRequestException("Un webhook distant doit utiliser https://");
        }
        if (in.type() == NotificationChannel.Type.EMAIL && !in.target().matches("[^@\\s]+@[^@\\s]+\\.[^@\\s]+")) {
            throw new BadRequestException("Adresse courriel invalide");
        }
        NotificationChannel c = new NotificationChannel();
        c.tenantId = in.tenantId();
        c.siteId = in.siteId();
        c.type = in.type();
        c.target = in.target();
        if (in.minSeverity() != null) c.minSeverity = in.minSeverity();
        c.persist();
        audit.record(me.username(), "NOTIFICATION_CHANNEL_CREATE", "channel:" + c.id, "SUCCESS", c.tenantId, c.siteId, c.type + " " + c.target);
        return c;
    }

    @DELETE @Path("/notification-channels/{id}") @Transactional @RolesAllowed({Roles.SUPER_ADMIN, Roles.SITE_ADMIN})
    public void disableChannel(@PathParam("id") UUID id) {
        NotificationChannel c = NotificationChannel.findById(id);
        if (c == null) throw new NotFoundException();
        me.checkAccess(c.tenantId, c.siteId);
        c.enabled = false;
        audit.record(me.username(), "NOTIFICATION_CHANNEL_DISABLE", "channel:" + c.id, "SUCCESS", c.tenantId, c.siteId, null);
    }
}
