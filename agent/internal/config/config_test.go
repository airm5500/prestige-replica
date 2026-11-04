package config

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "agent.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDefaults(t *testing.T) {
	c, err := Load(write(t, `{}`))
	if err != nil {
		t.Fatal(err)
	}
	if c.IntervalSeconds != 30 || c.MariaDB.Address != "127.0.0.1:3306" || c.BufferSize != 2880 {
		t.Fatalf("défauts inattendus: %+v", c)
	}
}

func TestRejectsPlainHTTP(t *testing.T) {
	_, err := Load(write(t, `{"controlPlaneUrl":"http://console.exemple","agentTokenFile":"x"}`))
	if err == nil {
		t.Fatal("http distant doit être refusé")
	}
	if _, err := Load(write(t, `{"controlPlaneUrl":"http://127.0.0.1:9090","agentTokenFile":"x"}`)); err != nil {
		t.Fatalf("http loopback doit être accepté: %v", err)
	}
}

func TestRejectsUnknownField(t *testing.T) {
	if _, err := Load(write(t, `{"password":"secret"}`)); err == nil {
		t.Fatal("champ inconnu doit être refusé")
	}
}

func TestPasswordFile(t *testing.T) {
	dir := t.TempDir()
	pf := filepath.Join(dir, "pw")
	os.WriteFile(pf, []byte("s3cret\n"), 0o600)
	c := &Config{MariaDB: MariaDBConfig{PasswordFile: pf}}
	pw, err := c.MariaDBPassword()
	if err != nil || pw != "s3cret" {
		t.Fatalf("pw=%q err=%v", pw, err)
	}
}
