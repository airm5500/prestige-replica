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

func TestControlRequiresLocalOptIn(t *testing.T) {
	r := &Runner{Cfg: &config.Config{}}
	for _, typ := range []string{TypeReplicationPause, TypeReplicationResume, TypeSwitchoverDemote, TypeSwitchoverPromote, TypeSwitchoverUndo, TypeReplicaAttach, TypeFence} {
		_, err := r.Execute(context.Background(), Job{Type: typ, Params: []byte(`{}`)}, nopRep{})
		if err == nil || !strings.Contains(err.Error(), "replicationControl.enabled") {
			t.Errorf("%s accepté sans replicationControl.enabled : %v", typ, err)
		}
	}
	r.Cfg.ReplicationControl.Enabled = true
	// Paramètres inattendus refusés avant toute connexion.
	if _, err := r.Execute(context.Background(), Job{Type: TypeSwitchoverPromote, Params: []byte(`{"gtid":"0-1-1","sql":"DROP"}`)}, nopRep{}); err == nil {
		t.Error("champ inconnu accepté")
	}
	if _, err := r.Execute(context.Background(), Job{Type: TypeReplicationPause, Params: []byte(`{"mode":"IO; DROP"}`)}, nopRep{}); err == nil {
		t.Error("mode de pause invalide accepté")
	}
	if _, err := r.Execute(context.Background(), Job{Type: TypeReplicaAttach, Params: []byte(`{"sourceHost":"a'b","sourcePort":3306}`)}, nopRep{}); err == nil {
		t.Error("hôte invalide accepté")
	}
}

func TestFenceMarkerLifecycle(t *testing.T) {
	r := &Runner{Cfg: &config.Config{StateDir: t.TempDir()}}
	if r.Fenced() {
		t.Fatal("isolé sans marqueur")
	}
	r.markFenced(nopRep{}, "test")
	if !r.Fenced() {
		t.Fatal("marqueur absent après isolement")
	}
	if err := r.Unfence(); err != nil || r.Fenced() {
		t.Fatalf("levée d'isolement : %v", err)
	}
	if err := r.Unfence(); err != nil {
		t.Fatal("levée d'un isolement inexistant doit être sans effet")
	}
}

type nopRep struct{}

func (nopRep) Step(string) {}
func (nopRep) Warn(string) {}

func TestBinlogOperationsRequireOptInAndValidate(t *testing.T) {
	r := &Runner{Cfg: &config.Config{}}
	if _, err := r.Execute(context.Background(), Job{Type: TypeBinlogArchive, Params: []byte(`{}`)}, nopRep{}); err == nil || !strings.Contains(err.Error(), "binlogArchive.enabled") {
		t.Errorf("archivage accepté sans opt-in : %v", err)
	}
	if _, err := r.Execute(context.Background(), Job{Type: TypePITR, Params: []byte(`{}`)}, nopRep{}); err == nil || !strings.Contains(err.Error(), "restoreTest.enabled") {
		t.Errorf("restauration à un instant acceptée sans opt-in : %v", err)
	}
	r.Cfg.BinlogArchive.Enabled, r.Cfg.RestoreTest.Enabled = true, true
	bad := []string{
		`{"sourceHost":"h;rm","sourcePort":3306,"serverId":1,"relativeDir":"a/b"}`,
		`{"sourceHost":"h","sourcePort":3306,"serverId":1,"relativeDir":"../x"}`,
		`{"sourceHost":"h","sourcePort":3306,"serverId":1,"relativeDir":"a","archived":["../../etc/passwd"]}`,
	}
	for _, b := range bad {
		if _, err := r.Execute(context.Background(), Job{Type: TypeBinlogArchive, Params: []byte(b)}, nopRep{}); err == nil {
			t.Errorf("paramètres d'archivage invalides acceptés : %s", b)
		}
	}
	pbad := []string{
		`{"stopAt":"2026-10-31T10:00:00Z","binlogs":[{"relativePath":"../a/binlog.000001.prmseal"}]}`,
		`{"stopAt":"2026-10-31T10:00:00Z","extract":["db.t; DROP"],"extractDir":"x"}`,
		`{"stopAt":"2026-10-31T10:00:00Z","startGtid":"1-2"}`,
		`{"binlogs":[]}`,
	}
	for _, b := range pbad {
		if _, err := r.Execute(context.Background(), Job{Type: TypePITR, Params: []byte(b)}, nopRep{}); err == nil {
			t.Errorf("paramètres de restauration invalides acceptés : %s", b)
		}
	}
	if !archivePathRe.MatchString("tenants/a-1/sites/b/binlogs/2/binlog.000012.prmseal") {
		t.Error("chemin d'archive valide refusé")
	}
}

func TestReplayErrorsOnlyIgnoresAccountStatements(t *testing.T) {
	e := &replayErrors{}
	_, _ = e.Write([]byte("--------------\nGRANT BINLOG MONITOR ON *.* TO `prm_repl`@`%`\n--------------\n\n"))
	_, _ = e.Write([]byte("ERROR 1290 (HY000) at line 32: The MariaDB server is running with the --skip-grant-tables option\nERROR 1290 (HY000) at line 40: idem\n"))
	if e.skipped != 2 || e.other != "" {
		t.Fatalf("attendu 2 ignorées, aucune autre : %+v", e)
	}
	_, _ = e.Write([]byte("ERROR 1062 (23000) at line 51: Duplicate entry"))
	_, _ = e.Write([]byte(" '5' for key 'PRIMARY'\n"))
	if e.other == "" || !strings.Contains(e.other, "1062") {
		t.Fatalf("une erreur de données doit être retenue : %+v", e)
	}
}
