package ops

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/airm5500/prestige-replica-manager/agent/internal/config"
)

func TestSafeJoin(t *testing.T) {
	root := t.TempDir()
	ok := []string{"tenants/a/sites/b/backups/c", "x"}
	for _, rel := range ok {
		if _, err := SafeJoin(root, rel); err != nil {
			t.Errorf("%q refusé: %v", rel, err)
		}
	}
	bad := []string{"../etc", "a/../../b", "/etc/passwd", `C:\Windows`, "a//b", "", "a/./b", "a b"}
	for _, rel := range bad {
		if _, err := SafeJoin(root, rel); err == nil {
			t.Errorf("%q accepté", rel)
		}
	}
	// Lien symbolique sortant de la racine.
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "evil")); err == nil {
		if _, err := SafeJoin(root, "evil/x"); err == nil {
			t.Error("lien symbolique sortant accepté")
		}
	}
}

func TestSQLString(t *testing.T) {
	got := sqlString(`a'b\c` + "\n")
	if got != `'a\'b\\c\n'` {
		t.Fatalf("%s", got)
	}
}

func TestValidators(t *testing.T) {
	for _, g := range []string{"0-1-5", "0-1-5,1-2-300"} {
		if !gtidRe.MatchString(g) {
			t.Errorf("GTID %q refusé", g)
		}
	}
	for _, g := range []string{"0-1-5'; DROP", "", "0-1"} {
		if gtidRe.MatchString(g) {
			t.Errorf("GTID %q accepté", g)
		}
	}
	if hostRe.MatchString("db' OR 1") || !hostRe.MatchString("192.168.1.20") || !hostRe.MatchString("srv-prestige.local") {
		t.Error("validation d'hôte incorrecte")
	}
}

func TestUnknownJobRefused(t *testing.T) {
	r := &Runner{Cfg: &config.Config{}}
	_, err := r.Execute(context.Background(), Job{Type: "SHELL", Params: json.RawMessage(`{"cmd":"rm -rf /"}`)}, nopRep{})
	if err == nil || !strings.Contains(err.Error(), "non autorisé") {
		t.Fatalf("err=%v", err)
	}
	_, err = r.Execute(context.Background(), Job{Type: TypeProvisionReplica, Params: json.RawMessage(`{}`)}, nopRep{})
	if err == nil || !strings.Contains(err.Error(), "non autorisée localement") {
		t.Fatalf("la reconstruction doit être refusée sans opt-in local: %v", err)
	}
	r.Cfg.Backup.Enabled = true
	_, err = r.Execute(context.Background(), Job{Type: TypeBackupFull, Params: json.RawMessage(`{"relativeDir":"x","extra":1}`)}, nopRep{})
	if err == nil || !strings.Contains(err.Error(), "paramètres invalides") {
		t.Fatalf("champ inconnu accepté: %v", err)
	}
}

func TestRecommendedConfig(t *testing.T) {
	c := RecommendedConfig("PRIMARY", 0, 11, true)
	for _, want := range []string{"server_id=11", "log_bin=", "binlog_format=ROW", "gtid_strict_mode=ON"} {
		if !strings.Contains(c, want) {
			t.Errorf("%q absent", want)
		}
	}
	if strings.Contains(c, "read_only") {
		t.Error("read_only ne doit pas être proposé au primaire")
	}
	if !strings.Contains(RecommendedConfig("REPLICA", 2, 0, true), "read_only=ON") {
		t.Error("read_only attendu sur la réplique")
	}
}

func TestVersionLess(t *testing.T) {
	if !versionLess("10.6.18-MariaDB", "10.11.2") || versionLess("10.11.2", "10.6.18") || versionLess("10.6.1", "10.6.18") {
		t.Fatal("comparaison de versions incorrecte")
	}
}

type nopRep struct{}

func (nopRep) Step(string) {}
func (nopRep) Warn(string) {}
