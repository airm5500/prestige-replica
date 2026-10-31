package ops

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
)

// ArtifactParams désigne une sauvegarde du catalogue.
type ArtifactParams struct {
	BackupID    string `json:"backupId"`
	RelativeDir string `json:"relativeDir"`
	SHA256      string `json:"sha256"`
}

// RestoreTest restaure la sauvegarde dans une instance temporaire isolée
// (écoute sur 127.0.0.1 uniquement, port libre, sans réplication), exécute des
// contrôles puis détruit l'instance. Une sauvegarde n'est considérée valide
// qu'après ce test (§6). La durée totale mesure un RTO réaliste.
func (r *Runner) RestoreTest(ctx context.Context, p ArtifactParams, rep Reporter) (map[string]any, error) {
	t0 := time.Now()
	timings := map[string]int{}
	lap := func(name string, since time.Time) { timings[name] = int(time.Since(since).Seconds()) }

	artifact, m, key, err := r.openArtifact(p.RelativeDir, p.SHA256, rep)
	if err != nil {
		return nil, err
	}
	lap("verifySeconds", t0)
	if err := os.MkdirAll(r.Cfg.RestoreTest.ScratchDir, 0o750); err != nil {
		return nil, err
	}
	scratch, err := os.MkdirTemp(r.Cfg.RestoreTest.ScratchDir, "restore-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(scratch)
	datadir := filepath.Join(scratch, "data")

	t1 := time.Now()
	if err := r.extract(ctx, artifact, key, datadir, rep); err != nil {
		return nil, err
	}
	lap("restoreSeconds", t1)
	// Le dossier de travail est dédié : l'utilisateur MariaDB doit pouvoir le traverser.
	if err := chownTree(r.Cfg.RestoreTest.ScratchDir, r.Cfg.RestoreTest.RunAsUser); err != nil {
		return nil, fmt.Errorf("droits du dossier de restauration: %w", err)
	}

	t2 := time.Now()
	rep.Step("démarrage de l'instance isolée")
	db, stop, err := r.startIsolated(ctx, scratch, datadir)
	if err != nil {
		return nil, err
	}
	defer stop()
	lap("startSeconds", t2)

	rep.Step("contrôles de cohérence")
	t3 := time.Now()
	checks, err := inspectRestored(ctx, db)
	if err != nil {
		return nil, err
	}
	lap("checkSeconds", t3)
	restoredGTID := ""
	if b, err := os.ReadFile(filepath.Join(datadir, "mariadb_backup_binlog_info")); err == nil {
		if f := strings.Fields(string(b)); len(f) >= 3 {
			restoredGTID = f[2]
		}
	}
	timings["totalSeconds"] = int(time.Since(t0).Seconds())
	rep.Step(fmt.Sprintf("restauration vérifiée en %s", time.Since(t0).Round(time.Second)))
	return map[string]any{
		"backupId":      m.BackupID,
		"serverVersion": m.ServerVersion,
		"gtid":          firstNonEmpty(restoredGTID, m.GTID),
		"timings":       timings,
		"checks":        checks,
	}, nil
}

// startIsolated lance mariadbd sur le datadir restauré, sans accès réseau
// externe ni réplication, et renvoie une connexion d'administration.
func (r *Runner) startIsolated(ctx context.Context, scratch, datadir string) (*sql.DB, func(), error) {
	mariadbd, err := r.FindTool("mariadbd")
	if err != nil {
		return nil, nil, err
	}
	port, err := freePort()
	if err != nil {
		return nil, nil, err
	}
	args := []string{
		"--no-defaults",
		"--datadir=" + datadir,
		"--port=" + strconv.Itoa(port),
		"--bind-address=127.0.0.1",
		"--pid-file=" + filepath.Join(scratch, "mysqld.pid"),
		"--log-error=" + filepath.Join(scratch, "error.log"),
		"--skip-grant-tables",
		"--skip-slave-start",
		"--skip-log-bin",
		"--event-scheduler=OFF",
		"--innodb-buffer-pool-size=" + strconv.Itoa(r.Cfg.RestoreTest.BufferPoolMB) + "M",
		"--tmpdir=" + scratch,
	}
	if runtime.GOOS != "windows" {
		args = append(args, "--socket="+filepath.Join(scratch, "mysqld.sock"))
	} else {
		args = append(args, "--skip-named-pipe")
	}
	args = append(args, runAsArgs(r.Cfg.RestoreTest.RunAsUser)...)
	cmd := exec.Command(mariadbd, args...)
	errTail := &tail{max: 20}
	cmd.Stdout, cmd.Stderr = errTail, errTail
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	mc := mysql.NewConfig()
	mc.User = "root"
	mc.Net = "tcp"
	mc.Addr = net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	mc.Timeout = 3 * time.Second
	db, _ := sql.Open("mysql", mc.FormatDSN())
	stop := func() {
		sctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		_, _ = db.ExecContext(sctx, "SHUTDOWN")
		db.Close()
		select {
		case <-exited:
		case <-sctx.Done():
			_ = cmd.Process.Kill()
			<-exited
		}
	}
	deadline := time.Now().Add(10 * time.Minute) // la reprise InnoDB peut être longue
	for {
		select {
		case err := <-exited:
			db.Close()
			log, _ := os.ReadFile(filepath.Join(scratch, "error.log"))
			return nil, nil, fmt.Errorf("l'instance isolée s'est arrêtée (%v) : %s", err, errorLines(firstNonEmpty(string(log), errTail.String())))
		case <-ctx.Done():
			stop()
			return nil, nil, ctx.Err()
		case <-time.After(time.Second):
		}
		if err := db.PingContext(ctx); err == nil {
			return db, stop, nil
		}
		if time.Now().After(deadline) {
			stop()
			return nil, nil, errors.New("l'instance isolée n'a pas démarré en 10 minutes")
		}
	}
}

// inspectRestored compte les objets et vérifie les plus grosses tables.
func inspectRestored(ctx context.Context, db *sql.DB) (map[string]any, error) {
	out := map[string]any{}
	var version string
	if err := db.QueryRowContext(ctx, "SELECT VERSION()").Scan(&version); err != nil {
		return nil, err
	}
	out["version"] = version
	rows, err := db.QueryContext(ctx, `SELECT table_schema, COUNT(*), COALESCE(SUM(data_length+index_length),0)
		FROM information_schema.tables
		WHERE table_schema NOT IN ('mysql','information_schema','performance_schema','sys')
		GROUP BY table_schema ORDER BY table_schema`)
	if err != nil {
		return nil, err
	}
	type schema struct {
		Name   string `json:"name"`
		Tables int64  `json:"tables"`
		Bytes  int64  `json:"bytes"`
	}
	var schemas []schema
	for rows.Next() {
		var s schema
		if err := rows.Scan(&s.Name, &s.Tables, &s.Bytes); err != nil {
			rows.Close()
			return nil, err
		}
		schemas = append(schemas, s)
	}
	rows.Close()
	out["schemas"] = schemas

	// Lecture complète des 5 plus grosses tables : force la lecture des pages.
	big, err := db.QueryContext(ctx, `SELECT table_schema, table_name FROM information_schema.tables
		WHERE table_type='BASE TABLE' AND table_schema NOT IN ('mysql','information_schema','performance_schema','sys')
		ORDER BY data_length DESC LIMIT 5`)
	if err != nil {
		return nil, err
	}
	type tbl struct{ Schema, Table string }
	var tables []tbl
	for big.Next() {
		var t tbl
		if err := big.Scan(&t.Schema, &t.Table); err == nil {
			tables = append(tables, t)
		}
	}
	big.Close()
	counts := map[string]int64{}
	for _, t := range tables {
		var n int64
		q := fmt.Sprintf("SELECT COUNT(*) FROM `%s`.`%s`", strings.ReplaceAll(t.Schema, "`", "``"), strings.ReplaceAll(t.Table, "`", "``"))
		cctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		err := db.QueryRowContext(cctx, q).Scan(&n)
		cancel()
		if err != nil {
			return nil, fmt.Errorf("lecture de %s.%s impossible: %w", t.Schema, t.Table, err)
		}
		counts[t.Schema+"."+t.Table] = n
	}
	out["rowCounts"] = counts
	return out, nil
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func firstNonEmpty(a ...string) string {
	for _, s := range a {
		if s != "" {
			return s
		}
	}
	return ""
}

// errorLines extrait les lignes [ERROR] d'un journal MariaDB.
func errorLines(log string) string {
	var out []string
	for _, l := range strings.Split(log, "\n") {
		if strings.Contains(l, "[ERROR]") && !strings.HasSuffix(strings.TrimSpace(l), "Aborting") {
			out = append(out, strings.TrimSpace(l))
		}
	}
	if len(out) == 0 {
		return lastLine(log)
	}
	if len(out) > 3 {
		out = out[len(out)-3:]
	}
	return strings.Join(out, " | ")
}
