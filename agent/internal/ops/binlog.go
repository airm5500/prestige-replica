package ops

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/airm5500/prestige-replica-manager/agent/internal/config"
	"github.com/airm5500/prestige-replica-manager/agent/internal/seal"
	"github.com/go-sql-driver/mysql"
)

// ---- Archivage continu des binlogs ---------------------------------------
//
// L'agent du serveur de sauvegarde lit les binlogs du primaire par le
// protocole de réplication (mariadb-binlog --read-from-remote-server --raw),
// comme une réplique : rien n'est installé ni lu sur le disque du primaire.
// Chaque fichier est chiffré avec la clé des sauvegardes. Le fichier en cours
// d'écriture est archivé aussi, puis remplacé au passage suivant : la perte
// maximale d'une restauration à un instant précis est l'intervalle d'archivage.

const binlogSuffix = ".prmseal"

// BinlogArchiveParams : source (primaire actuel), dossier attribué par la
// console et fichiers déjà archivés complets pour ce serveur.
type BinlogArchiveParams struct {
	SourceHost  string   `json:"sourceHost"`
	SourcePort  int      `json:"sourcePort"`
	ServerID    int64    `json:"serverId"`
	RelativeDir string   `json:"relativeDir"`
	Archived    []string `json:"archived"`
	MaxFiles    int      `json:"maxFiles"`
	// Expired : fichiers archivés antérieurs à la plus ancienne sauvegarde conservée.
	Expired []string `json:"expired"`
}

// BinlogInfo décrit un fichier archivé.
type BinlogInfo struct {
	File         string     `json:"file"`
	RelativePath string     `json:"relativePath"`
	SizeBytes    int64      `json:"sizeBytes"`
	SHA256       string     `json:"sha256"`
	FirstGTID    string     `json:"firstGtid,omitempty"`
	LastGTID     string     `json:"lastGtid,omitempty"`
	FirstEventAt *time.Time `json:"firstEventAt,omitempty"`
	LastEventAt  *time.Time `json:"lastEventAt,omitempty"`
	Complete     bool       `json:"complete"`
}

var (
	binlogNameRe  = regexp.MustCompile(`^[A-Za-z0-9_\-]{1,64}\.\d{6,}$`)
	archivePathRe = regexp.MustCompile(`^[A-Za-z0-9_\-]+(/[A-Za-z0-9_\-]+)*/[A-Za-z0-9_\-]{1,64}\.\d{6,}\.prmseal$`)
)

func binlogNumber(name string) int64 {
	n, _ := strconv.ParseInt(name[strings.LastIndex(name, ".")+1:], 10, 64)
	return n
}

// ArchiveBinlogs récupère les binlogs non encore archivés du primaire.
func (r *Runner) ArchiveBinlogs(ctx context.Context, p BinlogArchiveParams, rep Reporter) (map[string]any, error) {
	if !hostRe.MatchString(p.SourceHost) || p.SourcePort <= 0 || p.SourcePort > 65535 || p.ServerID <= 0 {
		return nil, errors.New("source invalide")
	}
	if !relPathRe.MatchString(p.RelativeDir) {
		return nil, errors.New("dossier relatif invalide")
	}
	for _, a := range p.Archived {
		if !binlogNameRe.MatchString(a) {
			return nil, fmt.Errorf("nom de binlog invalide : %q", a)
		}
	}
	for _, e := range p.Expired {
		if !archivePathRe.MatchString(e) {
			return nil, fmt.Errorf("chemin d'archive invalide : %q", e)
		}
	}
	if p.MaxFiles <= 0 || p.MaxFiles > 200 {
		p.MaxFiles = 50
	}
	dir, err := SafeJoin(r.Cfg.Backup.Root, p.RelativeDir+"/"+strconv.FormatInt(p.ServerID, 10))
	if err != nil {
		return nil, err
	}
	key, err := seal.LoadKey(r.Cfg.Backup.EncryptionKeyFile)
	if err != nil {
		return nil, err
	}
	tool, err := r.FindTool("mariadb-binlog")
	if err != nil {
		return nil, err
	}
	pw, err := config.ReadSecret(r.Cfg.BinlogArchive.PasswordFile)
	if err != nil {
		return nil, err
	}
	addr := net.JoinHostPort(p.SourceHost, strconv.Itoa(p.SourcePort))

	// Liste des binlogs et identité de la source.
	mc := mysql.NewConfig()
	mc.User, mc.Passwd, mc.Net, mc.Addr, mc.Timeout = r.Cfg.BinlogArchive.User, pw, "tcp", addr, 5*time.Second
	db, err := sql.Open("mysql", mc.FormatDSN())
	if err != nil {
		return nil, err
	}
	defer db.Close()
	sid, err := scalar(ctx, db, "SELECT @@server_id")
	if err != nil {
		return nil, fmt.Errorf("connexion à la source %s : %w", addr, err)
	}
	if sid != strconv.FormatInt(p.ServerID, 10) {
		return nil, fmt.Errorf("la source %s a le server_id %s, %d attendu : primaire changé, archivage reporté", addr, sid, p.ServerID)
	}
	rows, err := db.QueryContext(ctx, "SHOW BINARY LOGS")
	if err != nil {
		return nil, fmt.Errorf("SHOW BINARY LOGS (droit BINLOG MONITOR requis) : %w", err)
	}
	var names []string
	for rows.Next() {
		cols, _ := rows.Columns()
		vals := make([]sql.RawBytes, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			rows.Close()
			return nil, err
		}
		names = append(names, string(vals[0]))
	}
	rows.Close()
	if len(names) == 0 {
		return nil, errors.New("journal binaire désactivé sur la source")
	}
	done := map[string]bool{}
	var maxDone int64
	for _, a := range p.Archived {
		done[a] = true
		if n := binlogNumber(a); n > maxDone {
			maxDone = n
		}
	}
	active := names[len(names)-1]
	var todo []string
	for _, n := range names {
		if !done[n] && binlogNameRe.MatchString(n) {
			todo = append(todo, n)
		}
	}
	gap := ""
	if maxDone > 0 && len(todo) > 0 && binlogNumber(todo[0]) > maxDone+1 {
		gap = fmt.Sprintf("fichiers %d à %d purgés sur la source avant archivage", maxDone+1, binlogNumber(todo[0])-1)
		rep.Warn("trou dans l'archive : " + gap)
	}
	if len(todo) > p.MaxFiles {
		todo = todo[:p.MaxFiles] // les plus anciens d'abord ; la suite au passage suivant
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	cnf, cleanup, err := defaultsFile(r.Cfg.BinlogArchive.User, pw, addr)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	work, err := os.MkdirTemp("", "prm-binlog-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(work)

	var out []BinlogInfo
	var total int64
	for _, name := range todo {
		if _, err := run(ctx, nil, nil, tool, "--defaults-extra-file="+cnf, "--read-from-remote-server", "--raw",
			"--result-file="+work+string(os.PathSeparator), name); err != nil {
			return map[string]any{"files": out, "gap": gap}, fmt.Errorf("lecture de %s : %w", name, err)
		}
		local := filepath.Join(work, name)
		info, err := scanBinlog(ctx, tool, local)
		if err != nil {
			return map[string]any{"files": out, "gap": gap}, fmt.Errorf("analyse de %s : %w", name, err)
		}
		info.File = name
		info.Complete = name != active
		info.RelativePath = p.RelativeDir + "/" + strconv.FormatInt(p.ServerID, 10) + "/" + name + binlogSuffix
		dest := filepath.Join(dir, name+binlogSuffix)
		sum, size, err := sealFile(local, dest, key)
		if err != nil {
			return map[string]any{"files": out, "gap": gap}, err
		}
		info.SHA256, info.SizeBytes = sum, size
		total += size
		out = append(out, info)
		os.Remove(local)
	}
	last := ""
	if len(out) > 0 && out[len(out)-1].LastEventAt != nil {
		last = out[len(out)-1].LastEventAt.Format("02/01 15:04:05")
	}
	var deleted []string
	for _, e := range p.Expired {
		i := strings.LastIndex(e, "/")
		d, err := SafeJoin(r.Cfg.Backup.Root, e[:i])
		if err != nil {
			continue
		}
		if err := os.Remove(filepath.Join(d, e[i+1:])); err == nil || errors.Is(err, os.ErrNotExist) {
			deleted = append(deleted, e)
		}
	}
	if len(deleted) > 0 {
		rep.Step(fmt.Sprintf("%d fichier(s) expiré(s) supprimé(s) (antérieurs à la plus ancienne sauvegarde conservée)", len(deleted)))
	}
	rep.Step(fmt.Sprintf("%d fichier(s) archivé(s), %d Ko chiffrés%s", len(out), total>>10, map[bool]string{true: " ; dernière transaction " + last, false: ""}[last != ""]))
	return map[string]any{"files": out, "gap": gap, "active": active, "deleted": deleted}, nil
}

// sealFile chiffre src vers dest (écriture atomique) et renvoie l'empreinte du fichier chiffré.
func sealFile(src, dest string, key seal.Key) (string, int64, error) {
	in, err := os.Open(src)
	if err != nil {
		return "", 0, err
	}
	defer in.Close()
	tmp := dest + ".partial"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
	if err != nil {
		return "", 0, err
	}
	hw := newHashingWriter(f)
	if err := seal.Encrypt(hw, in, key); err != nil {
		f.Close()
		os.Remove(tmp)
		return "", 0, fmt.Errorf("chiffrement : %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return "", 0, err
	}
	if err := os.Rename(tmp, dest); err != nil {
		return "", 0, err
	}
	return hw.Sum(), hw.n, nil
}

// openSealed déchiffre un fichier archivé (après contrôle de l'empreinte) vers dest.
func openSealed(src, expectedSHA, dest string, key seal.Key) error {
	sum, _, err := FileSHA256(src)
	if err != nil {
		return err
	}
	if expectedSHA != "" && sum != expectedSHA {
		return fmt.Errorf("%s : empreinte différente du catalogue (fichier altéré)", filepath.Base(src))
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err := seal.Decrypt(out, in, key); err != nil {
		out.Close()
		return fmt.Errorf("déchiffrement de %s : %w", filepath.Base(src), err)
	}
	return out.Close()
}

// Lignes de mariadb-binlog : « # at 1234 » puis « #251031 19:29:20 server id 1 end_log_pos … GTID 0-1-5 trans ».
var (
	atRe      = regexp.MustCompile(`^# at (\d+)$`)
	headerRe  = regexp.MustCompile(`^#(\d{6})\s+(\d{1,2}):(\d{2}):(\d{2})\s+server id`)
	gtidEvtRe = regexp.MustCompile(`\sGTID (\d+)-(\d+)-(\d+)`)
)

type gtidPos struct {
	domain, server, seq int64
	at                  int64
	when                time.Time
}

// eachGTID parcourt les événements GTID d'un binlog local.
func eachGTID(ctx context.Context, tool, file string, fn func(g gtidPos) bool) (first, last *time.Time, err error) {
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(cctx, tool, "--no-defaults", "--base64-output=decode-rows", file)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	errTail := &tail{max: 10}
	cmd.Stderr = errTail
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 1024*1024), 64*1024*1024)
	var at int64
	stopped := false
	for sc.Scan() {
		line := sc.Text()
		if m := atRe.FindStringSubmatch(line); m != nil {
			at, _ = strconv.ParseInt(m[1], 10, 64)
			continue
		}
		h := headerRe.FindStringSubmatch(line)
		if h == nil {
			continue
		}
		when, perr := time.ParseInLocation("060102 15:04:05", fmt.Sprintf("%s %02s:%s:%s", h[1], h[2], h[3], h[4]), time.Local)
		if perr != nil {
			continue
		}
		// L'en-tête de format (position 4) porte l'heure de création du fichier, pas une transaction.
		if at > 4 {
			if first == nil {
				t := when.UTC()
				first = &t
			}
			t := when.UTC()
			last = &t
		}
		if m := gtidEvtRe.FindStringSubmatch(line); m != nil && !stopped {
			d, _ := strconv.ParseInt(m[1], 10, 64)
			s, _ := strconv.ParseInt(m[2], 10, 64)
			q, _ := strconv.ParseInt(m[3], 10, 64)
			if !fn(gtidPos{domain: d, server: s, seq: q, at: at, when: when.UTC()}) {
				stopped = true
			}
		}
	}
	if serr := sc.Err(); serr != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return first, last, serr
	}
	if werr := cmd.Wait(); werr != nil && cctx.Err() == nil {
		return first, last, fmt.Errorf("mariadb-binlog : %v %s", werr, lastLine(errTail.String()))
	}
	return first, last, nil
}

func scanBinlog(ctx context.Context, tool, file string) (BinlogInfo, error) {
	var info BinlogInfo
	first, last, err := eachGTID(ctx, tool, file, func(g gtidPos) bool {
		id := fmt.Sprintf("%d-%d-%d", g.domain, g.server, g.seq)
		if info.FirstGTID == "" {
			info.FirstGTID = id
		}
		info.LastGTID = id
		return true
	})
	info.FirstEventAt, info.LastEventAt = first, last
	return info, err
}

// ---- Restauration à un instant précis -----------------------------------

// BinlogRef : fichier archivé à rejouer, dans l'ordre fourni par la console.
type BinlogRef struct {
	RelativePath string `json:"relativePath"`
	SHA256       string `json:"sha256"`
}

// PITRParams : sauvegarde de départ, binlogs, instant visé et tables à extraire.
type PITRParams struct {
	Backup     ArtifactParams `json:"backup"`
	StartGTID  string         `json:"startGtid"`
	Binlogs    []BinlogRef    `json:"binlogs"`
	StopAt     time.Time      `json:"stopAt"`
	Extract    []string       `json:"extract"`
	ExtractDir string         `json:"extractDir"`
}

var tableRe = regexp.MustCompile(`^[A-Za-z0-9_$]{1,64}\.[A-Za-z0-9_$]{1,64}$`)

func gtidDomainSeq(g string) (int64, int64, bool) {
	p := strings.Split(strings.TrimSpace(strings.Split(g, ",")[0]), "-")
	if len(p) != 3 {
		return 0, 0, false
	}
	d, e1 := strconv.ParseInt(p[0], 10, 64)
	s, e2 := strconv.ParseInt(p[2], 10, 64)
	return d, s, e1 == nil && e2 == nil
}

// PointInTimeRestore restaure une sauvegarde vérifiée dans une instance
// isolée, rejoue les binlogs archivés jusqu'à l'instant visé, puis extrait
// éventuellement des tables dans des fichiers SQL chiffrés. La production
// n'est jamais modifiée : la réinjection reste une décision humaine.
func (r *Runner) PointInTimeRestore(ctx context.Context, p PITRParams, rep Reporter) (map[string]any, error) {
	if p.StopAt.IsZero() {
		return nil, errors.New("instant visé manquant")
	}
	if p.StartGTID != "" && !gtidRe.MatchString(p.StartGTID) {
		return nil, errors.New("GTID de départ invalide")
	}
	for _, b := range p.Binlogs {
		if !archivePathRe.MatchString(b.RelativePath) {
			return nil, fmt.Errorf("chemin de binlog invalide : %q", b.RelativePath)
		}
		if b.SHA256 != "" && !sha256Re.MatchString(b.SHA256) {
			return nil, errors.New("empreinte invalide")
		}
	}
	for _, t := range p.Extract {
		if !tableRe.MatchString(t) {
			return nil, fmt.Errorf("table invalide : %q (forme base.table)", t)
		}
	}
	if len(p.Extract) > 0 && !relPathRe.MatchString(p.ExtractDir) {
		return nil, errors.New("dossier d'extraction invalide")
	}
	t0 := time.Now()
	artifact, m, key, err := r.openArtifact(p.Backup.RelativeDir, p.Backup.SHA256, rep)
	if err != nil {
		return nil, err
	}
	startGTID := firstNonEmpty(p.StartGTID, m.GTID)
	domain, applied, ok := gtidDomainSeq(startGTID)
	if !ok {
		return nil, errors.New("position GTID de la sauvegarde inconnue : rejeu impossible")
	}
	binlogTool, err := r.FindTool("mariadb-binlog")
	if err != nil {
		return nil, err
	}
	client, err := r.FindTool("mariadb")
	if err != nil {
		if client, err = r.FindTool("mysql"); err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(r.Cfg.RestoreTest.ScratchDir, 0o750); err != nil {
		return nil, err
	}
	scratch, err := os.MkdirTemp(r.Cfg.RestoreTest.ScratchDir, "pitr-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(scratch)
	datadir := filepath.Join(scratch, "data")
	if err := r.extract(ctx, artifact, key, datadir, rep); err != nil {
		return nil, err
	}
	if err := chownTree(r.Cfg.RestoreTest.ScratchDir, r.Cfg.RestoreTest.RunAsUser); err != nil {
		return nil, fmt.Errorf("droits du dossier de restauration: %w", err)
	}
	rep.Step("démarrage de l'instance isolée")
	db, stop, err := r.startIsolated(ctx, scratch, datadir)
	if err != nil {
		return nil, err
	}
	defer stop()
	port := strings.TrimPrefix(isolatedAddr(db), "127.0.0.1:")

	// Rejeu fichier par fichier : chaque fichier repart du premier GTID non
	// encore appliqué, ce qui couvre les recouvrements après une bascule
	// (le nouveau primaire contient aussi les transactions répliquées).
	stopLocal := p.StopAt.In(time.Local).Format("2006-01-02 15:04:05")
	binDir := filepath.Join(scratch, "binlogs")
	_ = os.MkdirAll(binDir, 0o700)
	files, events, accountStmts := 0, 0, 0
	var lastApplied time.Time
	lastGTID := startGTID
	var gaps []string
	rep.Step(fmt.Sprintf("rejeu des journaux depuis %s jusqu'au %s", startGTID, p.StopAt.In(time.Local).Format("02/01/2006 15:04:05")))
	for _, b := range p.Binlogs {
		dirPart, base := b.RelativePath[:strings.LastIndex(b.RelativePath, "/")], b.RelativePath[strings.LastIndex(b.RelativePath, "/")+1:]
		srcDir, err := SafeJoin(r.Cfg.Backup.Root, dirPart)
		if err != nil {
			return nil, err
		}
		src := filepath.Join(srcDir, base) // base validé par archivePathRe
		if err != nil {
			return nil, err
		}
		local := filepath.Join(binDir, strings.TrimSuffix(filepath.Base(b.RelativePath), binlogSuffix))
		if err := openSealed(src, b.SHA256, local, key); err != nil {
			return nil, err
		}
		var startAt int64 = -1
		var fileMax int64 = applied
		pastStop := false
		n := 0
		if _, _, err := eachGTID(ctx, binlogTool, local, func(g gtidPos) bool {
			if g.domain != domain || g.seq <= applied {
				return true
			}
			// --stop-datetime s'arrête au premier événement daté à cette seconde ou après.
			if !g.when.Before(p.StopAt.Truncate(time.Second)) {
				pastStop = true
				return false
			}
			if g.seq > applied+1 && (startAt < 0 || g.seq > fileMax+1) {
				prev := applied
				if startAt >= 0 {
					prev = fileMax
				}
				gaps = append(gaps, fmt.Sprintf("%d-%d", prev+1, g.seq-1))
			}
			if startAt < 0 {
				startAt = g.at
			}
			n++
			fileMax = g.seq
			lastApplied = g.when
			lastGTID = fmt.Sprintf("%d-%d-%d", g.domain, g.server, g.seq)
			return true
		}); err != nil {
			return nil, err
		}
		if startAt >= 0 {
			skipped, err := replay(ctx, binlogTool, client, local, startAt, stopLocal, port)
			if err != nil {
				return nil, fmt.Errorf("rejeu de %s : %w", filepath.Base(local), err)
			}
			if skipped > 0 {
				accountStmts += skipped
				rep.Warn(fmt.Sprintf("%s : %d instruction(s) de gestion des comptes ignorée(s) (sans effet sur les données)", filepath.Base(local), skipped))
			}
			applied = fileMax
			files++
			events += n
		}
		os.Remove(local)
		if pastStop {
			break
		}
	}
	if len(gaps) > 0 {
		rep.Warn("transactions absentes de l'archive (numéros de séquence " + strings.Join(gaps, ", ") + ") : état restauré incomplet")
	}
	rep.Step(fmt.Sprintf("%d transaction(s) rejouée(s) depuis %d fichier(s) ; dernière : %s", events, files,
		map[bool]string{true: lastApplied.In(time.Local).Format("02/01/2006 15:04:05"), false: "aucune"}[events > 0]))

	checks, err := inspectRestored(ctx, db)
	if err != nil {
		return nil, err
	}
	var extracted []map[string]any
	if len(p.Extract) > 0 {
		dumpTool, err := r.FindTool("mariadb-dump")
		if err != nil {
			if dumpTool, err = r.FindTool("mysqldump"); err != nil {
				return nil, err
			}
		}
		outDir, err := SafeJoin(r.Cfg.Backup.Root, p.ExtractDir)
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(outDir, 0o750); err != nil {
			return nil, err
		}
		for _, t := range p.Extract {
			dbName, table, _ := strings.Cut(t, ".")
			plain := filepath.Join(scratch, t+".sql")
			f, err := os.OpenFile(plain, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
			if err != nil {
				return nil, err
			}
			_, derr := run(ctx, f, nil, dumpTool, "--no-defaults", "--host=127.0.0.1", "--port="+port, "--user=root",
				"--single-transaction", "--skip-lock-tables", "--no-create-db", "--skip-add-drop-table", "--complete-insert", dbName, table)
			f.Close()
			if derr != nil {
				return nil, fmt.Errorf("extraction de %s : %w", t, derr)
			}
			name := t + ".sql" + binlogSuffix
			sum, size, err := sealFile(plain, filepath.Join(outDir, name), key)
			if err != nil {
				return nil, err
			}
			extracted = append(extracted, map[string]any{"table": t, "relativePath": p.ExtractDir + "/" + name, "sha256": sum, "sizeBytes": size})
			rep.Step(fmt.Sprintf("table %s extraite (%d Ko chiffrés)", t, size>>10))
		}
	}
	res := map[string]any{
		"backupId": m.BackupID, "startGtid": startGTID, "reachedGtid": lastGTID, "binlogFiles": files, "transactions": events,
		"stopAt": p.StopAt.UTC(), "checks": checks, "gaps": gaps, "skippedAccountStatements": accountStmts, "extracted": extracted, "totalSeconds": int(time.Since(t0).Seconds()),
	}
	if events > 0 {
		res["lastTransactionAt"] = lastApplied.UTC()
	}
	return res, nil
}

// replay applique un binlog local à partir d'une position, jusqu'à l'heure d'arrêt.
func replay(ctx context.Context, binlogTool, client, file string, startAt int64, stopLocal, port string) (int, error) {
	pr, pw := io.Pipe()
	errc := make(chan error, 1)
	go func() {
		_, err := run(ctx, pw, nil, binlogTool, "--no-defaults", "--start-position="+strconv.FormatInt(startAt, 10),
			"--stop-datetime="+stopLocal, file)
		pw.CloseWithError(err)
		errc <- err
	}()
	// --force : l'instance isolée tourne sans tables de comptes (--skip-grant-tables), donc les
	// instructions de gestion des comptes (GRANT, CREATE USER…) y sont refusées (erreur 1290).
	// Elles sont sans effet sur les données : on les ignore et on les compte. Toute autre
	// erreur fait échouer la restauration.
	ew := &replayErrors{}
	cmd := exec.CommandContext(ctx, client, "--no-defaults", "--host=127.0.0.1", "--port="+port, "--user=root", "--binary-mode", "--force")
	cmd.Stdin = pr
	cmd.Stdout = io.Discard
	cmd.Stderr = ew
	cerr := cmd.Run()
	pr.Close()
	if err := <-errc; err != nil {
		return ew.skipped, err
	}
	if ew.other != "" {
		return ew.skipped, fmt.Errorf("mariadb : %s", ew.other)
	}
	if cerr != nil && ew.skipped == 0 {
		return 0, fmt.Errorf("mariadb : %w", cerr)
	}
	return ew.skipped, nil
}

// replayErrors trie les erreurs du client mariadb pendant le rejeu.
type replayErrors struct {
	buf     []byte
	skipped int
	other   string
}

func (e *replayErrors) Write(p []byte) (int, error) {
	e.buf = append(e.buf, p...)
	for {
		i := bytes.IndexByte(e.buf, '\n')
		if i < 0 {
			break
		}
		e.line(strings.TrimSpace(string(e.buf[:i])))
		e.buf = e.buf[i+1:]
	}
	if len(e.buf) > 64<<10 {
		e.line(strings.TrimSpace(string(e.buf)))
		e.buf = nil
	}
	return len(p), nil
}

func (e *replayErrors) line(l string) {
	// Avec --force, le client recopie aussi l'instruction fautive (entre tirets) : seules
	// les lignes « ERROR » comptent.
	switch {
	case strings.HasPrefix(l, "ERROR 1290 "):
		e.skipped++
	case strings.HasPrefix(l, "ERROR ") && e.other == "":
		e.other = l
	}
}

// isolatedAddr retrouve l'adresse de l'instance isolée à partir de la connexion.
func isolatedAddr(db *sql.DB) string {
	var host, port string
	_ = db.QueryRow("SELECT '127.0.0.1', @@port").Scan(&host, &port)
	return host + ":" + port
}

// DecryptFile déchiffre une extraction ou une archive (CLI de secours).
func DecryptFile(keyFile, in, out string) error {
	key, err := seal.LoadKey(keyFile)
	if err != nil {
		return err
	}
	if _, err := os.Stat(out); err == nil {
		return errors.New("le fichier de sortie existe déjà : refus d'écraser")
	}
	return openSealed(in, "", out, key)
}
