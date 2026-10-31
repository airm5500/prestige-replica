package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"database/sql"

	"github.com/airm5500/prestige-replica-manager/agent/internal/client"
	"github.com/airm5500/prestige-replica-manager/agent/internal/config"
	"github.com/airm5500/prestige-replica-manager/agent/internal/ops"
	"github.com/airm5500/prestige-replica-manager/agent/internal/seal"
)

// jobWorker exécute au plus une opération à la fois, en arrière-plan, pendant
// que la collecte continue.
type jobWorker struct {
	client *client.Client
	runner *ops.Runner
	log    *slog.Logger
	mu     sync.Mutex
	busy   bool
	wg     sync.WaitGroup
}

type remoteReporter struct {
	ctx    context.Context
	client *client.Client
	id     string
	log    *slog.Logger
}

func (r remoteReporter) send(level, msg string) {
	r.log.Info("opération", "id", r.id, "niveau", level, "message", msg)
	ctx, cancel := context.WithTimeout(r.ctx, 10*time.Second)
	defer cancel()
	if err := r.client.JobEvent(ctx, r.id, level, msg); err != nil {
		r.log.Warn("publication d'étape impossible", "erreur", err.Error())
	}
}
func (r remoteReporter) Step(m string) { r.send("INFO", m) }
func (r remoteReporter) Warn(m string) { r.send("WARN", m) }

func (w *jobWorker) poll(ctx context.Context) {
	w.mu.Lock()
	if w.busy {
		w.mu.Unlock()
		return
	}
	w.mu.Unlock()
	pctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	jobs, err := w.client.PollJobs(pctx)
	cancel()
	if err != nil || len(jobs) == 0 {
		return
	}
	j := jobs[0]
	sctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	err = w.client.StartJob(sctx, j.ID)
	cancel()
	if err != nil {
		w.log.Warn("opération non réclamée", "id", j.ID, "erreur", err.Error())
		return
	}
	w.mu.Lock()
	w.busy = true
	w.mu.Unlock()
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		defer func() { w.mu.Lock(); w.busy = false; w.mu.Unlock() }()
		rep := remoteReporter{ctx: context.WithoutCancel(ctx), client: w.client, id: j.ID, log: w.log}
		rep.Step("début de l'opération " + j.Type)
		// Battement de cœur : la console sait que l'opération progresse.
		hbDone := make(chan struct{})
		go func() {
			start := time.Now()
			t := time.NewTicker(10 * time.Minute)
			defer t.Stop()
			for {
				select {
				case <-hbDone:
					return
				case <-t.C:
					rep.Step(fmt.Sprintf("toujours en cours (%d min)", int(time.Since(start).Minutes())))
				}
			}
		}()
		result, err := w.runner.Execute(ctx, ops.Job{ID: j.ID, Type: j.Type, Params: j.Params}, rep)
		close(hbDone)
		status, msg := "SUCCEEDED", ""
		switch {
		case errors.Is(err, ops.ErrIntervention):
			status, msg = "INTERVENTION_REQUIRED", err.Error()
		case err != nil:
			status, msg = "FAILED", err.Error()
		}
		// Le résultat final est renvoyé jusqu'à acceptation : il ne doit pas se perdre.
		for attempt := 0; attempt < 30; attempt++ {
			fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
			ferr := w.client.FinishJob(fctx, j.ID, status, msg, result)
			cancel()
			if ferr == nil {
				return
			}
			w.log.Warn("envoi du résultat impossible, nouvel essai", "id", j.ID, "erreur", ferr.Error())
			time.Sleep(time.Duration(min(60, 2*(attempt+1))) * time.Second)
		}
	}()
}

func (w *jobWorker) wait() { w.wg.Wait() }

// ---- CLI de secours -------------------------------------------------------

type consoleReporter struct{}

func (consoleReporter) Step(m string) { fmt.Println("•", m) }
func (consoleReporter) Warn(m string) { fmt.Println("⚠", m) }

func keygen(out string) error {
	if out == "" {
		return errors.New("-out est obligatoire")
	}
	k, err := seal.NewKey()
	if err != nil {
		return err
	}
	f, err := os.OpenFile(out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("création de %s (fichier existant ?) : %w", out, err)
	}
	defer f.Close()
	if _, err := f.WriteString(hex.EncodeToString(k[:]) + "\n"); err != nil {
		return err
	}
	fmt.Printf("Clé %s écrite dans %s.\nConservez-en une copie hors du serveur (coffre, clé USB au coffre-fort) : sans elle, les sauvegardes sont irrécupérables.\n", k.ID(), out)
	return nil
}

func localOperation(cmd string, cfg *config.Config, db *sql.DB, role, rel string) int {
	r := &ops.Runner{Cfg: cfg, Monitor: db, Version: Version}
	ctx, cancel := signalContext()
	defer cancel()
	var j ops.Job
	switch cmd {
	case "preflight":
		j = ops.Job{Type: ops.TypePreflight, Params: mustJSON(map[string]any{"role": role})}
	case "backup":
		id := time.Now().UTC().Format("20060102-150405")
		j = ops.Job{Type: ops.TypeBackupFull, Params: mustJSON(map[string]any{"backupId": "local-" + id, "relativeDir": "local/" + id})}
	case "restore-test":
		j = ops.Job{Type: ops.TypeRestoreTest, Params: mustJSON(map[string]any{"relativeDir": rel})}
	}
	res, err := r.Execute(ctx, j, consoleReporter{})
	if res != nil {
		b, _ := json.MarshalIndent(res, "", "  ")
		fmt.Println(string(b))
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "ÉCHEC :", err)
		return 1
	}
	return 0
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
