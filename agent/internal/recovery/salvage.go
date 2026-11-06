package recovery

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const salvageDB = "prm_sauvetage"

// salvage : quand une table ne se lit pas d'un bloc (pages endommagées), ses lignes sont copiées
// par tranches dans l'ordre de la clé, depuis le début puis depuis la fin ; entre les deux, les clés
// encore listées par un index secondaire sont relues une à une. Seules les lignes des pages
// abîmées sont perdues. Demande une instance accessible en écriture (innodb_force_recovery ≤ 3).
func (j *job) salvage(ctx context.Context, in *instance, d Database, t TableFiles, tr *TableResult, method string) error {
	db := in.DB
	src := qi(d.Name) + "." + qi(t.Name)
	dst := qi(salvageDB) + "." + qi(t.Name)
	key := clusterKey(ctx, db, d.Name, t.Name)
	if len(key) == 0 {
		return fmt.Errorf("pas de clé primaire ni unique : sauvetage ligne à ligne impossible")
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	for _, q := range []string{"SET SESSION foreign_key_checks=0", "SET SESSION sql_log_bin=0",
		"CREATE DATABASE IF NOT EXISTS " + qi(salvageDB), "DROP TABLE IF EXISTS " + dst,
		"CREATE TABLE " + dst + " LIKE " + src, "ALTER TABLE " + dst + " ENGINE=InnoDB"} {
		if _, err := conn.ExecContext(ctx, q); err != nil && !strings.HasPrefix(q, "SET SESSION sql_log_bin") {
			return fmt.Errorf("préparation du sauvetage : %w", err)
		}
	}
	defer conn.ExecContext(context.Background(), "DROP TABLE IF EXISTS "+dst)
	cols := make([]string, len(key))
	for i, k := range key {
		cols[i] = qi(k)
	}
	tuple := "(" + strings.Join(cols, ",") + ")"
	asc, desc := strings.Join(cols, ","), strings.Join(cols, " DESC,")+" DESC"
	ph := "(" + strings.TrimSuffix(strings.Repeat("?,", len(key)), ",") + ")"

	// Avance par tranches ; une tranche en échec est retentée plus petite, jusqu'à une ligne.
	walk := func(cmp, order string, bound []any) ([]any, int64) {
		var last []any
		var copied int64
		batch := 1000
		for ctx.Err() == nil {
			where := ""
			args := []any{}
			if last != nil {
				where = " WHERE " + tuple + " " + cmp + " " + ph
				args = append(args, last...)
			}
			if bound != nil {
				opp := map[string]string{">": "<", "<": ">"}[cmp]
				if where == "" {
					where = " WHERE "
				} else {
					where += " AND "
				}
				where += tuple + " " + opp + " " + ph
				args = append(args, bound...)
			}
			res, err := conn.ExecContext(ctx, fmt.Sprintf("INSERT IGNORE INTO %s SELECT * FROM %s%s ORDER BY %s LIMIT %d", dst, src, where, order, batch), args...)
			if err != nil {
				if batch == 1 || !in.Alive() {
					return last, copied
				}
				batch /= 10
				continue
			}
			n, _ := res.RowsAffected()
			// Dernière clé lue : extrême de la table de sauvetage dans le sens de l'avance.
			var probe string
			if cmp == ">" {
				probe = fmt.Sprintf("SELECT %s FROM %s ORDER BY %s LIMIT 1", asc, dst, desc)
			} else {
				probe = fmt.Sprintf("SELECT %s FROM %s%s ORDER BY %s LIMIT 1", asc, dst, lowerPart(bound, tuple, ph), asc)
			}
			args2 := []any{}
			if cmp == "<" && bound != nil {
				args2 = bound
			}
			if k, err := scanKey(ctx, conn, probe, len(key), args2...); err == nil && k != nil {
				last = k
			}
			copied += n
			if n < int64(batch) && batch == 1000 {
				return nil, copied // fin de table atteinte sans erreur
			}
			if n == 0 {
				return last, copied
			}
			if batch < 1000 {
				batch = min(batch*10, 1000)
			}
		}
		return last, copied
	}
	stop, fwd := walk(">", asc, nil)
	var gap, lost int64
	var bwd int64
	if stop != nil {
		// Depuis la fin de la table, à rebours, jusqu'à la zone abîmée.
		var top []any
		top, bwd = walk("<", desc, stop)
		if top == nil {
			top = nil
		}
		// Entre les deux : clés connues d'un index secondaire, relues une à une.
		gap, lost = j.salvageGap(ctx, conn, d, t, src, dst, tuple, ph, key, stop, top)
	}
	total := fwd + bwd + gap
	// Estimation des pertes : nombre de lignes connu d'un index secondaire encore lisible.
	if lost == 0 {
		if ixs, err := conn.QueryContext(ctx, `SELECT DISTINCT INDEX_NAME FROM information_schema.STATISTICS WHERE TABLE_SCHEMA=? AND TABLE_NAME=? AND INDEX_NAME<>'PRIMARY'`, d.Name, t.Name); err == nil {
			var names []string
			for ixs.Next() {
				var n string
				if ixs.Scan(&n) == nil {
					names = append(names, n)
				}
			}
			ixs.Close()
			for _, n := range names {
				var c int64
				if conn.QueryRowContext(ctx, fmt.Sprintf("SELECT COUNT(*) FROM %s FORCE INDEX(%s)", src, qi(n))).Scan(&c) == nil && c > total {
					lost = c - total
					break
				}
			}
		}
	}
	if total == 0 {
		return fmt.Errorf("aucune ligne lisible")
	}
	file := filepath.Join(j.sqlDir, safe(d.Name), safe(t.Name)+".sql")
	var name, ddl string
	if err := conn.QueryRowContext(ctx, "SHOW CREATE TABLE "+src).Scan(&name, &ddl); err != nil {
		return err
	}
	// Export de la copie de sauvetage, sous le nom et la structure d'origine.
	if err := j.dumpSalvaged(ctx, in, d.Name, t.Name, ddl, file); err != nil {
		return err
	}
	j.ok(tr, method+", sauvetage ligne à ligne", file, total)
	if lost > 0 {
		tr.Warning = fmt.Sprintf("table endommagée : %d lignes sauvées, environ %d perdues (pages illisibles)", total, lost)
	} else {
		tr.Warning = fmt.Sprintf("table endommagée : %d lignes sauvées ; les lignes des pages illisibles sont perdues (nombre inconnu)", total)
	}
	tr.Partial = true
	return nil
}

func lowerPart(bound []any, tuple, ph string) string {
	if bound == nil {
		return ""
	}
	return " WHERE " + tuple + " > " + ph
}

func scanKey(ctx context.Context, conn *sql.Conn, q string, n int, args ...any) ([]any, error) {
	vals := make([]sql.RawBytes, n)
	ptr := make([]any, n)
	for i := range vals {
		ptr[i] = &vals[i]
	}
	rows, err := conn.QueryContext(ctx, q, args...)
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
	out := make([]any, n)
	for i, v := range vals {
		out[i] = string(v)
	}
	return out, nil
}

// salvageGap relit une à une les lignes dont un index secondaire connaît la clé, entre lo et hi.
func (j *job) salvageGap(ctx context.Context, conn *sql.Conn, d Database, t TableFiles, src, dst, tuple, ph string, key []string, lo, hi []any) (int64, int64) {
	rows, err := conn.QueryContext(ctx, `SELECT DISTINCT INDEX_NAME FROM information_schema.STATISTICS WHERE TABLE_SCHEMA=? AND TABLE_NAME=? AND INDEX_NAME<>'PRIMARY'`, d.Name, t.Name)
	if err != nil {
		return 0, 0
	}
	var idx []string
	for rows.Next() {
		var n string
		if rows.Scan(&n) == nil {
			idx = append(idx, n)
		}
	}
	rows.Close()
	cols := make([]string, len(key))
	for i, k := range key {
		cols[i] = qi(k)
	}
	where := " WHERE " + tuple + " > " + ph
	args := append([]any{}, lo...)
	if hi != nil {
		where += " AND " + tuple + " < " + ph
		args = append(args, hi...)
	}
	for _, ix := range idx {
		keys, err := conn.QueryContext(ctx, fmt.Sprintf("SELECT %s FROM %s FORCE INDEX(%s)%s", strings.Join(cols, ","), src, qi(ix), where), args...)
		if err != nil {
			continue
		}
		var list [][]any
		for keys.Next() {
			vals := make([]sql.RawBytes, len(key))
			ptr := make([]any, len(key))
			for i := range vals {
				ptr[i] = &vals[i]
			}
			if keys.Scan(ptr...) == nil {
				k := make([]any, len(key))
				for i, v := range vals {
					k[i] = string(v)
				}
				list = append(list, k)
			}
		}
		if keys.Err() != nil {
			keys.Close()
			continue
		}
		keys.Close()
		var got int64
		for _, k := range list {
			if ctx.Err() != nil {
				break
			}
			res, err := conn.ExecContext(ctx, fmt.Sprintf("INSERT IGNORE INTO %s SELECT * FROM %s WHERE %s = %s", dst, src, tuple, ph), k...)
			if err == nil {
				n, _ := res.RowsAffected()
				got += n
			}
		}
		return got, int64(len(list)) - got
	}
	return 0, 0
}

func (j *job) dumpSalvaged(ctx context.Context, in *instance, db, table, ddl, file string) error {
	if err := os.MkdirAll(filepath.Dir(file), 0o750); err != nil {
		return err
	}
	f, err := os.Create(file + ".partiel")
	if err != nil {
		return err
	}
	fmt.Fprintf(f, "-- Récupération Prestige Replica Manager : %s.%s (sauvetage ligne à ligne)\n%sSET FOREIGN_KEY_CHECKS=0;\nDROP TABLE IF EXISTS %s;\n%s;\n", db, table, j.dbHead(db), qi(table), ddl)
	f.Sync()
	err = runCmd(ctx, f, j.t.Dump, in.dumpArgs("--skip-triggers", "--no-create-db", "--no-create-info", salvageDB, table)...)
	fmt.Fprintln(f, "SET FOREIGN_KEY_CHECKS=1;")
	f.Close()
	if err != nil {
		os.Remove(file + ".partiel")
		return err
	}
	return os.Rename(file+".partiel", file)
}

// clusterKey : colonnes de la clé primaire, ou du premier index unique sur colonnes NOT NULL.
func clusterKey(ctx context.Context, db *sql.DB, schema, table string) []string {
	rows, err := db.QueryContext(ctx, `SELECT INDEX_NAME, COLUMN_NAME, NON_UNIQUE, NULLABLE FROM information_schema.STATISTICS
		WHERE TABLE_SCHEMA=? AND TABLE_NAME=? ORDER BY INDEX_NAME='PRIMARY' DESC, INDEX_NAME, SEQ_IN_INDEX`, schema, table)
	if err != nil {
		return nil
	}
	defer rows.Close()
	type ix struct {
		cols []string
		ok   bool
	}
	m := map[string]*ix{}
	var order []string
	for rows.Next() {
		var name, col, nullable string
		var nonUnique int
		if rows.Scan(&name, &col, &nonUnique, &nullable) != nil {
			continue
		}
		x, found := m[name]
		if !found {
			x = &ix{ok: nonUnique == 0}
			m[name] = x
			order = append(order, name)
		}
		x.cols = append(x.cols, col)
		if nullable == "YES" {
			x.ok = false
		}
	}
	for _, n := range order {
		if m[n].ok {
			return m[n].cols
		}
	}
	return nil
}
