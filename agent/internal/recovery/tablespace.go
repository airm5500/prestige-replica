package recovery

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var rowFormats = []string{"", "DYNAMIC", "COMPACT", "REDUNDANT", "COMPRESSED"}

var (
	engineClause = regexp.MustCompile(`(?i)ENGINE=BLACKHOLE`)
	rowFmtClause = regexp.MustCompile(`(?i)\s*ROW_FORMAT=\w+`)
	mismatchRe   = regexp.MustCompile(`(?i)schema mismatch|row_format|flags don't match|Table flags`)
)

// importTablespaces : instance neuve, structure reconstituée depuis le .frm (moteur BLACKHOLE),
// puis rattachement du fichier .ibd récupéré (DISCARD / IMPORT TABLESPACE). Les tables Aria
// et MyISAM sont recopiées et réparées (REPAIR TABLE).
func (j *job) importTablespaces(ctx context.Context) {
	fresh := filepath.Join(j.o.Work, "neuve")
	if err := os.MkdirAll(fresh, 0o750); err != nil {
		j.r.Attempts = append(j.r.Attempts, Attempt{Method: "import des fichiers de tables", Error: err.Error()})
		return
	}
	if j.o.RunAs != "" {
		chownTree(fresh, j.o.RunAs)
	}
	// Seuls les réglages de format de page comptent pour une instance neuve.
	ini := map[string]string{}
	for _, k := range []string{"innodb_page_size", "lower_case_table_names", "innodb_checksum_algorithm"} {
		if v, ok := j.ini[k]; ok {
			ini[k] = v
		}
	}
	method := "import du fichier .ibd (structure tirée du .frm)"
	j.log("Instance neuve pour l'import des fichiers de tables")
	start := func() (*instance, error) {
		return startInstance(ctx, j.t, startSpec{datadir: fresh, runDir: j.runDir, name: "import", blackhole: true, ini: ini,
			runAs: j.o.RunAs, bufferMB: j.o.BufferMB})
	}
	in, err := start()
	a := Attempt{Method: "instance neuve (import des fichiers de tables)", Started: err == nil}
	if err != nil {
		b, _ := os.ReadFile(filepath.Join(j.runDir, "import.err"))
		a.Error, a.Log = err.Error(), lastLines(errorOnly(string(b)), 6)
		j.r.Attempts = append(j.r.Attempts, a)
		return
	}
	j.r.Attempts = append(j.r.Attempts, a)
	defer func() { in.Stop() }()
	var bh string
	_ = in.DB.QueryRowContext(ctx, "SELECT SUPPORT FROM information_schema.ENGINES WHERE ENGINE='BLACKHOLE'").Scan(&bh)
	if bh != "YES" {
		j.r.Repairs = append(j.r.Repairs, "greffon BLACKHOLE indisponible : structures non lisibles depuis les .frm")
	}

	defer func() { j.objectsFromFiles(ctx, in, fresh) }()
	for _, d := range j.inv.Databases {
		for _, t := range d.Tables {
			tr := j.tables[d.Name+"."+t.Name]
			if tr.Status == "RECUPEREE" || ctx.Err() != nil {
				continue
			}
			if !in.Alive() {
				in.Stop()
				if in, err = start(); err != nil {
					j.r.Attempts = append(j.r.Attempts, Attempt{Method: "redémarrage de l'instance neuve", Error: err.Error()})
					return
				}
			}
			if _, err := in.DB.ExecContext(ctx, "CREATE DATABASE IF NOT EXISTS "+qi(d.Name)); err != nil {
				tr.Error = err.Error()
				continue
			}
			var e error
			switch {
			case t.Partitioned:
				e = fmt.Errorf("table partitionnée : import fichier par fichier non automatisé")
			case !t.has(".frm"):
				e = fmt.Errorf("fichier .frm absent : structure inconnue (la fournir depuis une ancienne sauvegarde ou la réplique)")
			case t.has(".ibd"):
				e = j.importIBD(ctx, in, fresh, d, t, tr, method)
			case t.has(".mai") || t.has(".myi"):
				e = j.repairCopy(ctx, in, fresh, d, t, tr)
			case strings.EqualFold(t.Engine, "MEMORY"):
				e = fmt.Errorf("table MEMORY : son contenu n'existait qu'en mémoire")
			default:
				e = fmt.Errorf("aucun fichier de données (.ibd absent : table stockée dans ibdata1 ?)")
			}
			if e != nil {
				if tr.Error != "" && !strings.HasPrefix(tr.Error, "!crash") {
					tr.Error = tr.Error + " ; import : " + e.Error()
				} else {
					tr.Error = strings.TrimPrefix(tr.Error, "!crash") + e.Error()
				}
				j.log("  %s.%s : %s", d.Name, t.Name, short(e.Error()))
			}
		}
	}
}

// structure lit la définition d'une table depuis son .frm, via le moteur BLACKHOLE.
func (j *job) structure(ctx context.Context, in *instance, fresh string, d Database, t TableFiles) (string, error) {
	frm, err := os.ReadFile(t.path(filepath.Join(j.data, d.Dir), ".frm"))
	if err != nil {
		return "", err
	}
	patched, err := PatchFRMEngine(frm, "BLACKHOLE", 19)
	if err != nil {
		return "", fmt.Errorf(".frm illisible : %w", err)
	}
	dst := filepath.Join(fresh, d.Dir, t.File+".frm")
	if err := os.WriteFile(dst, patched, 0o640); err != nil {
		return "", err
	}
	if j.o.RunAs != "" {
		chownTree(dst, j.o.RunAs)
	}
	_, _ = in.DB.ExecContext(ctx, "FLUSH TABLES")
	var name, ddl string
	err = in.DB.QueryRowContext(ctx, fmt.Sprintf("SHOW CREATE TABLE %s.%s", qi(d.Name), qi(t.Name))).Scan(&name, &ddl)
	_, _ = in.DB.ExecContext(ctx, fmt.Sprintf("DROP TABLE IF EXISTS %s.%s", qi(d.Name), qi(t.Name)))
	_ = os.Remove(dst)
	if err != nil {
		return "", fmt.Errorf("structure illisible depuis le .frm : %w", err)
	}
	return engineClause.ReplaceAllString(ddl, "ENGINE="+firstNon(t.Engine, "InnoDB")), nil
}

func (j *job) importIBD(ctx context.Context, in *instance, fresh string, d Database, t TableFiles, tr *TableResult, method string) error {
	ddl, err := j.structure(ctx, in, fresh, d, t)
	if err != nil {
		return err
	}
	ddl = regexp.MustCompile(`(?i)ENGINE=\w+`).ReplaceAllString(ddl, "ENGINE=InnoDB")
	db := in.DB
	full := qi(d.Name) + "." + qi(t.Name)
	var last error
	for _, rf := range rowFormats {
		create := ddl
		if rf != "" {
			create = rowFmtClause.ReplaceAllString(ddl, "") + " ROW_FORMAT=" + rf
		}
		_, _ = db.ExecContext(ctx, "DROP TABLE IF EXISTS "+full)
		_ = os.Remove(filepath.Join(fresh, d.Dir, t.File+".ibd"))
		if _, err := db.ExecContext(ctx, "USE "+qi(d.Name)); err != nil {
			return err
		}
		if _, err := db.ExecContext(ctx, "SET SESSION foreign_key_checks=0, SESSION innodb_strict_mode=0"); err != nil {
			return err
		}
		if _, err := db.ExecContext(ctx, create); err != nil {
			last = fmt.Errorf("création : %w", err)
			continue
		}
		// Sans fichier .cfg, InnoDB n'accepte l'import qu'avec le seul index principal.
		if drop := secondaryIndexes(ctx, db, d.Name, t.Name); len(drop) > 0 {
			parts := make([]string, len(drop))
			for i, ix := range drop {
				parts[i] = "DROP INDEX " + qi(ix)
			}
			if _, err := db.ExecContext(ctx, "ALTER TABLE "+full+" "+strings.Join(parts, ", ")); err != nil {
				last = fmt.Errorf("préparation : %w", err)
				continue
			}
		}
		if _, err := db.ExecContext(ctx, "ALTER TABLE "+full+" DISCARD TABLESPACE"); err != nil {
			last = err
			continue
		}
		dst := filepath.Join(fresh, d.Dir, t.File+".ibd")
		if err := copyFile(t.path(filepath.Join(j.data, d.Dir), ".ibd"), dst); err != nil {
			return err
		}
		if j.o.RunAs != "" {
			chownTree(dst, j.o.RunAs)
		}
		_, err := db.ExecContext(ctx, "ALTER TABLE "+full+" IMPORT TABLESPACE")
		if err == nil {
			file := filepath.Join(j.sqlDir, safe(d.Name), safe(t.Name)+".sql")
			rows, err := j.dumpTable(ctx, in, d.Name, t.Name, file, &create)
			if err != nil {
				return fmt.Errorf("import réussi mais export impossible : %w", err)
			}
			j.ok(tr, method, file, rows)
			tr.Warning = "clés étrangères non recréées (absentes du .frm) ; contenu tel qu'au dernier enregistrement du fichier .ibd"
			if rf != "" {
				tr.Method += ", ROW_FORMAT=" + rf
			}
			return nil
		}
		last = err
		if !in.Alive() {
			return fmt.Errorf("fichier .ibd endommagé (arrêt du serveur à l'import)")
		}
		if !mismatchRe.MatchString(err.Error()) {
			break
		}
	}
	_, _ = db.ExecContext(ctx, "DROP TABLE IF EXISTS "+full)
	return fmt.Errorf("import du .ibd impossible : %v", last)
}

// secondaryIndexes : index à retirer avant l'import (on garde le PRIMARY, ou à défaut le premier
// index unique sur des colonnes NOT NULL, qu'InnoDB utilise comme index principal).
func secondaryIndexes(ctx context.Context, db *sql.DB, schema, table string) []string {
	rows, err := db.QueryContext(ctx, `SELECT INDEX_NAME, MIN(NON_UNIQUE), MAX(NULLABLE='YES'), MIN(INDEX_TYPE)
		FROM information_schema.STATISTICS WHERE TABLE_SCHEMA=? AND TABLE_NAME=? GROUP BY INDEX_NAME ORDER BY MIN(SEQ_IN_INDEX), INDEX_NAME`, schema, table)
	if err != nil {
		return nil
	}
	defer rows.Close()
	type ix struct {
		name           string
		unique, notNul bool
	}
	var all []ix
	hasPK := false
	for rows.Next() {
		var name, typ string
		var nonUnique, nullable int
		if rows.Scan(&name, &nonUnique, &nullable, &typ) == nil {
			all = append(all, ix{name, nonUnique == 0, nullable == 0})
			if name == "PRIMARY" {
				hasPK = true
			}
		}
	}
	// Ordre de définition : celui de SHOW CREATE TABLE.
	var ddlName, ddl string
	_ = db.QueryRowContext(ctx, fmt.Sprintf("SHOW CREATE TABLE %s.%s", qi(schema), qi(table))).Scan(&ddlName, &ddl)
	pos := func(n string) int {
		if i := strings.Index(ddl, "KEY `"+strings.ReplaceAll(n, "`", "``")+"`"); i >= 0 {
			return i
		}
		return 1 << 30
	}
	keep := ""
	if !hasPK {
		best := 1 << 31
		for _, x := range all {
			if x.unique && x.notNul && pos(x.name) < best {
				keep, best = x.name, pos(x.name)
			}
		}
	}
	var out []string
	for _, x := range all {
		if x.name != "PRIMARY" && x.name != keep {
			out = append(out, x.name)
		}
	}
	return out
}

// repairCopy : table Aria ou MyISAM recopiée dans l'instance neuve puis réparée.
func (j *job) repairCopy(ctx context.Context, in *instance, fresh string, d Database, t TableFiles, tr *TableResult) error {
	for _, name := range t.Names {
		dst := filepath.Join(fresh, d.Dir, name)
		if err := copyFile(filepath.Join(j.data, d.Dir, name), dst); err != nil {
			return err
		}
		if j.o.RunAs != "" {
			chownTree(dst, j.o.RunAs)
		}
	}
	_, _ = in.DB.ExecContext(ctx, "FLUSH TABLES")
	full := qi(d.Name) + "." + qi(t.Name)
	method := "réparation " + firstNon(t.Engine, "Aria/MyISAM") + " (REPAIR TABLE)"
	for _, opt := range []string{"", " EXTENDED", " USE_FRM"} {
		if repairOK(ctx, in.DB, "REPAIR TABLE "+full+opt) {
			file := filepath.Join(j.sqlDir, safe(d.Name), safe(t.Name)+".sql")
			rows, err := j.dumpTable(ctx, in, d.Name, t.Name, file, nil)
			if err != nil {
				return err
			}
			j.ok(tr, method+opt, file, rows)
			tr.Warning = "table réparée : des lignes endommagées ont pu être écartées"
			return nil
		}
	}
	return fmt.Errorf("réparation impossible")
}

func repairOK(ctx context.Context, db *sql.DB, q string) bool {
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return false
	}
	defer rows.Close()
	ok := false
	for rows.Next() {
		var tbl, op, typ, msg string
		if rows.Scan(&tbl, &op, &typ, &msg) == nil && strings.EqualFold(typ, "status") && strings.EqualFold(msg, "OK") {
			ok = true
		}
	}
	return ok
}
