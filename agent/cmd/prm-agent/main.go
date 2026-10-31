// prm-agent est l'agent local de Prestige Replica Manager.
//
// Lot 1 : observation strictement en lecture seule. L'agent collecte l'état
// MariaDB (réplication, GTID, binlogs, disque) et l'envoie au Control Plane.
// Il n'expose aucun port et n'accepte aucune commande.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"math"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/airm5500/prestige-replica-manager/agent/internal/buffer"
	"github.com/airm5500/prestige-replica-manager/agent/internal/client"
	"github.com/airm5500/prestige-replica-manager/agent/internal/collector"
	"github.com/airm5500/prestige-replica-manager/agent/internal/config"
	"github.com/airm5500/prestige-replica-manager/agent/internal/report"
)

// Version est injectée à la construction (-ldflags "-X main.Version=...").
var Version = "0.1.0-dev"

func usage() {
	fmt.Fprintf(os.Stderr, `prm-agent %s — agent Prestige Replica Manager (lecture seule)

Utilisation :
  prm-agent run    -config agent.json   collecte périodique et envoi au Control Plane
  prm-agent status -config agent.json   affiche l'état local (CLI de secours, sans Control Plane)
  prm-agent status -config agent.json -json
  prm-agent check  -config agent.json   valide la configuration et la connexion MariaDB
  prm-agent version
`, Version)
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd := os.Args[1]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	cfgPath := fs.String("config", defaultConfigPath(), "fichier de configuration JSON")
	asJSON := fs.Bool("json", false, "sortie JSON (status)")
	_ = fs.Parse(os.Args[2:])

	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	switch cmd {
	case "version":
		fmt.Println(Version)
		return
	case "run", "status", "check":
	default:
		usage()
		os.Exit(2)
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	db, err := openDB(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer db.Close()
	col := &collector.Collector{DB: db, AgentVersion: Version, ExtraDiskPaths: cfg.ExtraDiskPaths,
		Timeout: time.Duration(cfg.MariaDB.TimeoutSeconds) * time.Second}

	switch cmd {
	case "status":
		r := col.Collect(context.Background())
		if *asJSON {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			_ = enc.Encode(r)
		} else {
			printStatus(r)
		}
		if !r.Reachable {
			os.Exit(1)
		}
	case "check":
		r := col.Collect(context.Background())
		ok := r.Reachable
		fmt.Printf("MariaDB joignable : %v\n", r.Reachable)
		for _, e := range r.Errors {
			fmt.Printf("  avertissement : %s\n", e)
		}
		if cfg.ControlPlaneURL != "" {
			tok, err := cfg.AgentToken()
			if err != nil {
				fmt.Println("jeton d'agent :", err)
				ok = false
			} else if _, err := client.New(cfg, tok); err != nil {
				fmt.Println("TLS :", err)
				ok = false
			} else {
				fmt.Println("Configuration Control Plane : valide")
			}
		}
		if !ok {
			os.Exit(1)
		}
	case "run":
		runService(func(ctx context.Context) error { return run(ctx, cfg, col, logger) })
	}
}

func openDB(cfg *config.Config) (*sql.DB, error) {
	pw, err := cfg.MariaDBPassword()
	if err != nil {
		return nil, err
	}
	mc := mysql.NewConfig()
	mc.User = cfg.MariaDB.User
	mc.Passwd = pw
	if strings.HasPrefix(cfg.MariaDB.Address, "unix:") {
		mc.Net = "unix"
		mc.Addr = strings.TrimPrefix(cfg.MariaDB.Address, "unix:")
	} else {
		mc.Net = "tcp"
		mc.Addr = cfg.MariaDB.Address
	}
	mc.Timeout = time.Duration(cfg.MariaDB.TimeoutSeconds) * time.Second
	mc.ReadTimeout = mc.Timeout
	mc.WriteTimeout = mc.Timeout
	db, err := sql.Open("mysql", mc.FormatDSN())
	if err != nil {
		return nil, errors.New("configuration MariaDB invalide")
	}
	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(10 * time.Minute)
	return db, nil
}

// run collecte périodiquement et vide la file vers le Control Plane.
func run(ctx context.Context, cfg *config.Config, col *collector.Collector, log *slog.Logger) error {
	log.Info("démarrage de l'agent", "version", Version, "intervalle", cfg.Interval().String(), "controlPlane", cfg.ControlPlaneURL)
	var cl *client.Client
	if cfg.ControlPlaneURL != "" {
		tok, err := cfg.AgentToken()
		if err != nil {
			return err
		}
		if cl, err = client.New(cfg, tok); err != nil {
			return err
		}
	} else {
		log.Warn("aucun controlPlaneUrl : mode autonome, les rapports ne sont pas envoyés")
	}
	queue := buffer.New[report.Report](cfg.BufferSize)
	ticker := time.NewTicker(cfg.Interval())
	defer ticker.Stop()
	failures := 0
	var nextAttempt time.Time

	tick := func() {
		r := col.Collect(ctx)
		if !r.Reachable {
			log.Warn("MariaDB injoignable", "erreurs", r.Errors)
		}
		if cl == nil {
			return
		}
		queue.Push(r)
		if time.Now().Before(nextAttempt) {
			return
		}
		for queue.Len() > 0 {
			batch := queue.Peek(50)
			sctx, cancel := context.WithTimeout(ctx, 20*time.Second)
			err := cl.Send(sctx, batch)
			cancel()
			if err != nil {
				failures++
				// Délai progressif plafonné à 5 minutes.
				backoff := time.Duration(math.Min(300, math.Pow(2, float64(min(failures, 9))))) * time.Second
				nextAttempt = time.Now().Add(backoff)
				log.Error("envoi impossible, rapports conservés", "erreur", err.Error(), "enAttente", queue.Len(),
					"abandonnés", queue.Dropped(), "prochainEssai", backoff.String())
				return
			}
			if failures > 0 {
				log.Info("connexion au Control Plane rétablie", "rapportsRenvoyés", len(batch))
			}
			failures = 0
			queue.Drop(len(batch))
		}
	}

	tick()
	for {
		select {
		case <-ctx.Done():
			log.Info("arrêt de l'agent", "enAttente", queue.Len())
			return nil
		case <-ticker.C:
			tick()
		}
	}
}

func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

func printStatus(r report.Report) {
	fmt.Printf("Hôte : %s (%s/%s)  collecte : %s\n", r.Hostname, r.OS, r.Arch, r.CollectedAt.Format(time.RFC3339))
	if !r.Reachable {
		fmt.Println("MariaDB : INJOIGNABLE")
	}
	if s := r.Server; s != nil && r.Reachable {
		fmt.Printf("MariaDB %s  server_id=%d  read_only=%v  binlog=%v (%s/%s)\n", s.Version, s.ServerID, s.ReadOnly, s.LogBin, s.BinlogFormat, s.BinlogRowImage)
		fmt.Printf("GTID courant=%q binlog=%q réplique=%q  strict=%v\n", s.GtidCurrentPos, s.GtidBinlogPos, s.GtidSlavePos, s.GtidStrictMode)
	}
	if b := r.Binlogs; b != nil {
		fmt.Printf("Binlogs : %d fichiers, %d Mo (%s → %s)\n", b.Count, b.TotalBytes/1e6, b.First, b.Last)
	}
	if len(r.Channels) == 0 && r.Reachable {
		fmt.Println("Aucun canal de réplication (serveur primaire ou autonome)")
	}
	for _, c := range r.Channels {
		lag := "NULL"
		if c.SecondsBehindMaster != nil {
			lag = fmt.Sprintf("%d s", *c.SecondsBehindMaster)
		}
		name := c.ConnectionName
		if name == "" {
			name = "(défaut)"
		}
		fmt.Printf("Canal %s ← %s:%d  IO=%s SQL=%s retard=%s GTID=%s\n", name, c.MasterHost, c.MasterPort, c.IORunning, c.SQLRunning, lag, c.UsingGtid)
		if c.LastIOErrno != 0 {
			fmt.Printf("  erreur IO %d : %s\n", c.LastIOErrno, c.LastIOError)
		}
		if c.LastSQLErrno != 0 {
			fmt.Printf("  erreur SQL %d : %s\n", c.LastSQLErrno, c.LastSQLError)
		}
	}
	for _, d := range r.Disks {
		pct := 0.0
		if d.TotalBytes > 0 {
			pct = 100 * float64(d.FreeBytes) / float64(d.TotalBytes)
		}
		fmt.Printf("Disque %s : %.1f%% libre (%d Go / %d Go)\n", d.Path, pct, d.FreeBytes/1e9, d.TotalBytes/1e9)
	}
	for _, e := range r.Errors {
		fmt.Printf("Avertissement : %s\n", e)
	}
}
