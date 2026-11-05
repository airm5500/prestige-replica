package recovery

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// copyTree copie un dossier ; les fichiers créés prennent les droits du poste actuel
// (ce qui contourne les droits NTFS de l'ancien Windows).
func copyTree(src, dst string, skip func(rel string) bool) error {
	return filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if rel != "." && skip != nil && !d.IsDir() && skip(rel) {
			return nil
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o750)
		}
		return copyFile(p, target)
	})
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// chownTree donne la copie à l'utilisateur du serveur temporaire (Linux, lancement en root).
func chownTree(p, user string) {
	if runtime.GOOS == "windows" || user == "" {
		return
	}
	_ = exec.Command("chown", "-R", user, p).Run()
}

var unsafeRe = regexp.MustCompile(`[^\pL\pN._-]+`)

func safe(s string) string {
	s = unsafeRe.ReplaceAllString(s, "_")
	if s == "" || s == "." || s == ".." {
		return "_"
	}
	return s
}

func short(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 220 {
		return s[:220] + "…"
	}
	return s
}

func rel(base, p string) string {
	r, err := filepath.Rel(base, p)
	if err != nil {
		return p
	}
	return r
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func firstNon(a ...string) string {
	for _, s := range a {
		if s != "" {
			return s
		}
	}
	return ""
}

// errorOnly garde les lignes [ERROR] d'un journal (ou tout le journal s'il n'y en a pas).
func errorOnly(log string) string {
	var out []string
	for _, l := range strings.Split(strings.ReplaceAll(log, "\r\n", "\n"), "\n") {
		if strings.Contains(l, "[ERROR]") && !strings.HasSuffix(strings.TrimSpace(l), "Aborting") {
			out = append(out, strings.TrimSpace(l))
		}
	}
	if len(out) == 0 {
		return log
	}
	return strings.Join(out, "\n")
}

func isBinlogName(n string) bool {
	return binlogRe.MatchString(n) && !strings.Contains(strings.ToLower(n), "relay") && !strings.HasPrefix(strings.ToLower(n), "aria_log")
}
