// Package collector lit l'état de MariaDB en lecture seule.
//
// L'agent du Lot 1 n'exécute QUE les instructions de la liste fermée
// AllowedStatements. Aucune instruction n'est construite à partir de données
// externes : l'interface ne peut pas faire exécuter de SQL arbitraire.
package collector

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/airm5500/prestige-replica-manager/agent/internal/report"
)

const (
	stmtReadOnly  = "SET SESSION TRANSACTION READ ONLY"
	stmtVariables = "SHOW GLOBAL VARIABLES WHERE Variable_name IN (" +
		"'version','version_comment','server_id','read_only','log_bin','log_slave_updates'," +
		"'binlog_format','binlog_row_image','gtid_domain_id','gtid_strict_mode','gtid_current_pos'," +
		"'gtid_binlog_pos','gtid_slave_pos','expire_logs_days','binlog_expire_logs_seconds','sync_binlog'," +
		"'innodb_flush_log_at_trx_commit','rpl_semi_sync_master_enabled','rpl_semi_sync_slave_enabled'," +
		"'time_zone','system_time_zone','datadir','have_ssl','max_connections','log_bin_basename')"
	stmtStatus = "SHOW GLOBAL STATUS WHERE Variable_name IN (" +
		"'Uptime','Threads_connected','Questions','Binlog_bytes_written')"
	stmtBinaryLogs = "SHOW BINARY LOGS"
	stmtAllSlaves  = "SHOW ALL SLAVES STATUS"
	stmtDatabases  = "SHOW DATABASES"
)

// AllowedStatements est la liste blanche exhaustive des instructions de l'agent.
var AllowedStatements = []string{stmtReadOnly, stmtVariables, stmtStatus, stmtBinaryLogs, stmtAllSlaves, stmtDatabases}

// Collector produit des rapports à partir d'une connexion SQL locale.
type Collector struct {
	DB             *sql.DB
	AgentVersion   string
	ExtraDiskPaths []string
	Timeout        time.Duration
}

// Collect construit un rapport. Il ne renvoie jamais d'erreur : les problèmes
// sont consignés dans Report.Errors pour être visibles dans la console.
func (c *Collector) Collect(ctx context.Context) report.Report {
	host, _ := os.Hostname()
	r := report.Report{
		SchemaVersion: report.SchemaVersion,
		AgentVersion:  c.AgentVersion,
		CollectedAt:   time.Now().UTC(),
		Hostname:      host,
		OS:            runtime.GOOS,
		Arch:          runtime.GOARCH,
		Channels:      []report.Channel{},
		Disks:         []report.Disk{},
		Errors:        []string{},
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	conn, err := c.DB.Conn(ctx)
	if err != nil {
		r.Errors = append(r.Errors, "connexion MariaDB: "+Redact(err.Error()))
		c.addDisks(&r, "")
		return r
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, stmtReadOnly); err != nil {
		r.Errors = append(r.Errors, "connexion MariaDB: "+Redact(err.Error()))
		c.addDisks(&r, "")
		return r
	}
	r.Reachable = true

	vars, err := keyValues(ctx, conn, stmtVariables)
	if err != nil {
		r.Errors = append(r.Errors, "variables: "+Redact(err.Error()))
	}
	status, err := keyValues(ctx, conn, stmtStatus)
	if err != nil {
		r.Errors = append(r.Errors, "statut: "+Redact(err.Error()))
	}
	r.Server = buildServer(vars, status)

	if r.Server.LogBin {
		if b, err := binaryLogs(ctx, conn); err != nil {
			r.Errors = append(r.Errors, "journaux binaires: "+Redact(err.Error()))
		} else {
			r.Binlogs = b
		}
	}

	rows, err := rowsAsMaps(ctx, conn, stmtAllSlaves)
	if err != nil {
		r.Errors = append(r.Errors, "état de réplication: "+Redact(err.Error()))
	}
	for _, m := range rows {
		r.Channels = append(r.Channels, buildChannel(m))
	}

	if dbs, err := databases(ctx, conn); err != nil {
		r.Errors = append(r.Errors, "bases: "+Redact(err.Error()))
	} else {
		r.Server.Databases = dbs
	}

	dirs := []string{r.Server.DataDir}
	if base := vars["log_bin_basename"]; base != "" {
		if i := strings.LastIndexAny(base, `/\`); i > 0 {
			dirs = append(dirs, base[:i])
		}
	}
	c.addDisks(&r, dirs...)
	return r
}

func (c *Collector) addDisks(r *report.Report, dirs ...string) {
	seen := map[string]bool{}
	for _, p := range append(dirs, c.ExtraDiskPaths...) {
		if p != "" {
			p = filepath.Clean(p)
		}
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		total, free, err := DiskUsage(p)
		if err != nil {
			r.Errors = append(r.Errors, fmt.Sprintf("disque %s: %v", p, err))
			continue
		}
		r.Disks = append(r.Disks, report.Disk{Path: p, TotalBytes: total, FreeBytes: free})
	}
}

func keyValues(ctx context.Context, conn *sql.Conn, q string) (map[string]string, error) {
	rows, err := conn.QueryContext(ctx, q)
	if err != nil {
		return map[string]string{}, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k string
		var v sql.NullString
		if err := rows.Scan(&k, &v); err != nil {
			return out, err
		}
		out[strings.ToLower(k)] = v.String
	}
	return out, rows.Err()
}

func rowsAsMaps(ctx context.Context, conn *sql.Conn, q string) ([]map[string]sql.NullString, error) {
	rows, err := conn.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var out []map[string]sql.NullString
	for rows.Next() {
		vals := make([]sql.NullString, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return out, err
		}
		m := make(map[string]sql.NullString, len(cols))
		for i, c := range cols {
			m[c] = vals[i]
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func binaryLogs(ctx context.Context, conn *sql.Conn) (*report.Binlogs, error) {
	rows, err := rowsAsMaps(ctx, conn, stmtBinaryLogs)
	if err != nil {
		return nil, err
	}
	b := &report.Binlogs{Count: len(rows)}
	for i, m := range rows {
		b.TotalBytes += atoi(m["File_size"].String)
		if i == 0 {
			b.First = m["Log_name"].String
		}
		b.Last = m["Log_name"].String
	}
	return b, nil
}

func databases(ctx context.Context, conn *sql.Conn) ([]string, error) {
	rows, err := conn.QueryContext(ctx, stmtDatabases)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return out, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func buildServer(v, s map[string]string) *report.Server {
	expire := atoi(v["binlog_expire_logs_seconds"])
	if expire == 0 {
		if d, err := strconv.ParseFloat(v["expire_logs_days"], 64); err == nil {
			expire = int64(d * 86400)
		}
	}
	return &report.Server{
		Version:             v["version"],
		VersionComment:      v["version_comment"],
		ServerID:            atoi(v["server_id"]),
		ReadOnly:            on(v["read_only"]),
		LogBin:              on(v["log_bin"]),
		LogSlaveUpdates:     on(v["log_slave_updates"]),
		BinlogFormat:        v["binlog_format"],
		BinlogRowImage:      v["binlog_row_image"],
		GtidDomainID:        atoi(v["gtid_domain_id"]),
		GtidStrictMode:      on(v["gtid_strict_mode"]),
		GtidCurrentPos:      v["gtid_current_pos"],
		GtidBinlogPos:       v["gtid_binlog_pos"],
		GtidSlavePos:        v["gtid_slave_pos"],
		BinlogExpireSeconds: expire,
		SyncBinlog:          atoi(v["sync_binlog"]),
		InnodbFlushAtCommit: atoi(v["innodb_flush_log_at_trx_commit"]),
		SemiSyncMasterOn:    on(v["rpl_semi_sync_master_enabled"]),
		SemiSyncSlaveOn:     on(v["rpl_semi_sync_slave_enabled"]),
		TimeZone:            v["time_zone"],
		SystemTimeZone:      v["system_time_zone"],
		DataDir:             v["datadir"],
		HaveSSL:             v["have_ssl"],
		MaxConnections:      atoi(v["max_connections"]),
		UptimeSeconds:       atoi(s["uptime"]),
		ThreadsConnected:    atoi(s["threads_connected"]),
		Questions:           atoi(s["questions"]),
		BinlogBytesWritten:  atoi(s["binlog_bytes_written"]),
		Databases:           []string{},
	}
}

func buildChannel(m map[string]sql.NullString) report.Channel {
	return report.Channel{
		ConnectionName:           m["Connection_name"].String,
		MasterHost:               m["Master_Host"].String,
		MasterPort:               atoi(m["Master_Port"].String),
		MasterServerID:           atoi(m["Master_Server_Id"].String),
		IORunning:                m["Slave_IO_Running"].String,
		SQLRunning:               m["Slave_SQL_Running"].String,
		SecondsBehindMaster:      optInt(m["Seconds_Behind_Master"]),
		SQLDelay:                 atoi(m["SQL_Delay"].String),
		SQLRemainingDelay:        optInt(m["SQL_Remaining_Delay"]),
		LastIOErrno:              atoi(m["Last_IO_Errno"].String),
		LastIOError:              Redact(m["Last_IO_Error"].String),
		LastSQLErrno:             atoi(m["Last_SQL_Errno"].String),
		LastSQLError:             Redact(m["Last_SQL_Error"].String),
		UsingGtid:                m["Using_Gtid"].String,
		GtidIOPos:                m["Gtid_IO_Pos"].String,
		MasterLogFile:            m["Master_Log_File"].String,
		ReadMasterLogPos:         atoi(m["Read_Master_Log_Pos"].String),
		RelayMasterLogFile:       m["Relay_Master_Log_File"].String,
		ExecMasterLogPos:         atoi(m["Exec_Master_Log_Pos"].String),
		RelayLogSpace:            atoi(m["Relay_Log_Space"].String),
		SlaveSQLState:            m["Slave_SQL_Running_State"].String,
		MasterSSLAllowed:         m["Master_SSL_Allowed"].String,
		ReplicateDoDB:            m["Replicate_Do_DB"].String,
		ReplicateIgnoreDB:        m["Replicate_Ignore_DB"].String,
		ReplicateDoTable:         m["Replicate_Do_Table"].String,
		ReplicateIgnoreTable:     m["Replicate_Ignore_Table"].String,
		ReplicateWildDoTable:     m["Replicate_Wild_Do_Table"].String,
		ReplicateWildIgnoreTable: m["Replicate_Wild_Ignore_Table"].String,
		RetriedTransactions:      atoi(m["Retried_transactions"].String),
	}
}

func atoi(s string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return n
}

func optInt(v sql.NullString) *int64 {
	if !v.Valid || v.String == "" {
		return nil
	}
	n := atoi(v.String)
	return &n
}

func on(s string) bool {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "ON", "1", "YES", "TRUE":
		return true
	}
	return false
}

var secretPattern = regexp.MustCompile(`(?i)(password|passwd|pwd|secret|token)(\s*[=:]\s*|\s+)('[^']*'|"[^"]*"|\S+)`)

// Redact masque toute valeur ressemblant à un secret dans un message.
// Les mentions « using password: YES/NO » de MariaDB sont conservées car elles
// ne révèlent aucun secret et aident au diagnostic.
func Redact(s string) string {
	return secretPattern.ReplaceAllStringFunc(s, func(m string) string {
		sub := secretPattern.FindStringSubmatch(m)
		v := strings.Trim(sub[3], "'\")")
		if strings.EqualFold(v, "YES") || strings.EqualFold(v, "NO") {
			return m
		}
		return sub[1] + sub[2] + "***"
	})
}
