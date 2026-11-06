package com.prestige.replica.service;

import java.nio.charset.StandardCharsets;
import java.security.MessageDigest;
import java.security.NoSuchAlgorithmException;
import java.security.SecureRandom;
import java.util.Base64;
import java.util.HexFormat;

/** Génération et empreinte des jetons d'agent. Seule l'empreinte est stockée. */
public final class Tokens {
    private static final SecureRandom RNG = new SecureRandom();

    private Tokens() {}

    public static String newAgentToken() {
        byte[] b = new byte[32];
        RNG.nextBytes(b);
        return "prm_" + Base64.getUrlEncoder().withoutPadding().encodeToString(b);
    }

    public static String sha256(String s) {
        try {
            return HexFormat.of().formatHex(MessageDigest.getInstance("SHA-256").digest(s.getBytes(StandardCharsets.UTF_8)));
        } catch (NoSuchAlgorithmException e) {
            throw new IllegalStateException(e);
        }
    }

    public static String prefix(String token) {
        return token.substring(0, Math.min(10, token.length()));
    }
}
