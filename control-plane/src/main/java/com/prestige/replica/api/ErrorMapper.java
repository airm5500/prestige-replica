package com.prestige.replica.api;

import jakarta.ws.rs.WebApplicationException;
import jakarta.ws.rs.core.MediaType;
import jakarta.ws.rs.core.Response;
import java.util.Map;
import org.jboss.resteasy.reactive.server.ServerExceptionMapper;

/** Renvoie le message des erreurs métier (4xx) à la console, pour qu'elle l'affiche tel quel. */
public class ErrorMapper {
    @ServerExceptionMapper
    public Response map(WebApplicationException e) {
        Response r = e.getResponse();
        int status = r.getStatus();
        String msg = e.getMessage();
        if (status >= 500 || msg == null || msg.startsWith("HTTP ")) return r;
        return Response.status(status).type(MediaType.APPLICATION_JSON).entity(Map.of("message", msg)).build();
    }
}
