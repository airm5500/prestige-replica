package ops

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/airm5500/prestige-replica-manager/agent/internal/collector"
)

// FindTool localise un binaire MariaDB officiel (mariadb-backup, mbstream, mariadbd).
func (r *Runner) FindTool(name string) (string, error) {
	exe := name
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	var candidates []string
	if r.Cfg.Tools.BinDir != "" {
		candidates = append(candidates, filepath.Join(r.Cfg.Tools.BinDir, exe))
	}
	if p, err := exec.LookPath(exe); err == nil {
		candidates = append(candidates, p)
	}
	if runtime.GOOS == "windows" {
		// Version la plus récente installée en premier.
		dirs, _ := filepath.Glob(filepath.Join(os.Getenv("ProgramFiles"), "MariaDB *", "bin", exe))
		sort.Sort(sort.Reverse(sort.StringSlice(dirs)))
		candidates = append(candidates, dirs...)
	} else {
		candidates = append(candidates, "/usr/bin/"+exe, "/usr/sbin/"+exe, "/usr/local/mysql/bin/"+exe)
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c, nil
		}
	}
	return "", fmt.Errorf("outil %s introuvable (renseigner tools.binDir)", exe)
}

var toolVersionRe = regexp.MustCompile(`(\d+\.\d+\.\d+)-MariaDB`)

// ToolVersion renvoie la version de serveur annoncée par un outil MariaDB.
func ToolVersion(ctx context.Context, path string) (string, error) {
	out, err := exec.CommandContext(ctx, path, "--version").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s --version: %w", filepath.Base(path), err)
	}
	if m := toolVersionRe.FindStringSubmatch(string(out)); m != nil {
		return m[1], nil
	}
	return "", fmt.Errorf("version illisible : %s", strings.TrimSpace(string(out)))
}

// majorMinor renvoie « 10.6 » pour « 10.6.18-MariaDB-log ».
func majorMinor(v string) string {
	parts := strings.SplitN(v, ".", 3)
	if len(parts) < 2 {
		return v
	}
	return parts[0] + "." + strings.TrimLeft(parts[1], "-")
}

// defaultsFile écrit un fichier d'options temporaire (0600) contenant les
// identifiants, pour qu'aucun mot de passe n'apparaisse dans la ligne de
// commande visible des autres processus.
func defaultsFile(user, password, address string) (string, func(), error) {
	f, err := os.CreateTemp("", "prm-*.cnf")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { os.Remove(f.Name()) }
	_ = f.Chmod(0o600)
	var b strings.Builder
	b.WriteString("[client]\n")
	fmt.Fprintf(&b, "user=%s\n", user)
	if password != "" {
		fmt.Fprintf(&b, "password=\"%s\"\n", strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(password))
	}
	if strings.HasPrefix(address, "unix:") {
		fmt.Fprintf(&b, "socket=%s\n", strings.TrimPrefix(address, "unix:"))
	} else if host, port, ok := strings.Cut(address, ":"); ok {
		fmt.Fprintf(&b, "host=%s\nport=%s\nprotocol=TCP\n", host, port)
	}
	if _, err := f.WriteString(b.String()); err != nil {
		f.Close()
		cleanup()
		return "", nil, err
	}
	f.Close()
	return f.Name(), cleanup, nil
}

// tail conserve les dernières lignes d'une sortie, masquées.
type tail struct {
	mu    sync.Mutex
	lines []string
	max   int
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	sc := bufio.NewScanner(bytes.NewReader(p))
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		t.lines = append(t.lines, collector.Redact(sc.Text()))
		if len(t.lines) > t.max {
			t.lines = t.lines[len(t.lines)-t.max:]
		}
	}
	return len(p), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.Join(t.lines, "\n")
}

// run exécute une commande, sortie standard vers stdout (peut être nil).
func run(ctx context.Context, stdout io.Writer, stdin io.Reader, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	t := &tail{max: 40}
	cmd.Stderr = t
	if stdout != nil {
		cmd.Stdout = stdout
	} else {
		cmd.Stdout = t
	}
	cmd.Stdin = stdin
	err := cmd.Run()
	if err != nil {
		if ctx.Err() != nil {
			return t.String(), fmt.Errorf("%s interrompu: %w", filepath.Base(name), ctx.Err())
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return t.String(), fmt.Errorf("%s a échoué (code %d) : %s", filepath.Base(name), ee.ExitCode(), lastLine(t.String()))
		}
		return t.String(), err
	}
	return t.String(), nil
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, "\n"); i >= 0 {
		return s[i+1:]
	}
	return s
}

// versionLess compare numériquement les versions majeure.mineure.
func versionLess(a, b string) bool {
	var a1, a2, b1, b2 int
	fmt.Sscanf(majorMinor(a), "%d.%d", &a1, &a2)
	fmt.Sscanf(majorMinor(b), "%d.%d", &b1, &b2)
	return a1 < b1 || (a1 == b1 && a2 < b2)
}
