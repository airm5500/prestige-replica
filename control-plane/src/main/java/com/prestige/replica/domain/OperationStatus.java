package com.prestige.replica.domain;

public enum OperationStatus {
    AWAITING_APPROVAL, QUEUED, RUNNING, SUCCEEDED, FAILED, INTERVENTION_REQUIRED, CANCELLED;

    public boolean terminal() {
        return this == SUCCEEDED || this == FAILED || this == INTERVENTION_REQUIRED || this == CANCELLED;
    }
}
