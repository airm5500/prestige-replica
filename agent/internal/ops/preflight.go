package ops

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/airm5500/prestige-replica-manager/agent/internal/collector"
	"github.com/airm5500/prestige-replica-manager/agent/internal/seal"
)

// PreflightParams : rôle visé et contexte fourni par la console.
type PreflightParams struct {
	Role              string `json:"role"` // PRIMARY, REPLICA, DELAYED_REPLICA, STANDALONE
	SuggestedServerID int64  `json:"suggestedServerId"`
	SourceVersion     string `json:"sourceVersion"`
}

// Check est un contrôle de l'assistant de prérequis.
type Check struct {
	ID      string `json:"id"`
	Status  string `json:"status"` // OK, WARN, FAIL
	Message string `json:"message"`
}

// Preflight vérifie les prérequis de réplication et de sauvegarde, sans rien modifier.
func (r *Runner) Preflight(ctx context.Context, p PreflightParams, rep Reporter) (map[string]any, error) {
	var checks []Check
	add := func(id, status, format string, a ...any) {
		checks = append(checks, Check{ID: id, Status: status, Message: fmt.Sprintf(format, a...)})
	}
	rep.Step("lecture de la configuration MariaDB")
	col := &collector.Collector{DB: r.Monitor, AgentVersion: r.Version, Timeout: 10 * time.Second}
	rp := col.Collect(ctx)
	if !rp.Reachable || rp.Server == nil {
		add("mariadb", "FAIL", "MariaDB injoignable : %s", strings.Join(rp.Errors, " ; "))
		return map[string]any{"checks": checks, "ready": false}, nil
	}
	s := rp.Server
	add("version", "OK", "MariaDB %s", s.Version)
	replica := p.Role == "REPLICA" || p.Role == "DELAYED_REPLICA"

	if p.SourceVersion != "" && majorMinor(p.SourceVersion) != majorMinor(s.Version) {
		st := "WARN"
		if versionLess(s.Version, p.SourceVersion) {
			st = "FAIL"
		}
		add("version-compat", st, "Source en %s, ce serveur en %s : utilisez la même version majeure (une réplique ne doit jamais être plus ancienne que sa source)", p.SourceVersion, s.Version)
	}

	needsBinlog := p.Role == "PRIMARY" || replica
	if !s.LogBin {
		st := "WARN"
		if p.Role == "PRIMARY" {
			st = "FAIL"
		}
		add("log_bin", st, "Journal binaire désactivé : indispensable sur le primaire pour la réplication et la restauration à un instant précis")
	} else {
		add("log_bin", "OK", "Journal binaire activé")
	}
	if s.ServerID == 0 || (s.ServerID == 1 && replica) {
		add("server_id", "FAIL", "server_id=%d : chaque serveur de la topologie doit avoir un identifiant unique", s.ServerID)
	} else {
		add("server_id", "OK", "server_id=%d", s.ServerID)
	}
	if s.LogBin && !strings.EqualFold(s.BinlogFormat, "ROW") {
		add("binlog_format", "WARN", "binlog_format=%s : ROW recommandé", s.BinlogFormat)
	}
	if s.LogBin && s.BinlogExpireSeconds > 0 && s.BinlogExpireSeconds < 86400 {
		add("binlog_retention", "WARN", "Rétention des binlogs inférieure à 24 h : une coupure plus longue imposera une reconstruction")
	}
	if replica && !s.ReadOnly {
		add("read_only", "WARN", "read_only=OFF : la réplique sera passée en lecture seule à la fin de la construction")
	}
	if replica && !s.LogSlaveUpdates {
		add("log_slave_updates", "WARN", "log_slave_updates=OFF : la réplique ne pourra pas servir de source après une bascule sans reconstruction")
	}
	if s.InnodbFlushAtCommit != 1 || (s.LogBin && s.SyncBinlog != 1) {
		add("durability", "WARN", "innodb_flush_log_at_trx_commit=%d, sync_binlog=%d : 1 et 1 recommandés pour ne perdre aucune transaction confirmée en cas de coupure électrique", s.InnodbFlushAtCommit, s.SyncBinlog)
	}

	rep.Step("contrôle des outils MariaDB")
	for _, tool := range []string{"mariadb-backup", "mbstream", "mariadbd"} {
		path, err := r.FindTool(tool)
		if err != nil {
			st := "WARN"
			if tool != "mariadbd" && r.Cfg.Backup.Enabled {
				st = "FAIL"
			}
			add("tool-"+tool, st, "%v", err)
			continue
		}
		if tool == "mariadb-backup" {
			v, err := ToolVersion(ctx, path)
			switch {
			case err != nil:
				add("tool-"+tool, "WARN", "%v", err)
			case majorMinor(v) != majorMinor(s.Version):
				add("tool-"+tool, "FAIL", "%s est en version %s alors que le serveur est en %s : utilisez l'outil livré avec le serveur", path, v, s.Version)
			default:
				add("tool-"+tool, "OK", "%s (%s)", path, v)
			}
			continue
		}
		add("tool-"+tool, "OK", "%s", path)
	}

	rep.Step("contrôle du stockage des sauvegardes")
	if !r.Cfg.Backup.Enabled && !r.Cfg.RestoreTest.Enabled && !r.Cfg.Provisioning.Enabled {
		add("backup", "WARN", "Aucune sauvegarde configurée sur cet agent (section backup)")
	}
	if r.Cfg.Backup.Root != "" {
		if err := checkWritable(r.Cfg.Backup.Root); err != nil {
			add("backup-root", "FAIL", "Stockage %s inaccessible en écriture : %v", r.Cfg.Backup.Root, err)
		} else {
			add("backup-root", "OK", "Stockage %s accessible", r.Cfg.Backup.Root)
		}
		if r.Cfg.Backup.EncryptionKeyFile != "" {
			if k, err := seal.LoadKey(r.Cfg.Backup.EncryptionKeyFile); err != nil {
				add("backup-key", "FAIL", "%v", err)
			} else {
				add("backup-key", "OK", "Clé de chiffrement %s (conservez-en une copie hors du serveur : sans elle, aucune restauration)", k.ID())
			}
		}
		if s.DataDir != "" {
			if size, err := DirSize(s.DataDir); err == nil {
				if _, free, err := collector.DiskUsage(r.Cfg.Backup.Root); err == nil {
					if float64(free) < 1.2*float64(size) {
						add("backup-space", "FAIL", "Espace libre %d Go sur le stockage pour un datadir de %d Go (1,2 fois requis par sauvegarde)", free>>30, size>>30)
					} else {
						add("backup-space", "OK", "Datadir %d Go, espace libre %d Go", size>>30, free>>30)
					}
				}
			} else {
				add("backup-space", "WARN", "Taille du datadir illisible par l'agent : %v", err)
			}
		}
	}
	if r.Cfg.RestoreTest.Enabled {
		if err := checkWritable(r.Cfg.RestoreTest.ScratchDir); err != nil {
			add("restore-scratch", "FAIL", "Dossier de restauration %s inaccessible : %v", r.Cfg.RestoreTest.ScratchDir, err)
		} else {
			add("restore-scratch", "OK", "Restaurations de contrôle dans %s", r.Cfg.RestoreTest.ScratchDir)
		}
	}
	if replica {
		if r.Cfg.Provisioning.Enabled {
			add("provisioning", "OK", "Reconstruction autorisée localement sur ce serveur (datadir %s)", r.Cfg.Provisioning.DataDir)
		} else {
			add("provisioning", "WARN", "Reconstruction non autorisée localement : activer provisioning.enabled sur cet agent pour construire la réplique")
		}
	}

	ready := true
	for _, c := range checks {
		if c.Status == "FAIL" {
			ready = false
		}
	}
	return map[string]any{
		"checks":            checks,
		"ready":             ready,
		"recommendedConfig": RecommendedConfig(p.Role, s.ServerID, p.SuggestedServerID, needsBinlog),
		"serverVersion":     s.Version,
	}, nil
}

// RecommendedConfig produit la section [mysqld] à ajouter au my.ini / my.cnf.
// Elle n'est jamais appliquée automatiquement (§16.2).
func RecommendedConfig(role string, currentID, suggestedID int64, binlog bool) string {
	id := currentID
	if id <= 1 && suggestedID > 0 {
		id = suggestedID
	}
	var b strings.Builder
	b.WriteString("# Prestige Replica Manager — configuration recommandée (rôle " + role + ")\n")
	b.WriteString("# À ajouter dans la section [mysqld] du my.ini, puis redémarrer le service MariaDB\n")
	b.WriteString("# pendant une fenêtre de maintenance. Conserver une copie du fichier d'origine.\n[mysqld]\n")
	fmt.Fprintf(&b, "server_id=%d\n", id)
	if binlog {
		b.WriteString("log_bin=mariadb-bin\nbinlog_format=ROW\nbinlog_row_image=FULL\n")
		b.WriteString("binlog_expire_logs_seconds=604800\nlog_slave_updates=ON\ngtid_strict_mode=ON\nsync_binlog=1\n")
	}
	b.WriteString("innodb_flush_log_at_trx_commit=1\n")
	if role == "REPLICA" || role == "DELAYED_REPLICA" {
		b.WriteString("read_only=ON\nrelay_log=mariadb-relay\nskip_slave_start=OFF\n")
	}
	return b.String()
}

func checkWritable(dir string) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".prm-write-test-*")
	if err != nil {
		return err
	}
	name := f.Name()
	f.Close()
	return os.Remove(filepath.Clean(name))
}
