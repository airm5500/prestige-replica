package com.prestige.replica.domain;

/** Rôles de la spécification §19.4. */
public final class Roles {
    public static final String SUPER_ADMIN = "SUPER_ADMIN";
    public static final String OPERATOR = "OPERATOR";
    public static final String APPROVER = "APPROVER";
    public static final String SITE_ADMIN = "SITE_ADMIN";
    public static final String AUDITOR = "AUDITOR";
    public static final String READ_ONLY = "READ_ONLY";

    public static final java.util.Set<String> ALL =
            java.util.Set.of(SUPER_ADMIN, OPERATOR, APPROVER, SITE_ADMIN, AUDITOR, READ_ONLY);

    private Roles() {}
}
