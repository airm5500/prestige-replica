package com.prestige.replica.alert;

import com.prestige.replica.domain.Severity;

/**
 * Constat produit par l'évaluateur. {@code key} distingue plusieurs constats
 * du même type sur un nœud (par exemple un canal ou un disque).
 */
public record Finding(String type, String key, Severity severity, String message, String details) {
    public static Finding of(String type, Severity s, String message) { return new Finding(type, "", s, message, null); }
}
