//go:build !windows

package main

import (
	"context"
	"fmt"
	"os"
)

func defaultConfigPath() string { return "/etc/prestige-replica-manager/agent.json" }

// runService exécute l'agent au premier plan ; systemd gère le cycle de vie.
func runService(fn func(ctx context.Context) error) {
	ctx, cancel := signalContext()
	defer cancel()
	if err := fn(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
