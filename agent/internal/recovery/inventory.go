package recovery

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// TableFiles décrit les fichiers d'une table trouvés dans le dossier de sa base.
type TableFiles struct {
	Name        string            `json:"name"`   // nom SQL (décodé)
	File        string            `json:"file"`   // nom de fichier (encodé, sans extension)
	Engine      string            `json:"engine"` // d'après le .frm
	Files       map[string]int64  `json:"files"`  // extension → taille
	Names       map[string]string `json:"-"`      // extension → nom réel du fichier
	Partitioned bool              `json:"partitioned,omitempty"`
	FRMError    string            `json:"frmError,omitempty"`
}

func (t TableFiles) has(ext string) bool { _, ok := t.Files[ext]; return ok }

// path renvoie le chemin réel d'un fichier de la table (casse de l'extension conservée).
func (t TableFiles) path(dir, ext string) string {
	if n, ok := t.Names[ext]; ok {
		return filepath.Join(dir, n)
	}
	return filepath.Join(dir, t.File+ext)
}

// Database regroupe les tables d'une base.
type Database struct {
	Name   string       `json:"name"`
	Dir    string       `json:"dir"`
	Tables []TableFiles `json:"tables"`
	Views  []string     `json:"views,omitempty"`
}

// Inventory : ce que contient le dossier data récupéré.
type Inventory struct {
	Source        string     `json:"source"`
	TotalBytes    int64      `json:"totalBytes"`
	FileCount     int        `json:"fileCount"`
	SystemFiles   []string   `json:"systemFiles"`          // ibdata*, undo*
	RedoFiles     []string   `json:"redoFiles"`            // ib_logfile*
	AriaLogs      []string   `json:"ariaLogs"`             // aria_log*
	ErrorLogs     []string   `json:"errorLogs"`            // *.err
	ConfigFile    string     `json:"configFile,omitempty"` // my.ini / my.cnf trouvé dans data
	Binlogs       []string   `json:"binlogs,omitempty"`    // journaux binaires du serveur
	Version       string     `json:"version,omitempty"`    // version qui a écrit ces fichiers
	VersionSource string     `json:"versionSource,omitempty"`
	HasMysqlDB    bool       `json:"hasMysqlDb"`
	Databases     []Database `json:"databases"`         // bases utilisateur
	Missing       []string   `json:"missing,omitempty"` // fichiers indispensables absents
}

var systemDBs = map[string]bool{"mysql": true, "performance_schema": true, "information_schema": true, "sys": true}

// tableExts : extensions rattachées à une table.
var tableExts = map[string]bool{".frm": true, ".ibd": true, ".cfg": true, ".isl": true, ".myd": true, ".myi": true,
	".mad": true, ".mai": true, ".par": true, ".trg": true, ".csv": true, ".csm": true, ".arz": true}

var (
	binlogRe   = regexp.MustCompile(`^.+\.\d{6}$`)
	versionRe  = regexp.MustCompile(`(\d+\.\d+\.\d+)(-MariaDB)?`)
	creatorRe  = regexp.MustCompile(`MariaDB (\d+\.\d+\.\d+)`)
	partRe     = regexp.MustCompile(`(?i)#P#`)
	filenameRe = regexp.MustCompile(`@([0-9a-fA-F]{4})`)
)

// DecodeFilename convertit un nom de fichier MariaDB (« ma@002dtable ») en nom SQL.
func DecodeFilename(s string) string {
	return filenameRe.ReplaceAllStringFunc(s, func(m string) string {
		v, err := strconv.ParseUint(m[1:], 16, 32)
		if err != nil {
			return m
		}
		return string(rune(v))
	})
}

// Scan inventorie un dossier data sans rien y écrire.
func Scan(source string) (*Inventory, error) {
	inv := &Inventory{Source: source}
	entries, err := os.ReadDir(source)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		name := e.Name()
		lower := strings.ToLower(name)
		p := filepath.Join(source, name)
		if e.IsDir() {
			if strings.HasPrefix(name, "#") || strings.HasPrefix(name, ".") {
				continue
			}
			if lower == "mysql" {
				inv.HasMysqlDB = true
			}
			if systemDBs[lower] {
				continue
			}
			db, err := scanDB(p, name)
			if err == nil && (len(db.Tables) > 0 || fileExists(filepath.Join(p, "db.opt"))) {
				inv.Databases = append(inv.Databases, db)
			}
			continue
		}
		switch {
		case strings.HasPrefix(lower, "ibdata") || strings.HasPrefix(lower, "undo"):
			inv.SystemFiles = append(inv.SystemFiles, name)
		case strings.HasPrefix(lower, "ib_logfile"):
			inv.RedoFiles = append(inv.RedoFiles, name)
		case strings.HasPrefix(lower, "aria_log"):
			inv.AriaLogs = append(inv.AriaLogs, name)
		case strings.HasSuffix(lower, ".err"):
			inv.ErrorLogs = append(inv.ErrorLogs, name)
		case lower == "my.ini" || lower == "my.cnf":
			inv.ConfigFile = name
		case binlogRe.MatchString(name) && !strings.Contains(lower, "relay"):
			inv.Binlogs = append(inv.Binlogs, name)
		case lower == "mysql_upgrade_info":
			if b, err := os.ReadFile(p); err == nil {
				if m := versionRe.FindStringSubmatch(string(b)); m != nil {
					inv.Version, inv.VersionSource = m[1], "mysql_upgrade_info"
				}
			}
		}
	}
	sort.Strings(inv.Binlogs)
	sort.Strings(inv.RedoFiles)
	sort.Strings(inv.SystemFiles)
	// Taille et nombre de fichiers.
	_ = filepath.WalkDir(source, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if i, err := d.Info(); err == nil {
				inv.TotalBytes += i.Size()
				inv.FileCount++
			}
		}
		return nil
	})
	// Le journal InnoDB indique la version qui l'a écrit (« MariaDB 10.6.18 »).
	if len(inv.RedoFiles) > 0 {
		if f, err := os.Open(filepath.Join(source, inv.RedoFiles[0])); err == nil {
			buf := make([]byte, 4096)
			n, _ := f.Read(buf)
			f.Close()
			if m := creatorRe.FindSubmatch(buf[:n]); m != nil {
				if inv.Version == "" || inv.VersionSource != "mysql_upgrade_info" {
					inv.Version, inv.VersionSource = string(m[1]), "ib_logfile0"
				} else if string(m[1]) != inv.Version {
					// Le journal InnoDB fait foi : mysql_upgrade_info peut dater d'avant une mise à jour.
					inv.Version, inv.VersionSource = string(m[1]), "ib_logfile0"
				}
			}
		}
	}
	if !hasPrefix(inv.SystemFiles, "ibdata") {
		inv.Missing = append(inv.Missing, "ibdata1")
	}
	if len(inv.RedoFiles) == 0 {
		inv.Missing = append(inv.Missing, "ib_logfile0")
	}
	if !inv.HasMysqlDB {
		inv.Missing = append(inv.Missing, "mysql (comptes)")
	}
	sort.Slice(inv.Databases, func(i, j int) bool { return inv.Databases[i].Name < inv.Databases[j].Name })
	return inv, nil
}

func scanDB(dir, name string) (Database, error) {
	db := Database{Name: DecodeFilename(name), Dir: name}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return db, err
	}
	tables := map[string]*TableFiles{}
	var order []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if !tableExts[ext] {
			continue
		}
		base := strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
		part := false
		if loc := partRe.FindStringIndex(base); loc != nil {
			base, part = base[:loc[0]], true
		}
		t, ok := tables[base]
		if !ok {
			t = &TableFiles{Name: DecodeFilename(base), File: base, Files: map[string]int64{}, Names: map[string]string{}}
			tables[base] = t
			order = append(order, base)
		}
		if part {
			t.Partitioned = true
		}
		if i, err := e.Info(); err == nil {
			t.Files[ext] += i.Size()
		}
		if !part {
			t.Names[ext] = e.Name()
		}
	}
	sort.Strings(order)
	for _, base := range order {
		t := tables[base]
		if t.has(".frm") {
			b, err := os.ReadFile(t.path(dir, ".frm"))
			switch {
			case err != nil:
				t.FRMError = err.Error()
			case bytes.HasPrefix(b, []byte("TYPE=VIEW")):
				db.Views = append(db.Views, t.Name)
				continue
			default:
				if eng, err := FRMEngine(b); err != nil {
					t.FRMError = err.Error()
				} else {
					t.Engine = eng
				}
			}
		} else if t.has(".ibd") {
			t.Engine = "InnoDB"
		} else if t.has(".mad") {
			t.Engine = "Aria"
		} else if t.has(".myd") {
			t.Engine = "MyISAM"
		}
		if t.has(".par") {
			t.Partitioned = true
		}
		if !t.has(".frm") && !t.has(".ibd") && !t.has(".mad") && !t.has(".myd") {
			continue // .TRG seul (déclencheurs) : rattaché à sa table
		}
		db.Tables = append(db.Tables, *t)
	}
	return db, nil
}

func hasPrefix(list []string, p string) bool {
	for _, s := range list {
		if strings.HasPrefix(strings.ToLower(s), p) {
			return true
		}
	}
	return false
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

// ReadIni lit les options [mysqld]/[mariadb]/[server] d'un my.ini (sans include).
func ReadIni(path string) map[string]string {
	out := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	section := ""
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if l == "" || l[0] == '#' || l[0] == ';' {
			continue
		}
		if l[0] == '[' {
			section = strings.ToLower(strings.Trim(l, "[] "))
			continue
		}
		if section != "mysqld" && section != "mariadb" && section != "server" && !strings.HasPrefix(section, "mariadb-") && !strings.HasPrefix(section, "mysqld-") {
			continue
		}
		k, v, _ := strings.Cut(l, "=")
		k = strings.ReplaceAll(strings.ToLower(strings.TrimSpace(k)), "-", "_")
		out[k] = strings.Trim(strings.TrimSpace(v), `"'`)
	}
	return out
}
