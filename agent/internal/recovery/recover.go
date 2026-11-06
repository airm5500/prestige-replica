package recovery

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Options d'une récupération à partir des fichiers bruts d'un serveur MariaDB.
type Options struct {
	Source       string   // dossier data récupéré (jamais modifié)
	Work         string   // dossier de travail (copie des fichiers, instances temporaires)
	Out          string   // dossier de résultat (exports SQL, rapport)
	BinDir       string   // binaires MariaDB de même version que la source (recommandé)
	MyIni        string   // ancien my.ini (sinon cherché à côté de la source)
	MaxForce     int      // niveau maximal d'innodb_force_recovery (0 à 6)
	Databases    []string // bases à récupérer (vide = toutes)
	DiagnoseOnly bool     // inventaire et diagnostic seulement
	BinlogsSince string   // exporter en SQL les binlogs trouvés à partir de cette date (AAAA-MM-JJ HH:MM:SS)
	RunAs        string   // utilisateur système du serveur temporaire (Linux, si lancé en root)
	BufferMB     int
	Log          func(string)
}

// TableResult : sort d'une table.
type TableResult struct {
	Database string `json:"database"`
	Table    string `json:"table"`
	Engine   string `json:"engine"`
	Status   string `json:"status"` // RECUPEREE, ECHEC, IGNOREE
	Method   string `json:"method,omitempty"`
	File     string `json:"file,omitempty"`
	Bytes    int64  `json:"bytes,omitempty"`
	Rows     int64  `json:"rows"`
	Warning  string `json:"warning,omitempty"`
	Partial  bool   `json:"partial,omitempty"`
	Error    string `json:"error,omitempty"`
}

// Attempt : un démarrage de l'instance de récupération.
type Attempt struct {
	Method  string    `json:"method"`
	Started bool      `json:"started"`
	Error   string    `json:"error,omitempty"`
	Log     string    `json:"log,omitempty"`
	Finding []Finding `json:"findings,omitempty"`
}

// Report : résultat complet, écrit dans rapport.json et RAPPORT.txt.
type Report struct {
	StartedAt      time.Time     `json:"startedAt"`
	FinishedAt     time.Time     `json:"finishedAt"`
	Inventory      *Inventory    `json:"inventory"`
	ToolsVersion   string        `json:"toolsVersion"`
	ServerBinary   string        `json:"serverBinary"`
	VersionWarning string        `json:"versionWarning,omitempty"`
	CleanShutdown  bool          `json:"cleanShutdown"`
	Findings       []Finding     `json:"findings"`
	OldLogTail     string        `json:"oldLogTail,omitempty"`
	Repairs        []string      `json:"repairs,omitempty"`
	Attempts       []Attempt     `json:"attempts"`
	Tables         []TableResult `json:"tables"`
	Extras         []string      `json:"extras,omitempty"`  // vues, programmes, comptes exportés
	Damaged        []string      `json:"damaged,omitempty"` // fichiers .ibd aux pages invalides (innochecksum)
	Binlogs        []BinlogFile  `json:"binlogs,omitempty"`
	BinlogSQL      string        `json:"binlogSql,omitempty"`
	FullRestore    string        `json:"fullRestore,omitempty"`
	Recovered      int           `json:"recovered"`
	Failed         int           `json:"failed"`
	Verdict        string        `json:"verdict"`
	Next           []string      `json:"next"`
}

type job struct {
	o      Options
	t      *Tools
	r      *Report
	inv    *Inventory
	ini    map[string]string
	data   string // copie de travail du dossier data
	runDir string
	sqlDir string
	tables map[string]*TableResult // « base.table »
	order  []string
	dbDone map[string]bool // vues et programmes exportés
}

func (j *job) log(f string, a ...any) {
	if j.o.Log != nil {
		j.o.Log(fmt.Sprintf(f, a...))
	}
}

// Run enchaîne toutes les méthodes, de la plus fidèle à la plus dégradée, et écrit le rapport.
func Run(ctx context.Context, o Options) (*Report, error) {
	if o.Source == "" || o.Out == "" || (o.Work == "" && !o.DiagnoseOnly) {
		return nil, fmt.Errorf("indiquer -source, -work et -out")
	}
	if o.MaxForce < 0 || o.MaxForce > 6 {
		return nil, fmt.Errorf("-max-force doit être entre 0 et 6")
	}
	var err error
	for _, p := range []*string{&o.Source, &o.Work, &o.Out, &o.BinDir, &o.MyIni} {
		if *p != "" {
			if *p, err = filepath.Abs(*p); err != nil {
				return nil, err
			}
		}
	}
	src := o.Source
	for _, p := range []string{o.Work, o.Out} {
		if p == "" {
			continue
		}
		a, _ := filepath.Abs(p)
		if a == src || strings.HasPrefix(a, src+string(filepath.Separator)) {
			return nil, fmt.Errorf("%s est dans le dossier source : choisir un autre emplacement (la source n'est jamais modifiée)", p)
		}
	}
	r := &Report{StartedAt: time.Now()}
	j := &job{o: o, r: r, tables: map[string]*TableResult{}, dbDone: map[string]bool{}}
	if err := os.MkdirAll(o.Out, 0o750); err != nil {
		return nil, err
	}

	// 1. Inventaire (lecture seule).
	j.log("Inventaire de %s", o.Source)
	inv, err := Scan(o.Source)
	if err != nil {
		return nil, fmt.Errorf("lecture de la source impossible : %w (droits ? lancer en administrateur)", err)
	}
	j.inv, r.Inventory = inv, inv
	if len(o.Databases) > 0 {
		keep := map[string]bool{}
		for _, d := range o.Databases {
			keep[strings.ToLower(d)] = true
		}
		var dbs []Database
		for _, d := range inv.Databases {
			if keep[strings.ToLower(d.Name)] {
				dbs = append(dbs, d)
			}
		}
		inv.Databases = dbs
	}
	for _, d := range inv.Databases {
		for _, t := range d.Tables {
			k := d.Name + "." + t.Name
			j.tables[k] = &TableResult{Database: d.Name, Table: t.Name, Engine: t.Engine, Status: "ECHEC"}
			j.order = append(j.order, k)
		}
	}
	// Ancien my.ini : réglages InnoDB à reprendre.
	ini := o.MyIni
	if ini == "" && inv.ConfigFile != "" {
		ini = filepath.Join(o.Source, inv.ConfigFile)
	}
	if ini == "" {
		if p := filepath.Join(filepath.Dir(filepath.Clean(o.Source)), "my.ini"); fileExists(p) {
			ini = p
		}
	}
	j.ini = map[string]string{}
	if ini != "" {
		j.ini = ReadIni(ini)
	}

	// 2. Diagnostic de l'ancien journal d'erreurs.
	if len(inv.ErrorLogs) > 0 {
		newest, newestT := "", time.Time{}
		for _, e := range inv.ErrorLogs {
			if st, err := os.Stat(filepath.Join(o.Source, e)); err == nil && st.ModTime().After(newestT) {
				newest, newestT = e, st.ModTime()
			}
		}
		f, clean, tailTxt, err := DiagnoseFile(filepath.Join(o.Source, newest))
		if err == nil {
			r.Findings, r.CleanShutdown, r.OldLogTail = f, clean, tailTxt
		}
	}

	// 3. Binaires et version.
	j.t, err = FindTools(ctx, o.BinDir, o.Source)
	if err != nil {
		return j.finish(err)
	}
	r.ToolsVersion, r.ServerBinary = j.t.Version, j.t.Server
	if inv.Version != "" && j.t.Version != "" && MajorMinor(inv.Version) != MajorMinor(j.t.Version) {
		r.VersionWarning = fmt.Sprintf("Les fichiers ont été écrits par MariaDB %s, les binaires utilisés sont en %s. "+
			"Après un arrêt brutal, seule la même version (%s) sait appliquer le journal InnoDB : indiquer son dossier bin avec -bin.",
			inv.Version, j.t.Version, MajorMinor(inv.Version))
	}
	j.log("Fichiers écrits par MariaDB %s ; binaires utilisés : %s (%s)", firstNon(inv.Version, "?"), j.t.Version, j.t.Server)

	// 4. Binlogs présents sur le disque : copiés et résumés, quoi qu'il arrive ensuite.
	j.binlogs(ctx)
	if o.DiagnoseOnly {
		j.checksums(ctx)
		return j.finish(nil)
	}

	// 5. Copie de travail : la source n'est jamais ouverte par un serveur.
	j.data = filepath.Join(o.Work, "data")
	j.runDir = filepath.Join(o.Work, "run")
	j.sqlDir = filepath.Join(o.Out, "sql")
	for _, d := range []string{j.runDir, j.sqlDir} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			return j.finish(err)
		}
	}
	if fileExists(j.data) {
		return j.finish(fmt.Errorf("%s existe déjà : choisir un dossier de travail vide", j.data))
	}
	j.log("Copie de travail vers %s (%d Mo)", j.data, inv.TotalBytes/1e6)
	if err := copyTree(o.Source, j.data, func(rel string) bool {
		base := strings.ToLower(filepath.Base(rel))
		return strings.HasSuffix(base, ".err") || strings.HasSuffix(base, ".pid") || base == "my.ini" || base == "my.cnf" ||
			isBinlogName(filepath.Base(rel))
	}); err != nil {
		return j.finish(fmt.Errorf("copie de travail impossible : %w", err))
	}
	if o.RunAs != "" {
		chownTree(j.data, o.RunAs)
		chownTree(j.runDir, o.RunAs)
	}

	// 6. Démarrage sur la copie, innodb_force_recovery croissant ; export table par table.
	if len(inv.Missing) == 0 || !contains(inv.Missing, "ibdata1") {
		j.ladder(ctx)
	} else {
		r.Attempts = append(r.Attempts, Attempt{Method: "démarrage normal", Error: "ibdata1 absent : le serveur ne peut pas démarrer sur ces fichiers"})
	}

	// 7. Tables restantes : structure tirée du .frm, données importées depuis le .ibd.
	if j.pending() > 0 {
		j.importTablespaces(ctx)
	}
	return j.finish(nil)
}

// checksums vérifie les pages de chaque fichier .ibd de la source (lecture seule).
func (j *job) checksums(ctx context.Context) {
	if j.t.Check == "" {
		return
	}
	for _, d := range j.inv.Databases {
		for _, t := range d.Tables {
			if !t.has(".ibd") || t.Partitioned || ctx.Err() != nil {
				continue
			}
			if err := runCmd(ctx, nil, j.t.Check, t.path(filepath.Join(j.o.Source, d.Dir), ".ibd")); err != nil {
				j.r.Damaged = append(j.r.Damaged, d.Name+"."+t.Name)
			}
		}
	}
	j.log("Contrôle des pages : %d table(s) avec des pages invalides", len(j.r.Damaged))
}

func (j *job) pending() int {
	n := 0
	for _, k := range j.order {
		if j.tables[k].Status != "RECUPEREE" {
			n++
		}
	}
	return n
}

// ladder : niveaux 0 à MaxForce. Une table exportée à un niveau n'est plus retentée.
func (j *job) ladder(ctx context.Context) {
	for level := 0; level <= j.o.MaxForce && j.pending() > 0; level++ {
		if ctx.Err() != nil {
			return
		}
		method := "démarrage normal"
		if level > 0 {
			method = fmt.Sprintf("innodb_force_recovery=%d", level)
		}
		j.log("Démarrage de l'instance de récupération (%s)", method)
		restarts := 0
		for {
			in, err := startInstance(ctx, j.t, startSpec{datadir: j.data, runDir: j.runDir, name: fmt.Sprintf("force%d", level),
				force: level, ini: j.ini, runAs: j.o.RunAs, bufferMB: j.o.BufferMB})
			a := Attempt{Method: method, Started: err == nil}
			if err != nil {
				b, _ := os.ReadFile(filepath.Join(j.runDir, fmt.Sprintf("force%d.err", level)))
				a.Error = err.Error()
				a.Finding, _ = Diagnose(string(b))
				a.Log = lastLines(errorOnly(string(b)), 6)
				j.r.Attempts = append(j.r.Attempts, a)
				j.log("  échec : %s", firstNon(a.Log, a.Error))
				// Journaux Aria illisibles : mis de côté (dans la copie) puis nouvel essai au même niveau.
				if hasCode(a.Finding, "ARIA") && j.setAsideAriaLogs() {
					continue
				}
				break
			}
			j.r.Attempts = append(j.r.Attempts, a)
			crashed := j.dumpAll(ctx, in, method, level)
			in.Stop()
			if crashed && restarts < 3 && j.pending() > 0 && ctx.Err() == nil {
				// Une page illisible a arrêté le serveur : reprise au même niveau avec les tables suivantes.
				restarts++
				j.log("  le serveur s'est arrêté pendant l'export ; redémarrage au même niveau")
				continue
			}
			break
		}
	}
}

func hasCode(f []Finding, code string) bool {
	for _, x := range f {
		if x.Code == code {
			return true
		}
	}
	return false
}

// setAsideAriaLogs déplace aria_log* de la copie de travail vers work/aria-de-cote (une seule fois).
func (j *job) setAsideAriaLogs() bool {
	aside := filepath.Join(j.o.Work, "aria-de-cote")
	if fileExists(aside) {
		return false
	}
	_ = os.MkdirAll(aside, 0o750)
	ents, _ := os.ReadDir(j.data)
	moved := 0
	for _, e := range ents {
		if strings.HasPrefix(strings.ToLower(e.Name()), "aria_log") {
			if os.Rename(filepath.Join(j.data, e.Name()), filepath.Join(aside, e.Name())) == nil {
				moved++
			}
		}
	}
	if moved > 0 {
		j.r.Repairs = append(j.r.Repairs, "journaux Aria illisibles mis de côté dans la copie de travail (tables système réparées au démarrage)")
	}
	return moved > 0
}

// dumpAll exporte les tables encore à récupérer ; renvoie true si le serveur s'est arrêté en cours.
func (j *job) dumpAll(ctx context.Context, in *instance, method string, level int) bool {
	if !j.dbDone["#comptes"] {
		j.dumpAccounts(ctx, in)
	}
	for _, d := range j.inv.Databases {
		for _, t := range d.Tables {
			tr := j.tables[d.Name+"."+t.Name]
			if tr.Status == "RECUPEREE" || tr.Error == "!crash"+method {
				continue
			}
			if ctx.Err() != nil {
				return false
			}
			file := filepath.Join(j.sqlDir, safe(d.Name), safe(t.Name)+".sql")
			rows, err := j.dumpTable(ctx, in, d.Name, t.Name, file, nil)
			if err != nil {
				tr.Error = err.Error()
				j.log("  %s.%s : %s", d.Name, t.Name, short(err.Error()))
				if !in.Alive() {
					tr.Error = "!crash" + method
					return true
				}
				// Lecture interrompue par une page illisible : sauvetage ligne à ligne (instance en écriture).
				if level <= 3 && strings.Contains(err.Error(), "dumping table") {
					j.log("  %s.%s : sauvetage ligne à ligne", d.Name, t.Name)
					if serr := j.salvage(ctx, in, d, t, tr, method); serr != nil {
						tr.Error += " ; sauvetage : " + serr.Error()
						if !in.Alive() {
							return true
						}
					}
				}
				continue
			}
			j.ok(tr, method, file, rows)
			if level >= 4 {
				tr.Warning = "exportée avec innodb_force_recovery≥4 : les dernières transactions et des lignes de pages endommagées peuvent manquer"
			}
		}
		if !j.dbDone[d.Name] {
			j.dumpPrograms(ctx, in, d)
		}
	}
	return false
}

// dbHead : création de la base avec son jeu de caractères d'origine (db.opt), puis USE.
func (j *job) dbHead(name string) string {
	opts := ""
	for _, d := range j.inv.Databases {
		if d.Name == name {
			kv := keyValues(readSmall(filepath.Join(j.o.Source, d.Dir, "db.opt")))
			if cs := kv["default-character-set"]; cs != "" {
				opts += " CHARACTER SET " + cs
			}
			if co := kv["default-collation"]; co != "" {
				opts += " COLLATE " + co
			}
		}
	}
	return fmt.Sprintf("CREATE DATABASE IF NOT EXISTS %s%s;\nUSE %s;\n", qi(name), opts, qi(name))
}

func readSmall(p string) []byte {
	b, _ := os.ReadFile(p)
	return b
}

func (j *job) ok(tr *TableResult, method, file string, rows int64) {
	tr.Status, tr.Method, tr.Error, tr.Rows = "RECUPEREE", method, "", rows
	tr.File, _ = filepath.Rel(j.o.Out, file)
	if st, err := os.Stat(file); err == nil {
		tr.Bytes = st.Size()
	}
	j.log("  %s.%s : %d lignes (%s)", tr.Database, tr.Table, rows, method)
}

// dumpTable écrit un fichier SQL autonome pour une table ; structure facultative fournie (import .ibd).
func (j *job) dumpTable(ctx context.Context, in *instance, db, table, file string, ddl *string) (int64, error) {
	if err := os.MkdirAll(filepath.Dir(file), 0o750); err != nil {
		return 0, err
	}
	tmp := file + ".partiel"
	f, err := os.Create(tmp)
	if err != nil {
		return 0, err
	}
	fmt.Fprintf(f, "-- Récupération Prestige Replica Manager : %s.%s\n%sSET FOREIGN_KEY_CHECKS=0;\n", db, table, j.dbHead(db))
	extra := []string{"--skip-triggers", "--no-create-db"}
	if ddl != nil {
		fmt.Fprintf(f, "DROP TABLE IF EXISTS %s;\n%s;\n", qi(table), *ddl)
		extra = append(extra, "--no-create-info")
	} else {
		extra = append(extra, "--add-drop-table")
	}
	f.Sync()
	err = runCmd(ctx, f, j.t.Dump, in.dumpArgs(append(extra, db, table)...)...)
	fmt.Fprintln(f, "SET FOREIGN_KEY_CHECKS=1;")
	f.Close()
	if err != nil {
		os.Remove(tmp)
		return 0, err
	}
	var n int64
	cctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	_ = in.DB.QueryRowContext(cctx, fmt.Sprintf("SELECT COUNT(*) FROM %s.%s", qi(db), qi(table))).Scan(&n)
	return n, os.Rename(tmp, file)
}

// dumpPrograms : vues, procédures, fonctions, déclencheurs et événements d'une base.
func (j *job) dumpPrograms(ctx context.Context, in *instance, d Database) {
	dir := filepath.Join(j.sqlDir, safe(d.Name))
	_ = os.MkdirAll(dir, 0o750)
	head := j.dbHead(d.Name)
	if len(d.Views) > 0 {
		file := filepath.Join(dir, "_vues.sql")
		if err := j.dumpTo(ctx, file, head, in.dumpArgs(append([]string{"--no-data", "--skip-triggers", "--no-create-db", "--add-drop-table", d.Name}, d.Views...)...)); err == nil {
			j.r.Extras = append(j.r.Extras, fmt.Sprintf("%s : %d vue(s) → %s", d.Name, len(d.Views), rel(j.o.Out, file)))
		} else {
			j.r.Extras = append(j.r.Extras, fmt.Sprintf("%s : vues non exportées (%s)", d.Name, short(err.Error())))
		}
	}
	file := filepath.Join(dir, "_programmes.sql")
	if err := j.dumpTo(ctx, file, head, in.dumpArgs("--no-data", "--no-create-info", "--no-create-db", "--routines", "--events", "--triggers", d.Name)); err == nil {
		j.r.Extras = append(j.r.Extras, fmt.Sprintf("%s : procédures, déclencheurs et événements → %s", d.Name, rel(j.o.Out, file)))
	} else {
		j.r.Extras = append(j.r.Extras, fmt.Sprintf("%s : procédures et déclencheurs non exportés (%s)", d.Name, short(err.Error())))
	}
	j.dbDone[d.Name] = true
}

// dumpAccounts : tables des comptes, à réappliquer à part (elles remplacent les comptes existants).
func (j *job) dumpAccounts(ctx context.Context, in *instance) {
	j.dbDone["#comptes"] = true
	var tabs []string
	for _, t := range []string{"global_priv", "user", "db", "tables_priv", "columns_priv", "procs_priv", "roles_mapping", "proxies_priv"} {
		var n string
		if in.DB.QueryRowContext(ctx, "SELECT TABLE_NAME FROM information_schema.TABLES WHERE TABLE_SCHEMA='mysql' AND TABLE_NAME=? AND TABLE_TYPE='BASE TABLE'", t).Scan(&n) == nil {
			tabs = append(tabs, t)
		}
	}
	if len(tabs) == 0 {
		j.r.Extras = append(j.r.Extras, "comptes : tables mysql absentes, non exportés")
		return
	}
	file := filepath.Join(j.o.Out, "comptes.sql")
	head := "-- Comptes et droits de l'ancien serveur. À appliquer SEULEMENT sur un serveur neuf de même version :\n-- ces lignes remplacent les comptes existants de même nom.\nUSE mysql;\n"
	err := j.dumpTo(ctx, file, head, in.dumpArgs(append([]string{"--no-create-info", "--replace", "--skip-triggers", "mysql"}, tabs...)...))
	if err == nil {
		f, _ := os.OpenFile(file, os.O_APPEND|os.O_WRONLY, 0)
		fmt.Fprintln(f, "FLUSH PRIVILEGES;")
		f.Close()
		j.r.Extras = append(j.r.Extras, "comptes et droits → comptes.sql (à appliquer à part)")
	} else {
		j.r.Extras = append(j.r.Extras, "comptes non exportés : "+short(err.Error()))
	}
}

func (j *job) dumpTo(ctx context.Context, file, head string, args []string) error {
	f, err := os.Create(file)
	if err != nil {
		return err
	}
	fmt.Fprint(f, head)
	f.Sync()
	err = runCmd(ctx, f, j.t.Dump, args...)
	f.Close()
	if err != nil {
		os.Remove(file)
		return err
	}
	// Un objet créé sans compte (serveur lancé sans contrôle des comptes) a un propriétaire vide,
	// refusé à la restauration : il est attribué au compte qui restaure.
	if b, err := os.ReadFile(file); err == nil && emptyDefiner.Match(b) {
		_ = os.WriteFile(file, emptyDefiner.ReplaceAll(b, []byte("DEFINER=CURRENT_USER")), 0o640)
	}
	return nil
}

var emptyDefiner = regexp.MustCompile("DEFINER=``(@``)?")

// finish écrit le script de restauration complète et le rapport.
func (j *job) finish(fatal error) (*Report, error) {
	r := j.r
	r.Tables = nil
	for _, k := range j.order {
		t := j.tables[k]
		if strings.HasPrefix(t.Error, "!crash") {
			t.Error = "arrêt du serveur en lisant cette table (pages endommagées) avec " + strings.TrimPrefix(t.Error, "!crash")
		}
		if t.Status == "RECUPEREE" {
			r.Recovered++
		} else if !j.o.DiagnoseOnly {
			r.Failed++
		}
		r.Tables = append(r.Tables, *t)
	}
	if !j.o.DiagnoseOnly && r.Recovered > 0 {
		if p, err := j.writeFullRestore(); err == nil {
			r.FullRestore = p
		}
	}
	j.verdict(fatal)
	r.FinishedAt = time.Now()
	b, _ := json.MarshalIndent(r, "", "  ")
	_ = os.WriteFile(filepath.Join(j.o.Out, "rapport.json"), b, 0o640)
	_ = os.WriteFile(filepath.Join(j.o.Out, "RAPPORT.txt"), []byte(TextReport(r, j.o.DiagnoseOnly)), 0o640)
	if j.runDir != "" {
		logs := filepath.Join(j.o.Out, "journaux")
		_ = os.MkdirAll(logs, 0o750)
		ents, _ := os.ReadDir(j.runDir)
		for _, e := range ents {
			if strings.HasSuffix(e.Name(), ".err") {
				_ = copyFile(filepath.Join(j.runDir, e.Name()), filepath.Join(logs, e.Name()))
			}
		}
	}
	return r, fatal
}

// writeFullRestore concatène : tables, puis vues, puis programmes.
func (j *job) writeFullRestore() (string, error) {
	out := filepath.Join(j.o.Out, "restauration-complete.sql")
	f, err := os.Create(out)
	if err != nil {
		return "", err
	}
	defer f.Close()
	fmt.Fprintf(f, "-- Restauration complète des données récupérées (%s)\n-- mariadb -u root -p < restauration-complete.sql\nSET NAMES utf8mb4;\nSET FOREIGN_KEY_CHECKS=0;\n", time.Now().Format("2006-01-02 15:04"))
	appendFile := func(p string) {
		in, err := os.Open(p)
		if err != nil {
			return
		}
		defer in.Close()
		_, _ = io.Copy(f, in)
		fmt.Fprintln(f)
	}
	for _, k := range j.order {
		if t := j.tables[k]; t.Status == "RECUPEREE" {
			appendFile(filepath.Join(j.o.Out, t.File))
		}
	}
	dbs := make([]string, 0)
	for _, d := range j.inv.Databases {
		dbs = append(dbs, safe(d.Name))
	}
	sort.Strings(dbs)
	for _, name := range []string{"_vues.sql", "_programmes.sql"} {
		for _, d := range dbs {
			appendFile(filepath.Join(j.sqlDir, d, name))
		}
	}
	fmt.Fprintln(f, "SET FOREIGN_KEY_CHECKS=1;")
	return "restauration-complete.sql", nil
}
