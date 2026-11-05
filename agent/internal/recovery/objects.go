package recovery

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const sideDB = "prm_recup_systeme"

// objectsFromFiles : quand l'ancien serveur ne démarre pas, vues et déclencheurs sont relus dans
// leurs fichiers texte (.frm de vue, .TRG), procédures et comptes dans les tables système copiées.
func (j *job) objectsFromFiles(ctx context.Context, in *instance, fresh string) {
	if !in.Alive() {
		return
	}
	procs := j.readSystemTables(ctx, in, fresh)
	for _, d := range j.inv.Databases {
		if j.dbDone[d.Name] {
			continue
		}
		dir := filepath.Join(j.sqlDir, safe(d.Name))
		_ = os.MkdirAll(dir, 0o750)
		head := j.dbHead(d.Name)
		src := filepath.Join(j.data, d.Dir)

		var views strings.Builder
		nv := 0
		for _, v := range d.Views {
			b, err := os.ReadFile(filepath.Join(src, encodeName(v)+".frm"))
			if err != nil {
				continue
			}
			kv := keyValues(b)
			q := unescapeFrm(firstNon(kv["view_body_utf8"], kv["query"]))
			if q == "" {
				continue
			}
			check := ""
			switch kv["with_check_option"] {
			case "1":
				check = " WITH LOCAL CHECK OPTION"
			case "2":
				check = " WITH CASCADED CHECK OPTION"
			}
			fmt.Fprintf(&views, "CREATE OR REPLACE VIEW %s AS %s%s;\n", qi(v), q, check)
			nv++
		}
		if nv > 0 {
			_ = os.WriteFile(filepath.Join(dir, "_vues.sql"), []byte(head+views.String()), 0o640)
			j.r.Extras = append(j.r.Extras, fmt.Sprintf("%s : %d vue(s) relue(s) dans les fichiers .frm → %s", d.Name, nv, rel(j.o.Out, filepath.Join(dir, "_vues.sql"))))
		}

		var prog strings.Builder
		nt := 0
		ents, _ := os.ReadDir(src)
		for _, e := range ents {
			if !strings.EqualFold(filepath.Ext(e.Name()), ".trg") {
				continue
			}
			b, err := os.ReadFile(filepath.Join(src, e.Name()))
			if err != nil {
				continue
			}
			for _, stmt := range quotedList(keyValues(b)["triggers"]) {
				stmt = string(emptyDefiner.ReplaceAll([]byte(stmt), []byte("DEFINER=CURRENT_USER")))
				fmt.Fprintf(&prog, "DELIMITER ;;\n%s;;\nDELIMITER ;\n", stmt)
				nt++
			}
		}
		np := 0
		for _, p := range procs[strings.ToLower(d.Name)] {
			prog.WriteString(p)
			np++
		}
		if nt+np > 0 {
			_ = os.WriteFile(filepath.Join(dir, "_programmes.sql"), []byte(head+prog.String()), 0o640)
			j.r.Extras = append(j.r.Extras, fmt.Sprintf("%s : %d déclencheur(s), %d procédure(s)/fonction(s) relus dans les fichiers → %s", d.Name, nt, np, rel(j.o.Out, filepath.Join(dir, "_programmes.sql"))))
		}
		j.dbDone[d.Name] = true
	}
}

// readSystemTables copie les tables système utiles dans une base à part de l'instance neuve,
// exporte les comptes et renvoie les procédures, par base.
func (j *job) readSystemTables(ctx context.Context, in *instance, fresh string) map[string][]string {
	out := map[string][]string{}
	sys, err := scanDB(filepath.Join(j.data, "mysql"), "mysql")
	if err != nil {
		return out
	}
	want := map[string]bool{"proc": true}
	accounts := []string{"global_priv", "db", "tables_priv", "columns_priv", "procs_priv", "roles_mapping", "proxies_priv"}
	if !j.dbDone["#comptes"] {
		for _, a := range accounts {
			want[a] = true
		}
	}
	if _, err := in.DB.ExecContext(ctx, "CREATE DATABASE IF NOT EXISTS "+sideDB); err != nil {
		return out
	}
	var copied []string
	for _, t := range sys.Tables {
		if !want[strings.ToLower(t.Name)] || t.has(".ibd") {
			continue
		}
		okCopy := true
		for _, name := range t.Names {
			if copyFile(filepath.Join(j.data, "mysql", name), filepath.Join(fresh, sideDB, name)) != nil {
				okCopy = false
			}
		}
		if okCopy {
			copied = append(copied, strings.ToLower(t.Name))
		}
	}
	if j.o.RunAs != "" {
		chownTree(filepath.Join(fresh, sideDB), j.o.RunAs)
	}
	_, _ = in.DB.ExecContext(ctx, "FLUSH TABLES")
	for _, t := range copied {
		_, _ = in.DB.ExecContext(ctx, fmt.Sprintf("REPAIR TABLE %s.%s", sideDB, qi(t)))
	}
	// Comptes.
	if !j.dbDone["#comptes"] {
		var tabs []string
		for _, a := range accounts {
			if contains(copied, a) {
				tabs = append(tabs, a)
			}
		}
		if len(tabs) > 0 {
			file := filepath.Join(j.o.Out, "comptes.sql")
			head := "-- Comptes et droits de l'ancien serveur. À appliquer SEULEMENT sur un serveur neuf de même version :\n-- ces lignes remplacent les comptes existants de même nom.\nUSE mysql;\n"
			if err := j.dumpTo(ctx, file, head, in.dumpArgs(append([]string{"--no-create-info", "--replace", "--skip-triggers", sideDB}, tabs...)...)); err == nil {
				f, _ := os.OpenFile(file, os.O_APPEND|os.O_WRONLY, 0)
				fmt.Fprintln(f, "FLUSH PRIVILEGES;")
				f.Close()
				j.r.Extras = append(j.r.Extras, "comptes et droits relus dans les tables système → comptes.sql (à appliquer à part)")
				j.dbDone["#comptes"] = true
			}
		}
	}
	// Procédures et fonctions.
	if contains(copied, "proc") {
		rows, err := in.DB.QueryContext(ctx, fmt.Sprintf(`SELECT db, name, type, param_list, returns, body, sql_mode, is_deterministic, security_type, comment
			FROM %s.proc WHERE type IN ('PROCEDURE','FUNCTION')`, sideDB))
		if err == nil {
			for rows.Next() {
				var db, name, typ, params, returns, body, mode, det, sec, comment string
				if rows.Scan(&db, &name, &typ, &params, &returns, &body, &mode, &det, &sec, &comment) != nil {
					continue
				}
				var s strings.Builder
				fmt.Fprintf(&s, "SET sql_mode='%s';\nDROP %s IF EXISTS %s;\nDELIMITER ;;\nCREATE %s %s(%s)", strings.ReplaceAll(mode, "'", "''"), typ, qi(name), typ, qi(name), params)
				if typ == "FUNCTION" {
					s.WriteString(" RETURNS " + returns)
				}
				if det == "YES" {
					s.WriteString(" DETERMINISTIC")
				}
				if sec == "INVOKER" {
					s.WriteString(" SQL SECURITY INVOKER")
				}
				if comment != "" {
					s.WriteString(" COMMENT '" + strings.ReplaceAll(comment, "'", "''") + "'")
				}
				fmt.Fprintf(&s, "\n%s;;\nDELIMITER ;\n", body)
				out[strings.ToLower(db)] = append(out[strings.ToLower(db)], s.String())
			}
			rows.Close()
		}
	}
	_, _ = in.DB.ExecContext(ctx, "DROP DATABASE IF EXISTS "+sideDB)
	return out
}

// keyValues lit un fichier texte « clé=valeur » (.frm de vue, .TRG).
func keyValues(b []byte) map[string]string {
	kv := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		if k, v, ok := strings.Cut(sc.Text(), "="); ok {
			kv[k] = v
		}
	}
	return kv
}

// unescapeFrm : séquences d'échappement des fichiers texte de MariaDB.
func unescapeFrm(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
			switch s[i] {
			case 'n':
				b.WriteByte('\n')
			case 'r':
				b.WriteByte('\r')
			case 't':
				b.WriteByte('\t')
			case '0':
				b.WriteByte(0)
			case 'Z':
				b.WriteByte(26)
			default:
				b.WriteByte(s[i])
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// quotedList : 'a' 'b' → [a b], échappements décodés.
func quotedList(s string) []string {
	var out []string
	for i := 0; i < len(s); i++ {
		if s[i] != '\'' {
			continue
		}
		var cur strings.Builder
		for i++; i < len(s); i++ {
			if s[i] == '\\' && i+1 < len(s) {
				cur.WriteByte(s[i])
				cur.WriteByte(s[i+1])
				i++
				continue
			}
			if s[i] == '\'' {
				break
			}
			cur.WriteByte(s[i])
		}
		out = append(out, unescapeFrm(cur.String()))
	}
	return out
}

// encodeName : nom SQL → nom de fichier MariaDB (@XXXX pour les caractères spéciaux).
func encodeName(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
		} else if r < 0x10000 {
			fmt.Fprintf(&b, "@%04x", r)
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}
