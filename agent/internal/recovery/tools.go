package recovery

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
)

// Tools : binaires MariaDB utilisés pour la récupération.
type Tools struct {
	Server  string // mariadbd / mysqld
	Dump    string // mariadb-dump / mysqldump
	Binlog  string // mariadb-binlog / mysqlbinlog (facultatif)
	Check   string // innochecksum (facultatif)
	Version string // version du serveur
	Plugins string // dossier des greffons (ha_blackhole), vide = défaut
}

func exe(n string) string {
	if runtime.GOOS == "windows" {
		return n + ".exe"
	}
	return n
}

// binDirs : dossiers où chercher, par ordre de préférence.
func binDirs(explicit, source string) []string {
	var dirs []string
	if explicit != "" {
		dirs = append(dirs, explicit, filepath.Join(explicit, "bin"))
	}
	// Dossier MariaDB copié avec les données (…\MariaDB 10.6\data → …\MariaDB 10.6\bin).
	if source != "" {
		dirs = append(dirs, filepath.Join(filepath.Dir(filepath.Clean(source)), "bin"))
	}
	if p, err := exec.LookPath(exe("mariadbd")); err == nil {
		dirs = append(dirs, filepath.Dir(p))
	}
	if p, err := exec.LookPath(exe("mysqld")); err == nil {
		dirs = append(dirs, filepath.Dir(p))
	}
	if runtime.GOOS == "windows" {
		found, _ := filepath.Glob(filepath.Join(os.Getenv("ProgramFiles"), "MariaDB *", "bin"))
		sort.Sort(sort.Reverse(sort.StringSlice(found)))
		dirs = append(dirs, found...)
	} else {
		dirs = append(dirs, "/usr/sbin", "/usr/bin", "/usr/local/mysql/bin")
	}
	return dirs
}

func findIn(dirs []string, names ...string) string {
	for _, d := range dirs {
		for _, n := range names {
			p := filepath.Join(d, exe(n))
			if st, err := os.Stat(p); err == nil && !st.IsDir() {
				return p
			}
		}
	}
	return ""
}

var serverVersionRe = regexp.MustCompile(`(\d+\.\d+\.\d+)-MariaDB`)

// FindTools localise les binaires : -bin d'abord, puis le dossier MariaDB copié, puis le poste.
func FindTools(ctx context.Context, explicit, source string) (*Tools, error) {
	dirs := binDirs(explicit, source)
	t := &Tools{
		Server: findIn(dirs, "mariadbd", "mysqld"),
		Dump:   findIn(dirs, "mariadb-dump", "mysqldump"),
		Binlog: findIn(dirs, "mariadb-binlog", "mysqlbinlog"),
		Check:  findIn(dirs, "innochecksum"),
	}
	if t.Server == "" || t.Dump == "" {
		return nil, errors.New("serveur MariaDB (mariadbd ou mysqld) ou mariadb-dump introuvable : indiquer le dossier bin avec -bin")
	}
	// Le dump et le serveur doivent venir du même dossier si possible.
	if d := findIn([]string{filepath.Dir(t.Server)}, "mariadb-dump", "mysqldump"); d != "" {
		t.Dump = d
	}
	if d := findIn([]string{filepath.Dir(t.Server)}, "mariadb-binlog", "mysqlbinlog"); d != "" {
		t.Binlog = d
	}
	out, _ := exec.CommandContext(ctx, t.Server, "--version").CombinedOutput()
	if m := serverVersionRe.FindStringSubmatch(string(out)); m != nil {
		t.Version = m[1]
	}
	// Dossier des greffons d'une installation Windows ou copiée : <base>\lib\plugin.
	base := filepath.Dir(filepath.Dir(t.Server))
	for _, p := range []string{filepath.Join(base, "lib", "plugin"), filepath.Join(base, "lib", "mysql", "plugin")} {
		if fileExists(filepath.Join(p, "ha_blackhole"+pluginExt())) {
			t.Plugins = p
			break
		}
	}
	return t, nil
}

func pluginExt() string {
	if runtime.GOOS == "windows" {
		return ".dll"
	}
	return ".so"
}

// MajorMinor : « 10.6.18 » → « 10.6 ».
func MajorMinor(v string) string {
	p := strings.SplitN(v, ".", 3)
	if len(p) < 2 {
		return v
	}
	return p[0] + "." + p[1]
}

// runCmd exécute une commande et renvoie la fin de sa sortie d'erreur.
func runCmd(ctx context.Context, stdout *os.File, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	var errb bytes.Buffer
	cmd.Stderr = &errb
	if stdout != nil {
		cmd.Stdout = stdout
	}
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if len(msg) > 600 {
			msg = "…" + msg[len(msg)-600:]
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%s : %v %s", filepath.Base(name), err, msg)
	}
	return nil
}
