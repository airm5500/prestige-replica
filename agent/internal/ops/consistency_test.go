package ops

import (
	"strings"
	"testing"
)

func TestCompareSchemas(t *testing.T) {
	p := &schema{objects: map[string]string{
		"table d.a": "moteur InnoDB", "column d.a.id": "int", "column d.a.nom": "varchar(20)", "index d.a.PRIMARY": "(id) unique",
		"index d.a.idx": "(nom) non unique", "table d.b": "moteur InnoDB", "column d.b.id": "int", "routine d.f": "FUNCTION empreinte 1",
		"table d.ignoree": "x", "column d.ignoree.v": "int",
	}}
	r := &schema{objects: map[string]string{
		"table d.a": "moteur InnoDB", "column d.a.id": "int", "column d.a.nom": "varchar(40)", "index d.a.PRIMARY": "(id) unique",
		"routine d.f": "FUNCTION empreinte 2", "table d.c": "moteur InnoDB", "column d.c.id": "int",
	}}
	diffs, affected := compareSchemas(p, r, func(t string) bool { return t == "d.ignoree" })
	got := map[string]string{}
	for _, d := range diffs {
		got[d.Kind+" "+d.Object] = d.Issue
	}
	want := map[string]string{
		"column d.a.nom": "DIFFERENT", "index d.a.idx": "MISSING_ON_REPLICA", "table d.b": "MISSING_ON_REPLICA",
		"routine d.f": "DIFFERENT", "table d.c": "EXTRA_ON_REPLICA",
	}
	if len(got) != len(want) {
		t.Fatalf("différences : %+v", diffs)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%s : %q attendu, %q obtenu (%+v)", k, v, got[k], diffs)
		}
	}
	if !affected["d.a"] || !affected["d.b"] || !affected["d.c"] {
		t.Fatalf("tables à ne pas comparer : %+v", affected)
	}
}

func TestChunkSQL(t *testing.T) {
	c := newChunkSQL("db.ligne`x", []string{"id", "jour", "montant"}, []string{"id", "jour"})
	w, args := c.where([]any{"1", "2026-10-30"}, []any{"9", "2026-10-31"})
	if w != " WHERE (`id`, `jour`) > (?,?) AND (`id`, `jour`) <= (?,?)" || len(args) != 4 {
		t.Fatalf("bornes : %q %v", w, args)
	}
	if c.table != "`db`.`ligne``x`" {
		t.Fatalf("identifiant mal protégé : %s", c.table)
	}
	if !strings.Contains(c.rowHash, "CONCAT(ISNULL(`id`), ISNULL(`jour`), ISNULL(`montant`))") {
		t.Fatalf("NULL non distingué : %s", c.rowHash)
	}
	s := newChunkSQL("db.t", []string{"id"}, []string{"id"})
	if w, _ := s.where(nil, []any{"5"}); w != " WHERE `id` <= ?" {
		t.Fatalf("première tranche : %q", w)
	}
}

func TestConsistencyValidates(t *testing.T) {
	ok := ConsistencyParams{SourceHost: "10.0.0.1", SourcePort: 3306}
	if err := ok.validate(); err != nil || ok.Mode != "FULL" || ok.ChunkRows != 1000 {
		t.Fatalf("%v %+v", err, ok)
	}
	for i, f := range []func(p *ConsistencyParams){
		func(p *ConsistencyParams) { p.SourceHost = "x; rm" },
		func(p *ConsistencyParams) { p.Mode = "REPAIR" },
		func(p *ConsistencyParams) { p.Databases = []string{"mysql"} },
		func(p *ConsistencyParams) { p.Exclude = []string{"a.b; DROP"} },
		func(p *ConsistencyParams) { p.ChunkRows = 50 },
		func(p *ConsistencyParams) { p.MaxMinutes = 2000 },
	} {
		p := ConsistencyParams{SourceHost: "10.0.0.1", SourcePort: 3306}
		f(&p)
		if p.validate() == nil {
			t.Fatalf("cas %d accepté", i)
		}
	}
}
