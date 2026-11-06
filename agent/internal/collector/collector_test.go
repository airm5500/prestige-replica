package collector

import (
	"database/sql"
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestRedact(t *testing.T) {
	cases := map[string]string{
		"error connecting: password=abc123 host":                         "error connecting: password=*** host",
		"IDENTIFIED BY 'x' password 'hunter2'":                           "IDENTIFIED BY 'x' password ***",
		"token: eyJhbGciOi":                                              "token: ***",
		"Access denied for user 'repl'@'10.0.0.2' (using password: YES)": "Access denied for user 'repl'@'10.0.0.2' (using password: YES)",
	}
	for in, want := range cases {
		if got := Redact(in); got != want {
			t.Errorf("Redact(%q) = %q, attendu %q", in, got, want)
		}
	}
}

func TestAllowListIsReadOnly(t *testing.T) {
	for _, s := range AllowedStatements {
		// On ignore les littéraux (noms de variables) avant de chercher les mots-clés.
		u := strings.ToUpper(regexp.MustCompile(`'[^']*'`).ReplaceAllString(s, "''"))
		if !(strings.HasPrefix(u, "SHOW ") || u == "SET SESSION TRANSACTION READ ONLY") {
			t.Errorf("instruction non autorisée dans la liste blanche: %s", s)
		}
		for _, bad := range []string{"STOP ", "START ", "CHANGE ", "RESET ", "PURGE ", "KILL ", "INSERT", "UPDATE", "DELETE", "DROP", "GRANT", "FLUSH"} {
			if strings.Contains(u, bad) {
				t.Errorf("mot-clé %q interdit dans %s", bad, s)
			}
		}
	}
}

func TestBuildChannelNullLag(t *testing.T) {
	ch := buildChannel(map[string]sql.NullString{
		"Slave_IO_Running":      {String: "Connecting", Valid: true},
		"Seconds_Behind_Master": {Valid: false},
		"Last_IO_Error":         {String: "error connecting to master 'repl@db:3306' - password=abc", Valid: true},
	})
	if ch.SecondsBehindMaster != nil {
		t.Fatal("NULL doit rester nil")
	}
	if strings.Contains(ch.LastIOError, "abc") {
		t.Fatal("secret non masqué")
	}
}

func TestBuildServerExpireDays(t *testing.T) {
	s := buildServer(map[string]string{"expire_logs_days": "7.000000", "read_only": "ON", "log_bin": "ON"}, map[string]string{})
	if s.BinlogExpireSeconds != 7*86400 || !s.ReadOnly || !s.LogBin {
		t.Fatalf("%+v", s)
	}
}

func TestDiskUsage(t *testing.T) {
	total, free, err := DiskUsage(os.TempDir())
	if err != nil || total == 0 || free > total {
		t.Fatalf("total=%d free=%d err=%v", total, free, err)
	}
}
