package com.prestige.replica.domain;

/** Niveau d'un incident, du moins au plus grave. */
public enum Severity {
    INFO, WARNING, CRITICAL;

    public boolean atLeast(Severity other) { return ordinal() >= other.ordinal(); }
}
