package ops

import (
	"os"
	"testing"
	"time"
)

func parseFixture(t *testing.T, p BinlogSearchParams) []BinlogTx {
	t.Helper()
	f, err := os.Open("testdata/binlog-verbose.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []BinlogTx
	parser := &binlogParser{file: "binlog.000003", filter: newFilter(p),
		from: time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), to: time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC),
		onTx: func(tx *BinlogTx, _ int) { out = append(out, *tx) }}
	if err := parseStream(f, parser); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestBinlogParserReadsTransactions(t *testing.T) {
	txs := parseFixture(t, BinlogSearchParams{})
	want := []struct {
		gtid, typ string
		rows      int
	}{{"0-2-1244", "DDL", 1}, {"0-2-1245", "DDL", 1}, {"0-2-1246", "INSERT", 200}, {"0-2-1560", "UPDATE", 50}, {"0-2-1672", "DELETE", 100}}
	if len(txs) != len(want) {
		t.Fatalf("attendu %d transactions, obtenu %d : %+v", len(want), len(txs), txs)
	}
	for i, w := range want {
		tx := txs[i]
		if tx.GTID != w.gtid || len(tx.Changes) != 1 || tx.Changes[0].Type != w.typ || tx.Changes[0].Rows != w.rows {
			t.Fatalf("transaction %d : %+v", i, tx)
		}
		if tx.ServerID != 2 || tx.File != "binlog.000003" || tx.Pos == 0 {
			t.Fatalf("métadonnées : %+v", tx)
		}
	}
	if txs[1].Changes[0].Database != "prestige_demo" || txs[1].Changes[0].Statement == "" {
		t.Fatalf("DDL : %+v", txs[1].Changes[0])
	}
	upd := txs[3].Changes[0]
	if len(upd.Sample) != searchSampleRows || upd.Sample[0].Before[2] != "@3=1.50" || upd.Sample[0].After[2] != "@3=11.50" {
		t.Fatalf("avant/après : %+v", upd.Sample[0])
	}
}

func TestBinlogParserFilters(t *testing.T) {
	txs := parseFixture(t, BinlogSearchParams{Types: []string{"DELETE"}, Tables: []string{"prestige_demo.clients"}})
	if len(txs) != 1 || txs[0].GTID != "0-2-1672" || txs[0].Changes[0].Rows != 100 {
		t.Fatalf("suppression : %+v", txs)
	}
	if len(txs[0].Queries) != 1 || txs[0].Queries[0] != "DELETE FROM clients WHERE id > 100" {
		t.Fatalf("requête d'origine : %+v", txs[0].Queries)
	}
	if txs[0].Changes[0].Sample[0].Before[0] != "@1=101" || txs[0].Changes[0].Sample[0].After != nil {
		t.Fatalf("ligne supprimée : %+v", txs[0].Changes[0].Sample[0])
	}
	txs = parseFixture(t, BinlogSearchParams{Text: "'client 150'"})
	if len(txs) != 2 || txs[0].Changes[0].Rows != 1 || txs[1].Changes[0].Rows != 1 || txs[1].Changes[0].Type != "DELETE" {
		t.Fatalf("recherche de texte : %+v", txs)
	}
	txs = parseFixture(t, BinlogSearchParams{Types: []string{"DDL"}, Databases: []string{"prestige_demo"}})
	if len(txs) != 1 || txs[0].GTID != "0-2-1245" {
		t.Fatalf("DDL de la base : %+v", txs)
	}
	txs = parseFixture(t, BinlogSearchParams{Tables: []string{"prestige_demo.autre"}})
	if len(txs) != 0 {
		t.Fatalf("autre table : %+v", txs)
	}
}

func TestBinlogSearchValidates(t *testing.T) {
	ok := BinlogSearchParams{Binlogs: []BinlogRef{{RelativePath: "t/1/binlog.000001.prmseal"}}, From: time.Now().Add(-time.Hour), To: time.Now(), ResultDir: "t/s/searches/x"}
	if err := ok.validate(); err != nil {
		t.Fatal(err)
	}
	bad := []func(p *BinlogSearchParams){
		func(p *BinlogSearchParams) { p.Binlogs = nil },
		func(p *BinlogSearchParams) { p.Binlogs = []BinlogRef{{RelativePath: "../x/binlog.000001.prmseal"}} },
		func(p *BinlogSearchParams) { p.To = p.From },
		func(p *BinlogSearchParams) { p.From = p.To.Add(-40 * 24 * time.Hour) },
		func(p *BinlogSearchParams) { p.Tables = []string{"clients; DROP"} },
		func(p *BinlogSearchParams) { p.Types = []string{"TRUNCATE"} },
		func(p *BinlogSearchParams) { p.ResultDir = "../../etc" },
	}
	for i, f := range bad {
		p := ok
		f(&p)
		if err := p.validate(); err == nil {
			t.Fatalf("cas %d accepté", i)
		}
	}
}
