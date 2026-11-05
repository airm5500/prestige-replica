package recovery

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

// instance : serveur MariaDB temporaire, local et sans comptes, sur une copie des fichiers.
type instance struct {
	Port   int
	DB     *sql.DB
	ErrLog string
	cmd    *exec.Cmd
	exited chan error
}

// réglages repris de l'ancien my.ini (ceux qui conditionnent la lecture des fichiers).
var inherited = []string{
	"innodb_page_size", "innodb_log_file_size", "innodb_log_files_in_group", "innodb_data_file_path",
	"innodb_undo_tablespaces", "innodb_checksum_algorithm", "innodb_file_per_table", "innodb_default_row_format",
	"lower_case_table_names", "character_set_server", "collation_server", "innodb_compression_algorithm",
}

type startSpec struct {
	datadir, runDir, name string
	force                 int
	blackhole             bool
	ini                   map[string]string
	runAs                 string
	bufferMB              int
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func startInstance(ctx context.Context, t *Tools, s startSpec) (*instance, error) {
	port, err := freePort()
	if err != nil {
		return nil, err
	}
	errLog := filepath.Join(s.runDir, s.name+".err")
	args := []string{
		"--no-defaults",
		"--datadir=" + s.datadir,
		"--port=" + strconv.Itoa(port),
		"--bind-address=127.0.0.1",
		"--pid-file=" + filepath.Join(s.runDir, s.name+".pid"),
		"--log-error=" + errLog,
		"--tmpdir=" + s.runDir,
		"--skip-grant-tables",
		"--skip-slave-start",
		"--skip-log-bin",
		"--event-scheduler=OFF",
		"--innodb-buffer-pool-size=" + strconv.Itoa(max(s.bufferMB, 128)) + "M",
		"--max-allowed-packet=1G",
		"--innodb-fast-shutdown=0",
		"--character-set-server=utf8mb4",
		"--aria-recover-options=BACKUP,FORCE",
		"--myisam-recover-options=BACKUP,FORCE",
	}
	for _, k := range inherited {
		if v, ok := s.ini[k]; ok && v != "" {
			args = append(args, "--"+strings.ReplaceAll(k, "_", "-")+"="+v)
		}
	}
	if s.force > 0 {
		args = append(args, "--innodb-force-recovery="+strconv.Itoa(s.force))
	}
	if s.blackhole {
		if t.Plugins != "" {
			args = append(args, "--plugin-dir="+t.Plugins)
		}
		args = append(args, "--plugin-load-add=ha_blackhole", "--innodb-strict-mode=OFF", "--innodb-file-per-table=ON")
	}
	if runtime.GOOS != "windows" {
		args = append(args, "--socket="+filepath.Join(s.runDir, s.name+".sock"))
	} else {
		args = append(args, "--skip-named-pipe")
	}
	if s.runAs != "" {
		args = append(args, "--user="+s.runAs)
	}
	_ = os.Remove(errLog)
	cmd := exec.Command(t.Server, args...)
	var stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stderr, &stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	in := &instance{Port: port, ErrLog: errLog, cmd: cmd, exited: make(chan error, 1)}
	go func() { in.exited <- cmd.Wait() }()

	mc := mysql.NewConfig()
	mc.User = "root"
	mc.Net = "tcp"
	mc.Addr = net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	mc.Timeout = 3 * time.Second
	mc.Params = map[string]string{"charset": "utf8mb4"}
	in.DB, _ = sql.Open("mysql", mc.FormatDSN())
	in.DB.SetMaxOpenConns(2)
	deadline := time.Now().Add(30 * time.Minute) // l'application du journal InnoDB peut être longue
	for {
		select {
		case err := <-in.exited:
			in.DB.Close()
			in.exited <- err
			msg := strings.TrimSpace(stderr.String())
			if len(msg) > 400 {
				msg = msg[len(msg)-400:]
			}
			if msg != "" {
				return nil, fmt.Errorf("arrêt au démarrage (%v) : %s", err, msg)
			}
			return nil, fmt.Errorf("arrêt au démarrage (%v)", err)
		case <-ctx.Done():
			in.Stop()
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
		if err := in.DB.PingContext(ctx); err == nil {
			return in, nil
		}
		if time.Now().After(deadline) {
			in.Stop()
			return nil, errors.New("pas démarré en 30 minutes")
		}
	}
}

// Alive indique si le serveur tourne encore (une page corrompue peut l'arrêter net).
func (in *instance) Alive() bool {
	select {
	case err := <-in.exited:
		in.exited <- err
		return false
	default:
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return in.DB.PingContext(ctx) == nil
}

// Stop arrête proprement l'instance, ou la tue au bout de 2 minutes.
func (in *instance) Stop() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	_, _ = in.DB.ExecContext(ctx, "SHUTDOWN")
	in.DB.Close()
	select {
	case err := <-in.exited:
		in.exited <- err
	case <-ctx.Done():
		_ = in.cmd.Process.Kill()
	}
}

// dumpArgs : connexion et options communes de mariadb-dump.
func (in *instance) dumpArgs(extra ...string) []string {
	a := []string{"--no-defaults", "--protocol=TCP", "-h", "127.0.0.1", "-P", strconv.Itoa(in.Port), "-u", "root",
		"--skip-lock-tables", "--quick", "--hex-blob", "--default-character-set=utf8mb4", "--max-allowed-packet=1G",
		"--skip-comments", "--skip-dump-date"}
	return append(a, extra...)
}

func qi(s string) string { return "`" + strings.ReplaceAll(s, "`", "``") + "`" }
