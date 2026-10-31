//go:build windows

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows/svc"
)

func defaultConfigPath() string {
	return filepath.Join(os.Getenv("ProgramData"), "PrestigeReplicaManager", "agent.json")
}

type winService struct {
	fn func(ctx context.Context) error
}

func (s *winService) Execute(_ []string, req <-chan svc.ChangeRequest, st chan<- svc.Status) (bool, uint32) {
	st <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.fn(ctx) }()
	st <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for {
		select {
		case err := <-done:
			cancel()
			if err != nil {
				return true, 1
			}
			return false, 0
		case c := <-req:
			switch c.Cmd {
			case svc.Interrogate:
				st <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				st <- svc.Status{State: svc.StopPending}
				cancel()
				<-done
				return false, 0
			}
		}
	}
}

// runService s'intègre au gestionnaire de services Windows, ou s'exécute au
// premier plan lorsqu'il est lancé depuis une console.
func runService(fn func(ctx context.Context) error) {
	isSvc, err := svc.IsWindowsService()
	if err == nil && isSvc {
		if err := svc.Run("PrestigeReplicaAgent", &winService{fn: fn}); err != nil {
			os.Exit(1)
		}
		return
	}
	ctx, cancel := signalContext()
	defer cancel()
	if err := fn(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
