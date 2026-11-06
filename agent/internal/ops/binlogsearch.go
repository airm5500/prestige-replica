package ops

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/airm5500/prestige-replica-manager/agent/internal/seal"
)

// TypeBinlogSearch : recherche dans les journaux archivés (§5 « Lecture et
// extraction des journaux binaires », vue d'audit lisible). Le format binaire
// n'est jamais décodé par l'agent : il lit la sortie de mariadb-binlog
// --base64-output=DECODE-ROWS --verbose. Le résultat sert au diagnostic, ce
// n'est pas un script de restauration (celle-ci passe par PITR_RESTORE).
const TypeBinlogSearch = "BINLOG_SEARCH"

const (
	searchMaxPreview   = 200  // transactions renvoyées à la console
	searchMaxFull      = 5000 // transactions conservées dans le résultat chiffré
	searchSampleRows   = 20   // lignes d'exemple par changement
	searchValueMax     = 200  // caractères par valeur
	searchMaxRangeDays = 31
)

var searchTypes = map[string]bool{"INSERT": true, "UPDATE": true, "DELETE": true, "DDL": true}

type BinlogSearchParams struct {
	Binlogs         []BinlogRef `json:"binlogs"`
	From            time.Time   `json:"from"`
	To              time.Time   `json:"to"`
	Databases       []string    `json:"databases"`
	Tables          []string    `json:"tables"`
	Types           []string    `json:"types"`
	Text            string      `json:"text"`
	MaxTransactions int         `json:"maxTransactions"`
	ResultDir       string      `json:"resultDir"`
}

type BinlogRow struct {
	Before []string `json:"before,omitempty"`
	After  []string `json:"after,omitempty"`
}

type BinlogChange struct {
	Type      string      `json:"type"`
	Database  string      `json:"database,omitempty"`
	Table     string      `json:"table,omitempty"`
	Statement string      `json:"statement,omitempty"`
	Rows      int         `json:"rows"`
	Sample    []BinlogRow `json:"sample,omitempty"`
}

type BinlogTx struct {
	GTID     string    `json:"gtid"`
	At       time.Time `json:"at"`
	ServerID int64     `json:"serverId"`
	File     string    `json:"file"`
	// Fichier archivé : permet une restauration arrêtée exactement avant cette transaction.
	RelativePath string         `json:"relativePath,omitempty"`
	Pos          int64          `json:"pos"`
	Queries      []string       `json:"queries,omitempty"`
	Changes      []BinlogChange `json:"changes"`
}

var (
	dbNameRe    = regexp.MustCompile(`^[A-Za-z0-9_$]{1,64}$`)
	serverIDRe  = regexp.MustCompile(`server id (\d+)`)
	rowHeadRe   = regexp.MustCompile("^### (INSERT INTO|UPDATE|DELETE FROM) `([^`]+)`\\.`([^`]+)`$")
	useRe       = regexp.MustCompile("^use `([^`]+)`/\\*!\\*/;$")
	firstWordRe = regexp.MustCompile(`^\s*([A-Za-z]+)`)
)

func (p *BinlogSearchParams) validate() error {
	if len(p.Binlogs) == 0 {
		return errors.New("aucun journal à parcourir sur cette période")
	}
	if len(p.Binlogs) > 500 {
		return errors.New("trop de journaux pour une seule recherche : réduisez la période")
	}
	for _, b := range p.Binlogs {
		if !archivePathRe.MatchString(b.RelativePath) {
			return fmt.Errorf("chemin de binlog invalide : %q", b.RelativePath)
		}
		if b.SHA256 != "" && !sha256Re.MatchString(b.SHA256) {
			return errors.New("empreinte invalide")
		}
	}
	if p.From.IsZero() || p.To.IsZero() || !p.To.After(p.From) {
		return errors.New("période invalide")
	}
	if p.To.Sub(p.From) > searchMaxRangeDays*24*time.Hour {
		return fmt.Errorf("période limitée à %d jours", searchMaxRangeDays)
	}
	for _, d := range p.Databases {
		if !dbNameRe.MatchString(d) {
			return fmt.Errorf("base invalide : %q", d)
		}
	}
	for _, t := range p.Tables {
		if !tableRe.MatchString(t) {
			return fmt.Errorf("table invalide : %q (forme base.table)", t)
		}
	}
	for _, t := range p.Types {
		if !searchTypes[t] {
			return fmt.Errorf("type d'événement inconnu : %q", t)
		}
	}
	if len(p.Text) > 200 {
		return errors.New("texte recherché trop long")
	}
	if !relPathRe.MatchString(p.ResultDir) {
		return errors.New("dossier de résultat invalide")
	}
	if p.MaxTransactions <= 0 || p.MaxTransactions > searchMaxPreview {
		p.MaxTransactions = searchMaxPreview
	}
	return nil
}

// searchFilter décide de ce qui est retenu.
type searchFilter struct {
	dbs, tables, types map[string]bool
	text               string
}

func newFilter(p BinlogSearchParams) searchFilter {
	set := func(xs []string, lower bool) map[string]bool {
		m := map[string]bool{}
		for _, x := range xs {
			if lower {
				x = strings.ToLower(x)
			}
			m[x] = true
		}
		return m
	}
	return searchFilter{dbs: set(p.Databases, true), tables: set(p.Tables, true), types: set(p.Types, false), text: strings.ToLower(p.Text)}
}

func (f searchFilter) changeScope(c *BinlogChange) bool {
	if len(f.types) > 0 && !f.types[c.Type] {
		return false
	}
	db := strings.ToLower(c.Database)
	if len(f.dbs) > 0 && !f.dbs[db] {
		return false
	}
	if len(f.tables) > 0 {
		if c.Table != "" {
			return f.tables[db+"."+strings.ToLower(c.Table)]
		}
		// Instruction (DDL…) : la table est cherchée dans le texte.
		s := strings.ToLower(c.Statement)
		for t := range f.tables {
			if strings.HasPrefix(t, db+".") && strings.Contains(s, t[len(db)+1:]) {
				return true
			}
		}
		return false
	}
	return true
}

func rowHas(r *BinlogRow, text string) bool {
	for _, v := range append(append([]string{}, r.Before...), r.After...) {
		if strings.Contains(strings.ToLower(v), text) {
			return true
		}
	}
	return false
}

// binlogParser lit la sortie verbeuse de mariadb-binlog, transaction par transaction.
type binlogParser struct {
	file     string
	relPath  string
	filter   searchFilter
	from, to time.Time
	onTx     func(tx *BinlogTx, rowsMatched int)

	pos          int64
	sessionDB    string
	tx           *BinlogTx
	inQuery      bool
	stmt         strings.Builder
	annotate     bool
	cur          *BinlogChange // changement de lignes en cours
	curMatch     bool
	row          *BinlogRow
	section      string // SET ou WHERE
	queryHit     bool
	lastAnnotate bool
	matched      int
	events       int
}

func (p *binlogParser) flushRow() {
	if p.row == nil || p.cur == nil {
		p.row = nil
		return
	}
	if p.filter.text == "" || p.queryHit || rowHas(p.row, p.filter.text) {
		p.cur.Rows++
		if len(p.cur.Sample) < searchSampleRows {
			p.cur.Sample = append(p.cur.Sample, *p.row)
		}
	}
	p.row = nil
}

func (p *binlogParser) flushChange() {
	p.flushRow()
	if p.cur != nil && p.curMatch && p.cur.Rows > 0 && p.tx != nil {
		// Regroupe avec le changement précédent s'il porte sur la même table et le même type.
		n := len(p.tx.Changes)
		if n > 0 && p.tx.Changes[n-1].Type == p.cur.Type && p.tx.Changes[n-1].Database == p.cur.Database && p.tx.Changes[n-1].Table == p.cur.Table && p.cur.Statement == "" {
			last := &p.tx.Changes[n-1]
			last.Rows += p.cur.Rows
			for _, s := range p.cur.Sample {
				if len(last.Sample) < searchSampleRows {
					last.Sample = append(last.Sample, s)
				}
			}
		} else {
			p.tx.Changes = append(p.tx.Changes, *p.cur)
		}
		p.matched += p.cur.Rows
	}
	p.cur = nil
}

func (p *binlogParser) flushTx() {
	p.flushStatement()
	p.flushChange()
	if p.tx != nil && len(p.tx.Changes) > 0 && !p.tx.At.Before(p.from) && p.tx.At.Before(p.to) {
		p.onTx(p.tx, p.matched)
	}
	p.tx, p.matched, p.queryHit = nil, 0, false
}

func classify(stmt string) string {
	m := firstWordRe.FindStringSubmatch(stmt)
	if m == nil {
		return "DDL"
	}
	switch strings.ToUpper(m[1]) {
	case "INSERT", "REPLACE":
		return "INSERT"
	case "UPDATE":
		return "UPDATE"
	case "DELETE":
		return "DELETE"
	}
	return "DDL"
}

func (p *binlogParser) flushStatement() {
	if !p.inQuery {
		return
	}
	p.inQuery = false
	s := strings.TrimSpace(p.stmt.String())
	p.stmt.Reset()
	switch strings.ToUpper(s) {
	case "", "BEGIN", "COMMIT", "ROLLBACK", "START TRANSACTION":
		return
	}
	if p.tx == nil {
		return
	}
	c := BinlogChange{Type: classify(s), Database: p.sessionDB, Statement: truncateValue(s, 4000), Rows: 1}
	if !p.filter.changeScope(&c) {
		return
	}
	if p.filter.text != "" && !strings.Contains(strings.ToLower(s), p.filter.text) {
		return
	}
	p.flushChange()
	p.tx.Changes = append(p.tx.Changes, c)
	p.matched++
}

func truncateValue(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && (s[cut]&0xC0) == 0x80 { // ne pas couper un caractère UTF-8
		cut--
	}
	return s[:cut] + "…"
}

func (p *binlogParser) line(l string) {
	if m := atRe.FindStringSubmatch(l); m != nil {
		p.pos, _ = strconv.ParseInt(m[1], 10, 64)
		return
	}
	if strings.HasPrefix(l, "#Q> ") {
		if p.tx != nil && p.annotate {
			q := strings.TrimPrefix(l, "#Q> ")
			n := len(p.tx.Queries)
			if n > 0 && p.annotateCont() {
				p.tx.Queries[n-1] = truncateValue(p.tx.Queries[n-1]+"\n"+q, 4000)
			} else if n < 50 {
				p.tx.Queries = append(p.tx.Queries, truncateValue(q, 4000))
			}
			if p.filter.text != "" && strings.Contains(strings.ToLower(q), p.filter.text) {
				p.queryHit = true
			}
		}
		return
	}
	if h := headerRe.FindStringSubmatch(l); h != nil {
		p.events++
		p.annotate = false
		p.lastAnnotate = false
		switch {
		case gtidEvtRe.MatchString(l):
			p.flushTx()
			when, _ := time.ParseInLocation("060102 15:04:05", fmt.Sprintf("%s %02s:%s:%s", h[1], h[2], h[3], h[4]), time.Local)
			g := gtidEvtRe.FindStringSubmatch(l)
			sid := int64(0)
			if m := serverIDRe.FindStringSubmatch(l); m != nil {
				sid, _ = strconv.ParseInt(m[1], 10, 64)
			}
			p.tx = &BinlogTx{GTID: g[1] + "-" + g[2] + "-" + g[3], At: when.UTC(), ServerID: sid, File: p.file, RelativePath: p.relPath, Pos: p.pos}
		case strings.Contains(l, "\tQuery\t"):
			p.flushStatement()
			p.flushChange()
			p.inQuery = true
		case strings.Contains(l, "\tAnnotate_rows:"):
			p.flushStatement()
			p.flushChange()
			p.annotate = true
			p.queryHit = false
		case strings.Contains(l, "\tXid = "):
			p.flushTx()
		default:
			p.flushStatement()
		}
		return
	}
	if m := rowHeadRe.FindStringSubmatch(l); m != nil {
		typ := map[string]string{"INSERT INTO": "INSERT", "UPDATE": "UPDATE", "DELETE FROM": "DELETE"}[m[1]]
		if p.cur == nil || p.cur.Type != typ || p.cur.Database != m[2] || p.cur.Table != m[3] {
			p.flushChange()
			p.cur = &BinlogChange{Type: typ, Database: m[2], Table: m[3]}
			p.curMatch = p.filter.changeScope(p.cur)
		} else {
			p.flushRow()
		}
		if p.curMatch {
			p.row = &BinlogRow{}
		}
		p.section = ""
		return
	}
	if strings.HasPrefix(l, "### ") {
		if p.row == nil {
			return
		}
		v := strings.TrimPrefix(l, "### ")
		switch strings.TrimSpace(v) {
		case "SET":
			p.section = "SET"
			return
		case "WHERE":
			p.section = "WHERE"
			return
		}
		v = truncateValue(strings.TrimSpace(v), searchValueMax)
		if p.section == "WHERE" {
			p.row.Before = append(p.row.Before, v)
		} else {
			p.row.After = append(p.row.After, v)
		}
		return
	}
	if p.inQuery {
		if m := useRe.FindStringSubmatch(l); m != nil {
			p.sessionDB = m[1]
			return
		}
		end := strings.HasSuffix(l, "/*!*/;")
		content := strings.TrimSuffix(l, "/*!*/;")
		if p.stmt.Len() == 0 && (strings.HasPrefix(l, "SET ") || strings.HasPrefix(l, "/*") || strings.HasPrefix(l, "DELIMITER") || strings.HasPrefix(l, "#")) {
			return
		}
		if content != "" && p.stmt.Len() < 8000 {
			if p.stmt.Len() > 0 {
				p.stmt.WriteByte('\n')
			}
			p.stmt.WriteString(content)
		}
		if end {
			p.flushStatement()
			p.inQuery = true // d'autres instructions peuvent suivre dans le même événement
		}
	}
}

// annotateCont : une requête annotée sur plusieurs lignes arrive en lignes « #Q> » consécutives.
func (p *binlogParser) annotateCont() bool {
	c := p.lastAnnotate
	p.lastAnnotate = true
	return c
}

// SearchBinlogs parcourt les journaux archivés et rend les transactions qui correspondent.
func (r *Runner) SearchBinlogs(ctx context.Context, p BinlogSearchParams, rep Reporter) (map[string]any, error) {
	if err := p.validate(); err != nil {
		return nil, err
	}
	if r.Cfg.Backup.Root == "" || r.Cfg.Backup.EncryptionKeyFile == "" {
		return nil, errors.New("backup.root et la clé de chiffrement sont nécessaires pour lire l'archive")
	}
	key, err := seal.LoadKey(r.Cfg.Backup.EncryptionKeyFile)
	if err != nil {
		return nil, err
	}
	tool, err := r.FindTool("mariadb-binlog")
	if err != nil {
		return nil, err
	}
	toolVersion := ""
	if out, verr := run(ctx, nil, nil, tool, "--version"); verr == nil {
		toolVersion = strings.TrimSpace(lastLine(out))
	}
	work, err := os.MkdirTemp("", "prm-recherche-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(work) // contient des journaux déchiffrés : jamais conservés
	t0 := time.Now()
	filter := newFilter(p)
	var all []BinlogTx
	total, rows, events := 0, 0, 0
	truncated := false
	var sources []map[string]any
	startLocal := p.From.In(time.Local).Format("2006-01-02 15:04:05")
	stopLocal := p.To.In(time.Local).Add(time.Second).Format("2006-01-02 15:04:05")
	rep.Step(fmt.Sprintf("recherche dans %d journal(aux) du %s au %s", len(p.Binlogs),
		p.From.In(time.Local).Format("02/01/2006 15:04:05"), p.To.In(time.Local).Format("02/01/2006 15:04:05")))
	for _, b := range p.Binlogs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		dirPart, base := b.RelativePath[:strings.LastIndex(b.RelativePath, "/")], b.RelativePath[strings.LastIndex(b.RelativePath, "/")+1:]
		srcDir, err := SafeJoin(r.Cfg.Backup.Root, dirPart)
		if err != nil {
			return nil, err
		}
		src := filepath.Join(srcDir, base)
		local := filepath.Join(work, strings.TrimSuffix(base, binlogSuffix))
		if err := openSealed(src, b.SHA256, local, key); err != nil {
			return nil, err
		}
		st, _ := os.Stat(local)
		size := int64(0)
		if st != nil {
			size = st.Size()
		}
		sources = append(sources, map[string]any{"relativePath": b.RelativePath, "sha256": b.SHA256, "sizeBytes": size})
		parser := &binlogParser{file: filepath.Base(local), relPath: b.RelativePath, filter: filter, from: p.From, to: p.To.Add(time.Second),
			onTx: func(tx *BinlogTx, n int) {
				total++
				rows += n
				if len(all) < searchMaxFull {
					all = append(all, *tx)
				} else {
					truncated = true
				}
			}}
		if err := runParser(ctx, tool, local, startLocal, stopLocal, parser); err != nil {
			return nil, fmt.Errorf("lecture de %s : %w", filepath.Base(local), err)
		}
		events += parser.events
		os.Remove(local)
	}
	rep.Step(fmt.Sprintf("%d transaction(s) trouvée(s), %d ligne(s) ou instruction(s) concernée(s)", total, rows))

	// Résultat complet chiffré dans le stockage ; la console ne reçoit qu'un aperçu borné.
	manifest := map[string]any{
		"createdAt": time.Now().UTC(), "tool": toolVersion, "sources": sources,
		"params":       map[string]any{"from": p.From.UTC(), "to": p.To.UTC(), "databases": p.Databases, "tables": p.Tables, "types": p.Types, "text": p.Text},
		"transactions": total, "rows": rows, "truncated": truncated,
	}
	full := map[string]any{"manifest": manifest, "transactions": all}
	resDir, err := SafeJoin(r.Cfg.Backup.Root, p.ResultDir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(resDir, 0o750); err != nil {
		return nil, err
	}
	tmp := filepath.Join(work, "resultat.json")
	fb, err := json.MarshalIndent(full, "", " ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(tmp, fb, 0o600); err != nil {
		return nil, err
	}
	dest := filepath.Join(resDir, "resultat.json"+binlogSuffix)
	sum, sz, err := sealFile(tmp, dest, key)
	if err != nil {
		return nil, err
	}
	preview := all
	if len(preview) > p.MaxTransactions {
		preview = preview[:p.MaxTransactions]
	}
	return map[string]any{
		"transactions": preview, "total": total, "rows": rows, "events": events, "truncated": truncated || total > len(preview),
		"files": len(p.Binlogs), "tool": toolVersion, "sources": sources,
		"result":       map[string]any{"relativePath": p.ResultDir + "/resultat.json" + binlogSuffix, "sha256": sum, "sizeBytes": sz},
		"totalSeconds": int(time.Since(t0).Seconds()),
	}, nil
}

func runParser(ctx context.Context, tool, file, startLocal, stopLocal string, p *binlogParser) error {
	cmd := exec.CommandContext(ctx, tool, "--no-defaults", "--base64-output=DECODE-ROWS", "--verbose",
		"--start-datetime="+startLocal, "--stop-datetime="+stopLocal, file)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	errTail := &tail{max: 10}
	cmd.Stderr = errTail
	if err := cmd.Start(); err != nil {
		return err
	}
	if err := parseStream(stdout, p); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return err
	}
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("mariadb-binlog : %v %s", err, lastLine(errTail.String()))
	}
	return nil
}

func parseStream(r io.Reader, p *binlogParser) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1024*1024), 64*1024*1024)
	for sc.Scan() {
		p.line(sc.Text())
	}
	p.flushTx()
	return sc.Err()
}
