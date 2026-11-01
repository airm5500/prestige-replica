package ops

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/airm5500/prestige-replica-manager/agent/internal/config"
	"github.com/go-sql-driver/mysql"
)

// TypeConsistencyCheck : contrôle de cohérence d'une réplique avec son primaire (§7).
// Lecture seule des deux côtés ; aucune correction n'est jamais tentée.
const TypeConsistencyCheck = "CONSISTENCY_CHECK"

var systemDBs = map[string]bool{"mysql": true, "information_schema": true, "performance_schema": true, "sys": true}

var excludeRe = regexp.MustCompile(`^[A-Za-z0-9_$]{1,64}\.([A-Za-z0-9_$]{1,64}|\*)$`)

type ConsistencyParams struct {
	SourceHost  string   `json:"sourceHost"`
	SourcePort  int      `json:"sourcePort"`
	Mode        string   `json:"mode"` // SCHEMA ou FULL
	Databases   []string `json:"databases"`
	Exclude     []string `json:"exclude"`
	ChunkRows   int      `json:"chunkRows"`
	PauseFactor float64  `json:"pauseFactor"`
	MaxMinutes  int      `json:"maxMinutes"`
	StartAfter  string   `json:"startAfter"` // reprise : tables strictement après « base.table »
	Rechecks    int      `json:"rechecks"`
}

type SchemaDiff struct {
	Kind    string `json:"kind"`
	Object  string `json:"object"`
	Issue   string `json:"issue"` // MISSING_ON_REPLICA, EXTRA_ON_REPLICA, DIFFERENT
	Primary string `json:"primary,omitempty"`
	Replica string `json:"replica,omitempty"`
}

type KeyDiff struct {
	Key   string `json:"key"`
	Issue string `json:"issue"` // MISSING_ON_REPLICA, EXTRA_ON_REPLICA, DIFFERENT
}

type ChunkDiff struct {
	Lower       string    `json:"lower,omitempty"`
	Upper       string    `json:"upper,omitempty"`
	PrimaryRows int64     `json:"primaryRows"`
	ReplicaRows int64     `json:"replicaRows"`
	Keys        []KeyDiff `json:"keys,omitempty"`
	MoreKeys    bool      `json:"moreKeys,omitempty"`
}

type TableCheck struct {
	Table       string      `json:"table"`
	Rows        int64       `json:"rows"`
	Chunks      int         `json:"chunks"`
	Status      string      `json:"status"` // OK, DIVERGENT, INCONCLUSIVE, SKIPPED
	Reason      string      `json:"reason,omitempty"`
	Divergences []ChunkDiff `json:"divergences,omitempty"`
	Seconds     float64     `json:"seconds"`
}

func (p *ConsistencyParams) validate() error {
	if !hostRe.MatchString(p.SourceHost) || p.SourcePort <= 0 || p.SourcePort > 65535 {
		return errors.New("primaire invalide")
	}
	switch p.Mode {
	case "":
		p.Mode = "FULL"
	case "SCHEMA", "FULL":
	default:
		return errors.New("mode inconnu (SCHEMA ou FULL)")
	}
	for _, d := range p.Databases {
		if !dbNameRe.MatchString(d) || systemDBs[strings.ToLower(d)] {
			return fmt.Errorf("base invalide : %q", d)
		}
	}
	for _, e := range p.Exclude {
		if !excludeRe.MatchString(e) {
			return fmt.Errorf("exclusion invalide : %q (base.table ou base.*)", e)
		}
	}
	if p.StartAfter != "" && !tableRe.MatchString(p.StartAfter) {
		return errors.New("point de reprise invalide")
	}
	if p.ChunkRows <= 0 {
		p.ChunkRows = 1000
	}
	if p.ChunkRows < 100 || p.ChunkRows > 20000 {
		return errors.New("taille de tranche entre 100 et 20 000 lignes")
	}
	if p.PauseFactor < 0 || p.PauseFactor > 10 {
		return errors.New("facteur de pause entre 0 et 10")
	}
	if p.MaxMinutes <= 0 {
		p.MaxMinutes = 120
	}
	if p.MaxMinutes > 720 {
		return errors.New("durée maximale : 12 heures")
	}
	if p.Rechecks <= 0 {
		p.Rechecks = 3
	}
	if p.Rechecks > 10 {
		p.Rechecks = 10
	}
	return nil
}

func quoteIdent(s string) string { return "`" + strings.ReplaceAll(s, "`", "``") + "`" }

func (r *Runner) consistencyDB(addr string) (*sql.DB, error) {
	pw, err := config.ReadSecret(r.Cfg.ConsistencyCheck.PasswordFile)
	if err != nil {
		return nil, err
	}
	mc := mysql.NewConfig()
	mc.User, mc.Passwd, mc.Net, mc.Addr, mc.Timeout = r.Cfg.ConsistencyCheck.User, pw, "tcp", addr, 5*time.Second
	mc.ReadTimeout = 5 * time.Minute
	db, err := sql.Open("mysql", mc.FormatDSN())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(2)
	// Lecture seule et sans effet sur les journaux : aucune écriture n'est faite par ce compte.
	return db, nil
}

// CheckConsistency compare le schéma puis les données (par tranches de clé primaire).
func (r *Runner) CheckConsistency(ctx context.Context, p ConsistencyParams, rep Reporter) (map[string]any, error) {
	if err := p.validate(); err != nil {
		return nil, err
	}
	t0 := time.Now()
	deadline := t0.Add(time.Duration(p.MaxMinutes) * time.Minute)
	paddr := net.JoinHostPort(p.SourceHost, strconv.Itoa(p.SourcePort))
	prim, err := r.consistencyDB(paddr)
	if err != nil {
		return nil, err
	}
	defer prim.Close()
	repl, err := r.consistencyDB(r.Cfg.MariaDB.Address)
	if err != nil {
		return nil, err
	}
	defer repl.Close()
	psid, err := scalar(ctx, prim, "SELECT @@server_id")
	if err != nil {
		return nil, fmt.Errorf("connexion au primaire %s : %w", paddr, err)
	}
	rsid, err := scalar(ctx, repl, "SELECT @@server_id")
	if err != nil {
		return nil, fmt.Errorf("connexion à la réplique locale : %w", err)
	}
	if psid == rsid {
		return nil, errors.New("le primaire et la réplique ont le même server_id : vérifiez l'adresse du primaire")
	}

	dbs := p.Databases
	if len(dbs) == 0 {
		if dbs, err = userDatabases(ctx, prim); err != nil {
			return nil, err
		}
	}
	excluded := func(table string) bool {
		db := table[:strings.Index(table, ".")]
		for _, e := range p.Exclude {
			if strings.EqualFold(e, table) || strings.EqualFold(e, db+".*") {
				return true
			}
		}
		return false
	}
	rep.Step(fmt.Sprintf("comparaison du schéma de %d base(s) avec le primaire %s", len(dbs), paddr))
	ps, err := schemaObjects(ctx, prim, dbs)
	if err != nil {
		return nil, fmt.Errorf("schéma du primaire : %w", err)
	}
	rs, err := schemaObjects(ctx, repl, dbs)
	if err != nil {
		return nil, fmt.Errorf("schéma de la réplique : %w", err)
	}
	diffs, tablesWithDiff := compareSchemas(ps, rs, excluded)
	if len(diffs) > 0 {
		rep.Warn(fmt.Sprintf("%d différence(s) de schéma", len(diffs)))
	}

	var tables []TableCheck
	completed := true
	lastTable := ""
	var rows int64
	if p.Mode == "FULL" {
		names := baseTables(ps)
		rep.Step(fmt.Sprintf("comparaison des données : %d table(s), tranches de %d lignes", len(names), p.ChunkRows))
		for i, name := range names {
			if p.StartAfter != "" && strings.ToLower(name) <= strings.ToLower(p.StartAfter) {
				continue
			}
			if excluded(name) {
				continue
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if time.Now().After(deadline) {
				completed = false
				rep.Warn(fmt.Sprintf("durée maximale atteinte (%d min) : contrôle à reprendre après %s", p.MaxMinutes, lastTable))
				break
			}
			var tc TableCheck
			if tablesWithDiff[name] {
				tc = TableCheck{Table: name, Status: "SKIPPED", Reason: "structure différente : comparer d'abord le schéma"}
			} else if _, ok := rs.objects["table "+name]; !ok {
				tc = TableCheck{Table: name, Status: "SKIPPED", Reason: "absente de la réplique"}
			} else {
				tc, err = checkTable(ctx, prim, repl, ps, name, p, deadline)
				if err != nil {
					return nil, fmt.Errorf("table %s : %w", name, err)
				}
				if tc.Status == "" { // interrompu par la durée maximale
					completed = false
					rep.Warn(fmt.Sprintf("durée maximale atteinte (%d min) pendant %s : contrôle à reprendre après %s", p.MaxMinutes, name, lastTable))
					break
				}
			}
			rows += tc.Rows
			tables = append(tables, tc)
			lastTable = name
			switch tc.Status {
			case "DIVERGENT":
				rep.Warn(fmt.Sprintf("%s : données différentes dans %d tranche(s)", name, len(tc.Divergences)))
			case "INCONCLUSIVE":
				rep.Warn(fmt.Sprintf("%s : non concluant (%s)", name, tc.Reason))
			}
			if (i+1)%25 == 0 {
				rep.Step(fmt.Sprintf("%d/%d tables, %d lignes comparées", i+1, len(names), rows))
			}
		}
	}

	sum := map[string]int{"tablesChecked": 0, "divergentTables": 0, "inconclusiveTables": 0, "skippedTables": 0, "schemaDifferences": len(diffs)}
	for _, t := range tables {
		switch t.Status {
		case "OK":
			sum["tablesChecked"]++
		case "DIVERGENT":
			sum["tablesChecked"]++
			sum["divergentTables"]++
		case "INCONCLUSIVE":
			sum["inconclusiveTables"]++
		case "SKIPPED":
			sum["skippedTables"]++
		}
	}
	status := "OK"
	switch {
	case len(diffs) > 0 || sum["divergentTables"] > 0:
		status = "DIVERGENT"
	case sum["inconclusiveTables"] > 0 || !completed:
		status = "INCONCLUSIVE"
	}
	rep.Step(fmt.Sprintf("résultat : %s — %d table(s) comparée(s), %d ligne(s), %d différence(s) de schéma, %d table(s) divergente(s)",
		map[string]string{"OK": "cohérente", "DIVERGENT": "ÉCARTS", "INCONCLUSIVE": "non concluant"}[status],
		sum["tablesChecked"], rows, len(diffs), sum["divergentTables"]))
	if len(diffs) > 200 {
		diffs = diffs[:200]
	}
	return map[string]any{
		"status": status, "mode": p.Mode, "primary": paddr, "primaryServerId": psid, "replicaServerId": rsid,
		"databases": dbs, "schemaObjects": len(ps.objects), "schemaDifferences": diffs, "tables": tables, "rows": rows,
		"summary": sum, "completed": completed, "lastTable": lastTable, "totalSeconds": int(time.Since(t0).Seconds()),
	}, nil
}

func userDatabases(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, "SELECT SCHEMA_NAME FROM information_schema.SCHEMATA ORDER BY SCHEMA_NAME")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		if !systemDBs[strings.ToLower(n)] {
			out = append(out, n)
		}
	}
	return out, rows.Err()
}

// schema : objets du schéma et leur description normalisée.
type schema struct {
	objects map[string]string   // « kind base.objet » → description
	columns map[string][]string // base.table → colonnes dans l'ordre
	pk      map[string][]string // base.table → colonnes de la clé primaire
	kinds   map[string]string   // base.table → BASE TABLE / VIEW
}

func schemaObjects(ctx context.Context, db *sql.DB, dbs []string) (*schema, error) {
	s := &schema{objects: map[string]string{}, columns: map[string][]string{}, pk: map[string][]string{}, kinds: map[string]string{}}
	if len(dbs) == 0 {
		return s, nil
	}
	in := strings.TrimSuffix(strings.Repeat("?,", len(dbs)), ",")
	args := make([]any, len(dbs))
	for i, d := range dbs {
		args[i] = d
	}
	type q struct {
		sql string
		fn  func(v []sql.NullString)
	}
	n := func(v sql.NullString) string {
		if !v.Valid {
			return "NULL"
		}
		return v.String
	}
	queries := []q{
		{"SELECT TABLE_SCHEMA, TABLE_NAME, TABLE_TYPE, ENGINE, TABLE_COLLATION FROM information_schema.TABLES WHERE TABLE_SCHEMA IN (" + in + ")",
			func(v []sql.NullString) {
				t := v[0].String + "." + v[1].String
				s.kinds[t] = v[2].String
				kind := "table"
				if v[2].String == "VIEW" {
					kind = "view"
					s.objects["view "+t] = ""
					return
				}
				s.objects[kind+" "+t] = "moteur " + n(v[3]) + ", interclassement " + n(v[4])
			}},
		{"SELECT TABLE_SCHEMA, TABLE_NAME, COLUMN_NAME, ORDINAL_POSITION, COLUMN_TYPE, IS_NULLABLE, COLUMN_DEFAULT, EXTRA, COLLATION_NAME FROM information_schema.COLUMNS WHERE TABLE_SCHEMA IN (" + in + ") ORDER BY TABLE_SCHEMA, TABLE_NAME, ORDINAL_POSITION",
			func(v []sql.NullString) {
				t := v[0].String + "." + v[1].String
				s.columns[t] = append(s.columns[t], v[2].String)
				if s.kinds[t] == "VIEW" {
					return
				}
				d := fmt.Sprintf("%s, position %s, NULL %s, défaut %s", v[4].String, v[3].String,
					map[string]string{"YES": "permis", "NO": "interdit"}[v[5].String], n(v[6]))
				if v[7].String != "" {
					d += ", " + v[7].String
				}
				if v[8].Valid {
					d += ", interclassement " + v[8].String
				}
				s.objects["column "+t+"."+v[2].String] = d
			}},
		{"SELECT TABLE_SCHEMA, TABLE_NAME, INDEX_NAME, NON_UNIQUE, GROUP_CONCAT(CONCAT(COLUMN_NAME, IFNULL(CONCAT('(', SUB_PART, ')'), '')) ORDER BY SEQ_IN_INDEX) FROM information_schema.STATISTICS WHERE TABLE_SCHEMA IN (" + in + ") GROUP BY TABLE_SCHEMA, TABLE_NAME, INDEX_NAME, NON_UNIQUE",
			func(v []sql.NullString) {
				t := v[0].String + "." + v[1].String
				u := "unique"
				if v[3].String == "1" {
					u = "non unique"
				}
				s.objects["index "+t+"."+v[2].String] = "(" + v[4].String + ") " + u
				if v[2].String == "PRIMARY" {
					for _, c := range strings.Split(v[4].String, ",") {
						if i := strings.Index(c, "("); i >= 0 {
							c = c[:i]
						}
						s.pk[t] = append(s.pk[t], c)
					}
				}
			}},
		{"SELECT ROUTINE_SCHEMA, ROUTINE_NAME, ROUTINE_TYPE, MD5(COALESCE(ROUTINE_DEFINITION, '')) FROM information_schema.ROUTINES WHERE ROUTINE_SCHEMA IN (" + in + ")",
			func(v []sql.NullString) {
				s.objects["routine "+v[0].String+"."+v[1].String] = v[2].String + " empreinte " + v[3].String[:12]
			}},
		{"SELECT TABLE_SCHEMA, TABLE_NAME, MD5(COALESCE(VIEW_DEFINITION, '')) FROM information_schema.VIEWS WHERE TABLE_SCHEMA IN (" + in + ")",
			func(v []sql.NullString) {
				s.objects["view "+v[0].String+"."+v[1].String] = "empreinte " + v[2].String[:12]
			}},
		{"SELECT TRIGGER_SCHEMA, TRIGGER_NAME, ACTION_TIMING, EVENT_MANIPULATION, EVENT_OBJECT_TABLE, MD5(ACTION_STATEMENT) FROM information_schema.TRIGGERS WHERE TRIGGER_SCHEMA IN (" + in + ")",
			func(v []sql.NullString) {
				s.objects["trigger "+v[0].String+"."+v[1].String] = v[2].String + " " + v[3].String + " sur " + v[4].String + ", empreinte " + v[5].String[:12]
			}},
		// L'état des événements diffère normalement (désactivés sur une réplique) : seule la définition compte.
		{"SELECT EVENT_SCHEMA, EVENT_NAME, MD5(COALESCE(EVENT_DEFINITION, '')) FROM information_schema.EVENTS WHERE EVENT_SCHEMA IN (" + in + ")",
			func(v []sql.NullString) {
				s.objects["event "+v[0].String+"."+v[1].String] = "empreinte " + v[2].String[:12]
			}},
	}
	for _, qq := range queries {
		rows, err := db.QueryContext(ctx, qq.sql, args...)
		if err != nil {
			return nil, err
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			v := make([]sql.NullString, len(cols))
			ptr := make([]any, len(cols))
			for i := range v {
				ptr[i] = &v[i]
			}
			if err := rows.Scan(ptr...); err != nil {
				rows.Close()
				return nil, err
			}
			qq.fn(v)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return s, nil
}

// objectTable rend « base.table » pour un objet rattaché à une table (table, colonne, index, vue).
func objectTable(key string) string {
	sp := strings.Index(key, " ")
	kind, name := key[:sp], key[sp+1:]
	parts := strings.SplitN(name, ".", 3)
	if len(parts) < 2 {
		return ""
	}
	switch kind {
	case "table", "column", "index", "view":
		return parts[0] + "." + parts[1]
	}
	return parts[0] + "." + "*"
}

func compareSchemas(p, r *schema, excluded func(string) bool) ([]SchemaDiff, map[string]bool) {
	var out []SchemaDiff
	affected := map[string]bool{}
	keys := map[string]bool{}
	for k := range p.objects {
		keys[k] = true
	}
	for k := range r.objects {
		keys[k] = true
	}
	sorted := make([]string, 0, len(keys))
	for k := range keys {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)
	for _, k := range sorted {
		t := objectTable(k)
		if t != "" && excluded(t) {
			continue
		}
		pv, pok := p.objects[k]
		rv, rok := r.objects[k]
		sp := strings.Index(k, " ")
		d := SchemaDiff{Kind: k[:sp], Object: k[sp+1:], Primary: pv, Replica: rv}
		switch {
		case pok && !rok:
			d.Issue = "MISSING_ON_REPLICA"
		case !pok && rok:
			d.Issue = "EXTRA_ON_REPLICA"
		case pv != rv:
			d.Issue = "DIFFERENT"
		default:
			continue
		}
		// Une table absente d'un côté est signalée une fois, sans le détail de ses colonnes et index.
		if d.Kind == "column" || d.Kind == "index" {
			_, a := p.objects["table "+t]
			_, b := r.objects["table "+t]
			if a != b {
				continue
			}
		}
		// Un index différent n'empêche pas de comparer les données ; une table ou une colonne, si.
		if d.Kind == "table" || d.Kind == "column" {
			affected[t] = true
		}
		out = append(out, d)
	}
	return out, affected
}

func baseTables(s *schema) []string {
	var out []string
	for t, k := range s.kinds {
		if k == "BASE TABLE" || k == "SYSTEM VERSIONED" {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i]) < strings.ToLower(out[j]) })
	return out
}

// chunkSQL prépare les requêtes d'une table : bornes, somme de contrôle, empreintes par ligne.
type chunkSQL struct {
	table, keyCols, rowHash string
	pk                      []string
}

func newChunkSQL(table string, cols, pk []string) chunkSQL {
	dot := strings.Index(table, ".")
	qt := quoteIdent(table[:dot]) + "." + quoteIdent(table[dot+1:])
	qc := make([]string, len(cols))
	nulls := make([]string, len(cols))
	for i, c := range cols {
		qc[i] = quoteIdent(c)
		nulls[i] = "ISNULL(" + qc[i] + ")"
	}
	qk := make([]string, len(pk))
	for i, c := range pk {
		qk[i] = quoteIdent(c)
	}
	return chunkSQL{table: qt, keyCols: strings.Join(qk, ", "), pk: pk,
		rowHash: "MD5(CONCAT_WS('#', " + strings.Join(qc, ", ") + ", CONCAT(" + strings.Join(nulls, ", ") + ")))"}
}

func (c chunkSQL) tuple() string {
	if len(c.pk) == 1 {
		return c.keyCols
	}
	return "(" + c.keyCols + ")"
}

func (c chunkSQL) placeholders() string {
	if len(c.pk) == 1 {
		return "?"
	}
	return "(" + strings.TrimSuffix(strings.Repeat("?,", len(c.pk)), ",") + ")"
}

func (c chunkSQL) where(lo, hi []any) (string, []any) {
	var conds []string
	var args []any
	if lo != nil {
		conds = append(conds, c.tuple()+" > "+c.placeholders())
		args = append(args, lo...)
	}
	if hi != nil {
		conds = append(conds, c.tuple()+" <= "+c.placeholders())
		args = append(args, hi...)
	}
	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

func (c chunkSQL) checksum(ctx context.Context, db *sql.DB, lo, hi []any) (int64, string, error) {
	w, args := c.where(lo, hi)
	q := "SELECT COUNT(*), COALESCE(BIT_XOR(CAST(CONV(SUBSTRING(" + c.rowHash + ", 1, 16), 16, 10) AS UNSIGNED)), 0) FROM " + c.table + w
	var n int64
	var sum string
	err := db.QueryRowContext(ctx, q, args...).Scan(&n, &sum)
	return n, sum, err
}

// upper rend la clé de la dernière ligne de la tranche commençant après lo, ou nil en fin de table.
func (c chunkSQL) upper(ctx context.Context, db *sql.DB, lo []any, n int) ([]any, error) {
	w, args := c.where(lo, nil)
	q := "SELECT " + c.keyCols + " FROM " + c.table + w + " ORDER BY " + c.keyCols + " LIMIT 1 OFFSET " + strconv.Itoa(n-1)
	vals := make([]sql.RawBytes, len(c.pk))
	ptr := make([]any, len(vals))
	for i := range vals {
		ptr[i] = &vals[i]
	}
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	if err := rows.Scan(ptr...); err != nil {
		return nil, err
	}
	out := make([]any, len(vals))
	for i, v := range vals {
		if v == nil {
			out[i] = nil
		} else {
			out[i] = string(v)
		}
	}
	return out, nil
}

func (c chunkSQL) rowHashes(ctx context.Context, db *sql.DB, lo, hi []any, limit int) (map[string]string, []string, error) {
	w, args := c.where(lo, hi)
	q := "SELECT CONCAT_WS(',', " + c.keyCols + "), " + c.rowHash + " FROM " + c.table + w + " ORDER BY " + c.keyCols + " LIMIT " + strconv.Itoa(limit)
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	m := map[string]string{}
	var order []string
	for rows.Next() {
		var k, h sql.NullString
		if err := rows.Scan(&k, &h); err != nil {
			return nil, nil, err
		}
		m[k.String] = h.String
		order = append(order, k.String)
	}
	return m, order, rows.Err()
}

func keyString(k []any) string {
	if k == nil {
		return ""
	}
	parts := make([]string, len(k))
	for i, v := range k {
		parts[i] = fmt.Sprint(v)
	}
	return strings.Join(parts, ",")
}

// waitReplica attend que la réplique ait appliqué la position du primaire (au plus 30 s).
func waitReplica(ctx context.Context, prim, repl *sql.DB) (bool, error) {
	pos, err := scalar(ctx, prim, "SELECT @@gtid_binlog_pos")
	if err != nil || pos == "" {
		return err == nil, err
	}
	res, err := scalar(ctx, repl, "SELECT MASTER_GTID_WAIT('"+strings.ReplaceAll(pos, "'", "")+"', 30)")
	if err != nil {
		return false, err
	}
	return res == "0", nil
}

type chunkRange struct{ lo, hi []any }

func checkTable(ctx context.Context, prim, repl *sql.DB, s *schema, name string, p ConsistencyParams, deadline time.Time) (TableCheck, error) {
	t0 := time.Now()
	tc := TableCheck{Table: name}
	pk := s.pk[name]
	cols := s.columns[name]
	c := newChunkSQL(name, cols, pk)
	if len(pk) == 0 {
		// Sans clé primaire : comparaison globale si la table est petite.
		n, _, err := c.checksum(ctx, prim, nil, nil)
		if err != nil {
			return tc, err
		}
		if n > 50000 {
			tc.Status, tc.Reason, tc.Rows = "SKIPPED", "pas de clé primaire et plus de 50 000 lignes", n
			return tc, nil
		}
	}
	var suspects []chunkRange
	var lo []any
	for {
		if err := ctx.Err(); err != nil {
			return tc, err
		}
		if time.Now().After(deadline) {
			return TableCheck{}, nil
		}
		cs := time.Now()
		var hi []any
		var err error
		if len(pk) > 0 {
			if hi, err = c.upper(ctx, prim, lo, p.ChunkRows); err != nil {
				return tc, err
			}
		}
		pn, psum, err := c.checksum(ctx, prim, lo, hi)
		if err != nil {
			return tc, err
		}
		if _, err := waitReplica(ctx, prim, repl); err != nil {
			return tc, err
		}
		rn, rsum, err := c.checksum(ctx, repl, lo, hi)
		if err != nil {
			return tc, err
		}
		tc.Rows += pn
		tc.Chunks++
		if pn != rn || psum != rsum {
			suspects = append(suspects, chunkRange{lo, hi})
		}
		// Débit limité : pause proportionnelle au temps passé, pour ne pas peser sur la production.
		if p.PauseFactor > 0 {
			time.Sleep(time.Duration(float64(time.Since(cs)) * p.PauseFactor))
		}
		if hi == nil || len(pk) == 0 {
			break
		}
		lo = hi
	}
	// Recontrôle des tranches suspectes : un écart passager (écriture pendant la lecture) disparaît.
	hot := 0
	for _, sc := range suspects {
		confirmed := true
		var lastP string
		changing := false
		for i := 0; i < p.Rechecks; i++ {
			time.Sleep(time.Duration(i+1) * 2 * time.Second)
			pn, psum, err := c.checksum(ctx, prim, sc.lo, sc.hi)
			if err != nil {
				return tc, err
			}
			if lastP != "" && lastP != psum {
				changing = true
			}
			lastP = psum
			if _, err := waitReplica(ctx, prim, repl); err != nil {
				return tc, err
			}
			rn, rsum, err := c.checksum(ctx, repl, sc.lo, sc.hi)
			if err != nil {
				return tc, err
			}
			if pn == rn && psum == rsum {
				confirmed = false
				break
			}
		}
		if !confirmed {
			continue
		}
		if changing {
			hot++
			continue
		}
		d, err := diffChunk(ctx, prim, repl, c, sc, p.ChunkRows)
		if err != nil {
			return tc, err
		}
		tc.Divergences = append(tc.Divergences, d)
	}
	tc.Seconds = time.Since(t0).Seconds()
	switch {
	case len(tc.Divergences) > 0:
		tc.Status = "DIVERGENT"
		if len(tc.Divergences) > 50 {
			tc.Divergences = tc.Divergences[:50]
		}
	case hot > 0:
		tc.Status, tc.Reason = "INCONCLUSIVE", fmt.Sprintf("%d tranche(s) modifiée(s) pendant le contrôle", hot)
	default:
		tc.Status = "OK"
	}
	return tc, nil
}

// diffChunk compare ligne à ligne une tranche divergente et rend les clés concernées.
func diffChunk(ctx context.Context, prim, repl *sql.DB, c chunkSQL, sc chunkRange, chunk int) (ChunkDiff, error) {
	limit := chunk*2 + 10
	pm, porder, err := c.rowHashes(ctx, prim, sc.lo, sc.hi, limit)
	if err != nil {
		return ChunkDiff{}, err
	}
	if _, err := waitReplica(ctx, prim, repl); err != nil {
		return ChunkDiff{}, err
	}
	rm, rorder, err := c.rowHashes(ctx, repl, sc.lo, sc.hi, limit)
	if err != nil {
		return ChunkDiff{}, err
	}
	d := ChunkDiff{Lower: keyString(sc.lo), Upper: keyString(sc.hi), PrimaryRows: int64(len(pm)), ReplicaRows: int64(len(rm))}
	add := func(k, issue string) {
		if len(d.Keys) < 50 {
			d.Keys = append(d.Keys, KeyDiff{Key: k, Issue: issue})
		} else {
			d.MoreKeys = true
		}
	}
	for _, k := range porder {
		rh, ok := rm[k]
		switch {
		case !ok:
			add(k, "MISSING_ON_REPLICA")
		case rh != pm[k]:
			add(k, "DIFFERENT")
		}
	}
	for _, k := range rorder {
		if _, ok := pm[k]; !ok {
			add(k, "EXTRA_ON_REPLICA")
		}
	}
	return d, nil
}
