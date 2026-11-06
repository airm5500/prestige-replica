package ops

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/airm5500/prestige-replica-manager/agent/internal/config"
)

// Lot 3 : pilotage de la réplication. Ces opérations ne remplacent jamais de
// données ; elles agissent sur l'état de réplication (STOP/START SLAVE,
// read_only, CHANGE MASTER) avec le compte administrateur local.

// PauseParams : « ALL » arrête les deux threads, « SQL » seulement l'application
// (les journaux continuent d'être reçus : reprise plus rapide).
type PauseParams struct {
	Mode string `json:"mode"`
	// Reason : motif saisi dans la console, conservé pour l'audit uniquement.
	Reason string `json:"reason"`
}

// DemoteParams : rétrogradation de l'ancien primaire avant une bascule.
type DemoteParams struct {
	// MaxWaitSeconds : délai pour constater l'arrêt complet des écritures.
	MaxWaitSeconds int `json:"maxWaitSeconds"`
}

// PromoteParams : promotion de la réplique cible une fois la position atteinte.
type PromoteParams struct {
	GTID           string `json:"gtid"`
	TimeoutSeconds int    `json:"timeoutSeconds"`
	// WaitRelay (bascule d'urgence) : la source est perdue ; on attend que
	// tout ce qui a été reçu (Gtid_IO_Pos) soit appliqué avant de promouvoir.
	WaitRelay bool `json:"waitRelay"`
}

// FenceParams : isolement d'un ancien primaire (bascule d'urgence).
type FenceParams struct {
	// StopService : si MariaDB ne répond pas, arrêter son service pour
	// qu'un processus bloqué ne reprenne pas les écritures.
	StopService bool `json:"stopService"`
}

// AttachParams : (re)branchement de ce serveur sur une nouvelle source.
type AttachParams struct {
	SourceHost string `json:"sourceHost"`
	SourcePort int    `json:"sourcePort"`
	// DelaySeconds : nil = conserver le retard volontaire actuel.
	DelaySeconds *int `json:"delaySeconds"`
}

func (r *Runner) controlDB() (*sql.DB, error) {
	rc := r.Cfg.ReplicationControl
	pw, err := readSecretFile(rc.AdminPasswordFile)
	if err != nil {
		return nil, err
	}
	return r.adminDB(rc.AdminUser, pw)
}

// slaveStatus renvoie les colonnes utiles de SHOW SLAVE STATUS (vide si ce
// serveur n'est pas réplique).
func slaveStatus(ctx context.Context, db *sql.DB) (map[string]string, error) {
	rows, err := db.QueryContext(ctx, "SHOW SLAVE STATUS")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	state := map[string]string{}
	cols, _ := rows.Columns()
	if rows.Next() {
		vals := make([]sql.NullString, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		for i, c := range cols {
			switch c {
			case "Master_Host", "Master_Port", "Slave_IO_Running", "Slave_SQL_Running", "Seconds_Behind_Master", "SQL_Delay",
				"Gtid_IO_Pos", "Using_Gtid", "Last_IO_Error", "Last_SQL_Error", "Last_IO_Errno", "Last_SQL_Errno":
				state[c] = vals[i].String
			}
		}
	}
	return state, rows.Err()
}

func scalar(ctx context.Context, db *sql.DB, q string) (string, error) {
	var v sql.NullString
	err := db.QueryRowContext(ctx, q).Scan(&v)
	return v.String, err
}

// Pause arrête la réplication de ce serveur (réplique uniquement).
func (r *Runner) Pause(ctx context.Context, p PauseParams, rep Reporter) (map[string]any, error) {
	stmt := "STOP SLAVE"
	switch p.Mode {
	case "", "ALL":
	case "SQL":
		stmt = "STOP SLAVE SQL_THREAD"
	default:
		return nil, errors.New("mode de pause invalide (ALL ou SQL)")
	}
	db, err := r.controlDB()
	if err != nil {
		return nil, err
	}
	defer db.Close()
	st, err := slaveStatus(ctx, db)
	if err != nil {
		return nil, fmt.Errorf("lecture de l'état de réplication: %w", err)
	}
	if len(st) == 0 {
		return nil, errors.New("ce serveur n'est pas une réplique : rien à suspendre")
	}
	rep.Step("arrêt de la réplication (" + stmt + ")")
	if _, err := db.ExecContext(ctx, stmt); err != nil {
		return nil, fmt.Errorf("%s : %w", stmt, err)
	}
	st, _ = slaveStatus(ctx, db)
	pos, _ := scalar(ctx, db, "SELECT @@gtid_slave_pos")
	rep.Step("réplication suspendue à la position GTID " + pos)
	return map[string]any{"mode": p.Mode, "gtidSlavePos": pos, "replication": st}, nil
}

// Resume relance la réplication et vérifie que les deux threads tournent.
func (r *Runner) Resume(ctx context.Context, rep Reporter) (map[string]any, error) {
	db, err := r.controlDB()
	if err != nil {
		return nil, err
	}
	defer db.Close()
	st, err := slaveStatus(ctx, db)
	if err != nil {
		return nil, fmt.Errorf("lecture de l'état de réplication: %w", err)
	}
	if len(st) == 0 {
		return nil, errors.New("aucun canal de réplication configuré : utilisez une reconstruction ou un rattachement")
	}
	rep.Step("démarrage de la réplication (START SLAVE)")
	if _, err := db.ExecContext(ctx, "START SLAVE"); err != nil {
		return nil, fmt.Errorf("START SLAVE : %w", err)
	}
	st, err = waitRunning(ctx, db, time.Minute)
	if err != nil {
		return map[string]any{"replication": st}, err
	}
	rep.Step("réplication en cours, retard " + st["Seconds_Behind_Master"] + " s")
	return map[string]any{"replication": st}, nil
}

// waitRunning attend que les threads IO et SQL tournent sans erreur.
func waitRunning(ctx context.Context, db *sql.DB, max time.Duration) (map[string]string, error) {
	deadline := time.Now().Add(max)
	for {
		st, err := slaveStatus(ctx, db)
		if err == nil {
			if st["Last_SQL_Error"] != "" && st["Slave_SQL_Running"] != "Yes" {
				return st, fmt.Errorf("erreur SQL de réplication %s : %s", st["Last_SQL_Errno"], st["Last_SQL_Error"])
			}
			if st["Slave_IO_Running"] == "Yes" && st["Slave_SQL_Running"] == "Yes" {
				return st, nil
			}
		}
		if time.Now().After(deadline) {
			return st, fmt.Errorf("la réplication ne tourne pas après %s (IO=%s, SQL=%s) %s", max, st["Slave_IO_Running"], st["Slave_SQL_Running"], st["Last_IO_Error"])
		}
		select {
		case <-ctx.Done():
			return st, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// Demote met l'ancien primaire en lecture seule, coupe les connexions
// applicatives et vérifie que plus aucune transaction n'est écrite. En cas
// d'échec, read_only est rétabli : le primaire reste primaire.
func (r *Runner) Demote(ctx context.Context, p DemoteParams, rep Reporter) (res map[string]any, err error) {
	if p.MaxWaitSeconds <= 0 || p.MaxWaitSeconds > 600 {
		p.MaxWaitSeconds = 30
	}
	db, err := r.controlDB()
	if err != nil {
		return nil, err
	}
	defer db.Close()
	if st, err := slaveStatus(ctx, db); err != nil {
		return nil, fmt.Errorf("lecture de l'état de réplication: %w", err)
	} else if st["Slave_SQL_Running"] == "Yes" {
		return nil, errors.New("ce serveur est lui-même une réplique en cours : ce n'est pas le primaire, rétrogradation refusée")
	}
	if ro, _ := scalar(ctx, db, "SELECT @@read_only"); ro == "1" {
		rep.Warn("le serveur était déjà en lecture seule")
	}
	rep.Step("passage en lecture seule (read_only=ON)")
	if _, err := db.ExecContext(ctx, "SET GLOBAL read_only = ON"); err != nil {
		return nil, fmt.Errorf("SET GLOBAL read_only : %w", err)
	}
	defer func() {
		if err != nil {
			if _, rerr := db.ExecContext(context.WithoutCancel(ctx), "SET GLOBAL read_only = OFF"); rerr != nil {
				err = fmt.Errorf("%w : %v ; ET le retour en écriture a échoué : %v", ErrIntervention, err, rerr)
				return
			}
			rep.Warn("retour arrière : le serveur accepte de nouveau les écritures")
			res = map[string]any{"demoted": false}
		}
	}()

	killed, err := r.killClients(ctx, db, rep)
	if err != nil {
		return nil, err
	}

	rep.Step("contrôle de l'arrêt des écritures")
	deadline := time.Now().Add(time.Duration(p.MaxWaitSeconds) * time.Second)
	prev, err := scalar(ctx, db, "SELECT @@gtid_binlog_pos")
	if err != nil {
		return nil, err
	}
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
		cur, err := scalar(ctx, db, "SELECT @@gtid_binlog_pos")
		if err != nil {
			return nil, err
		}
		if cur == prev {
			rep.Step("plus aucune écriture ; position finale " + cur)
			return map[string]any{"demoted": true, "gtid": cur, "killedConnections": killed}, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("des écritures continuent malgré read_only (%s → %s) : un compte applicatif dispose sans doute du privilège SUPER ; bascule annulée", prev, cur)
		}
		rep.Warn("écritures encore en cours (" + cur + "), nouvelle vérification")
		prev = cur
		if n, err := r.killClients(ctx, db, rep); err == nil {
			killed += n
		}
	}
}

// killClients coupe les connexions clientes, sauf réplication, supervision,
// administration et comptes protégés.
func (r *Runner) killClients(ctx context.Context, db *sql.DB, rep Reporter) (int, error) {
	protected := []string{"system user", "event_scheduler", "", r.Cfg.ReplicationControl.AdminUser, r.Cfg.MariaDB.User,
		r.Cfg.Backup.User, r.Cfg.ReplicationControl.ReplicationUser}
	protected = append(protected, r.Cfg.ReplicationControl.ProtectedUsers...)
	rows, err := db.QueryContext(ctx, "SELECT ID, USER, COMMAND FROM information_schema.PROCESSLIST WHERE ID <> CONNECTION_ID()")
	if err != nil {
		return 0, fmt.Errorf("liste des connexions: %w", err)
	}
	type conn struct {
		id         int64
		user, comm string
	}
	var list []conn
	for rows.Next() {
		var c conn
		var user, comm sql.NullString
		if err := rows.Scan(&c.id, &user, &comm); err != nil {
			rows.Close()
			return 0, err
		}
		c.user, c.comm = user.String, comm.String
		list = append(list, c)
	}
	rows.Close()
	killed := 0
	users := map[string]int{}
	for _, c := range list {
		if slices.Contains(protected, c.user) || strings.HasPrefix(c.comm, "Binlog Dump") || c.comm == "Daemon" || c.comm == "Slave_IO" || c.comm == "Slave_SQL" {
			continue
		}
		if _, err := db.ExecContext(ctx, "KILL CONNECTION "+strconv.FormatInt(c.id, 10)); err != nil {
			rep.Warn(fmt.Sprintf("connexion %d (%s) non coupée : %v", c.id, c.user, err))
			continue
		}
		killed++
		users[c.user]++
	}
	if killed > 0 {
		parts := []string{}
		for u, n := range users {
			parts = append(parts, fmt.Sprintf("%s×%d", u, n))
		}
		slices.Sort(parts)
		rep.Step(fmt.Sprintf("%d connexion(s) applicative(s) coupée(s) : %s", killed, strings.Join(parts, ", ")))
	}
	return killed, nil
}

// Promote attend que la réplique ait appliqué toutes les transactions de
// l'ancien primaire, puis la détache et l'ouvre en écriture. Tant que la
// réplication n'est pas arrêtée, rien n'est modifié (promoted=false).
func (r *Runner) Promote(ctx context.Context, p PromoteParams, rep Reporter) (map[string]any, error) {
	if p.GTID != "" && !gtidRe.MatchString(p.GTID) {
		return nil, errors.New("position GTID invalide")
	}
	if p.TimeoutSeconds <= 0 || p.TimeoutSeconds > 3600 {
		p.TimeoutSeconds = 120
	}
	notPromoted := map[string]any{"promoted": false}
	db, err := r.controlDB()
	if err != nil {
		return notPromoted, err
	}
	defer db.Close()
	st, err := slaveStatus(ctx, db)
	if err != nil {
		return notPromoted, fmt.Errorf("lecture de l'état de réplication: %w", err)
	}
	if len(st) == 0 {
		return notPromoted, errors.New("ce serveur n'est pas une réplique : promotion refusée")
	}
	if st["Slave_SQL_Running"] != "Yes" {
		return notPromoted, fmt.Errorf("le thread SQL est arrêté (%s) : la réplique n'est pas à jour, promotion refusée", st["Last_SQL_Error"])
	}
	start := time.Now()
	if p.WaitRelay {
		if p.GTID != "" {
			return notPromoted, errors.New("gtid et waitRelay sont exclusifs")
		}
		p.GTID = st["Gtid_IO_Pos"]
		rep.Step("source perdue : application de tout ce qui a été reçu (" + p.GTID + ")")
	}
	if p.GTID != "" && !gtidRe.MatchString(p.GTID) {
		return notPromoted, errors.New("position reçue illisible : " + p.GTID)
	}
	if p.GTID != "" {
		rep.Step("attente de l'application de la position " + p.GTID)
		var res sql.NullInt64
		if err := db.QueryRowContext(ctx, "SELECT MASTER_GTID_WAIT(?, ?)", p.GTID, p.TimeoutSeconds).Scan(&res); err != nil {
			return notPromoted, fmt.Errorf("MASTER_GTID_WAIT : %w", err)
		}
		if !res.Valid || res.Int64 != 0 {
			return notPromoted, fmt.Errorf("position %s non atteinte en %d s : promotion annulée, rien n'a été modifié", p.GTID, p.TimeoutSeconds)
		}
		rep.Step(fmt.Sprintf("toutes les transactions de l'ancien primaire sont appliquées (%d ms)", time.Since(start).Milliseconds()))
	}
	rep.Step("arrêt et suppression de la configuration de réplication")
	if _, err := db.ExecContext(ctx, "STOP SLAVE"); err != nil {
		return notPromoted, fmt.Errorf("STOP SLAVE : %w", err)
	}
	for _, s := range []string{"RESET SLAVE ALL", "SET GLOBAL read_only = OFF"} {
		if _, err := db.ExecContext(ctx, s); err != nil {
			return map[string]any{"promoted": false, "partial": true}, fmt.Errorf("%w : %s : %v", ErrIntervention, s, err)
		}
	}
	pos, _ := scalar(ctx, db, "SELECT @@gtid_current_pos")
	rep.Step("serveur promu primaire, ouvert en écriture ; position " + pos)
	return map[string]any{"promoted": true, "gtid": pos, "waitMs": time.Since(start).Milliseconds()}, nil
}

// Attach (re)branche ce serveur sur une nouvelle source avec GTID. Un ancien
// primaire repart de sa propre position binlog ; une réplique conserve la sienne.
func (r *Runner) Attach(ctx context.Context, p AttachParams, rep Reporter) (map[string]any, error) {
	if !hostRe.MatchString(p.SourceHost) || p.SourcePort <= 0 || p.SourcePort > 65535 {
		return nil, errors.New("source invalide")
	}
	if p.DelaySeconds != nil && (*p.DelaySeconds < 0 || *p.DelaySeconds > 7*86400) {
		return nil, errors.New("retard invalide")
	}
	rc := r.Cfg.ReplicationControl
	replPw, err := readSecretFile(rc.ReplicationPasswordFile)
	if err != nil {
		return nil, err
	}
	db, err := r.controlDB()
	if err != nil {
		return nil, err
	}
	defer db.Close()
	st, err := slaveStatus(ctx, db)
	if err != nil {
		return nil, fmt.Errorf("lecture de l'état de réplication: %w", err)
	}
	delay := 0
	if d, err := strconv.Atoi(st["SQL_Delay"]); err == nil {
		delay = d
	}
	if p.DelaySeconds != nil {
		delay = *p.DelaySeconds
	}
	stmts := []string{"SET GLOBAL read_only = ON"}
	if len(st) > 0 {
		rep.Step(fmt.Sprintf("réplique actuelle de %s:%s : changement de source", st["Master_Host"], st["Master_Port"]))
		stmts = append(stmts, "STOP SLAVE")
	} else {
		pos, _ := scalar(ctx, db, "SELECT @@gtid_binlog_pos")
		rep.Step("ancien primaire : reprise à partir de sa propre position " + pos)
		stmts = append(stmts, "SET GLOBAL gtid_slave_pos = @@gtid_binlog_pos")
	}
	stmts = append(stmts,
		fmt.Sprintf("CHANGE MASTER TO MASTER_HOST=%s, MASTER_PORT=%d, MASTER_USER=%s, MASTER_PASSWORD=%s, MASTER_USE_GTID=slave_pos, MASTER_DELAY=%d, MASTER_CONNECT_RETRY=10",
			sqlString(p.SourceHost), p.SourcePort, sqlString(rc.ReplicationUser), sqlString(replPw), delay),
		"START SLAVE")
	rep.Step(fmt.Sprintf("branchement sur %s:%d (retard volontaire %d s)", p.SourceHost, p.SourcePort, delay))
	for _, s := range stmts {
		if _, err := db.ExecContext(ctx, s); err != nil {
			return nil, fmt.Errorf("%w : %s : %v", ErrIntervention, strings.SplitN(s, " MASTER_PASSWORD", 2)[0], err)
		}
	}
	st, err = waitRunning(ctx, db, 90*time.Second)
	if err != nil {
		return map[string]any{"replication": st}, fmt.Errorf("%w : %v", ErrIntervention, err)
	}
	rep.Step("réplication en cours depuis la nouvelle source")
	return map[string]any{"replication": st, "delaySeconds": delay}, nil
}

// Fence isole un ancien primaire : lecture seule et coupure des connexions
// s'il répond ; arrêt du service sinon (s'il est demandé). Jamais l'inverse :
// aucune opération ne rouvre en écriture un serveur isolé.
func (r *Runner) Fence(ctx context.Context, p FenceParams, rep Reporter) (map[string]any, error) {
	db, err := r.controlDB()
	if err != nil {
		return nil, err
	}
	defer db.Close()
	pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	err = db.PingContext(pctx)
	cancel()
	if err == nil {
		rep.Step("MariaDB répond : passage en lecture seule et coupure des connexions")
		if _, err := db.ExecContext(ctx, "SET GLOBAL read_only = ON"); err != nil {
			return nil, fmt.Errorf("SET GLOBAL read_only : %w", err)
		}
		killed, _ := r.killClients(ctx, db, rep)
		pos, _ := scalar(ctx, db, "SELECT @@gtid_binlog_pos")
		r.markFenced(rep, "lecture seule ; dernière position "+pos)
		rep.Step("serveur isolé ; dernière position locale " + pos)
		return map[string]any{"fenced": true, "readOnly": true, "killedConnections": killed, "gtidBinlogPos": pos}, nil
	}
	rep.Warn("MariaDB ne répond pas : " + err.Error())
	r.markFenced(rep, "MariaDB injoignable au moment de l'isolement")
	if !p.StopService {
		return map[string]any{"fenced": true, "mariadbDown": true}, nil
	}
	rep.Step("arrêt du service MariaDB pour empêcher toute reprise des écritures")
	serr := r.service(ctx, false)
	if serr != nil {
		// Service déjà arrêté : « net stop » / systemctl renvoient une erreur sans gravité.
		rep.Warn("arrêt du service : " + serr.Error())
	}
	res := map[string]any{"fenced": true, "mariadbDown": true, "serviceStopped": serr == nil}
	// Sans cela, un redémarrage de la machine relancerait MariaDB en écriture.
	if derr := r.disableAutoStart(ctx); derr != nil {
		rep.Warn("désactivation du démarrage automatique : " + derr.Error())
	} else {
		rep.Step("démarrage automatique du service désactivé (à réactiver après reconstruction)")
		res["autoStartDisabled"] = true
	}
	return res, nil
}

// UndoDemote rouvre en écriture un ancien primaire rétrogradé, si la promotion
// de la cible n'a pas eu lieu. Refusé si ce serveur réplique déjà depuis une
// autre source.
func (r *Runner) UndoDemote(ctx context.Context, rep Reporter) (map[string]any, error) {
	db, err := r.controlDB()
	if err != nil {
		return nil, err
	}
	defer db.Close()
	st, err := slaveStatus(ctx, db)
	if err != nil {
		return nil, err
	}
	if len(st) > 0 {
		return nil, errors.New("ce serveur est configuré comme réplique : retour en écriture refusé")
	}
	rep.Step("retour en écriture (read_only=OFF)")
	if _, err := db.ExecContext(ctx, "SET GLOBAL read_only = OFF"); err != nil {
		return nil, err
	}
	return map[string]any{"readOnly": false}, nil
}

func readSecretFile(path string) (string, error) { return config.ReadSecret(path) }

// disableAutoStart passe le service MariaDB en démarrage manuel.
func (r *Runner) disableAutoStart(ctx context.Context) error {
	if len(r.Cfg.Provisioning.StopCommand) > 0 {
		return errors.New("commande d'arrêt personnalisée : désactivez le démarrage automatique à la main")
	}
	name := r.Cfg.Provisioning.ServiceName
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if runtime.GOOS == "windows" {
		if name == "" {
			name = "MariaDB"
		}
		_, err := run(cctx, nil, nil, "sc", "config", name, "start=", "demand")
		return err
	}
	if name == "" {
		name = "mariadb"
	}
	_, err := run(cctx, nil, nil, "systemctl", "disable", name)
	return err
}

// ---- Isolement persistant -------------------------------------------------
//
// Un serveur isolé garde un marqueur local. Tant qu'il existe, l'agent remet
// MariaDB en lecture seule dès qu'il le trouve en écriture (redémarrage après
// une coupure de courant, relance manuelle), sans attendre la console. Seule
// une reconstruction réussie (ou « prm-agent unfence ») le supprime.

// markerPath : un marqueur par instance MariaDB, pour que deux agents dont les
// configurations partagent un dossier ne s'isolent pas mutuellement.
func (r *Runner) markerPath() string {
	id := strings.Map(func(c rune) rune {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			return c
		}
		return '_'
	}, r.Cfg.MariaDB.Address)
	return filepath.Join(r.Cfg.StateDir, "fenced-"+id+".json")
}

// Fenced indique si ce serveur est isolé localement.
func (r *Runner) Fenced() bool {
	_, err := os.Stat(r.markerPath())
	return err == nil
}

func (r *Runner) markFenced(rep Reporter, reason string) {
	b, _ := json.Marshal(map[string]string{"at": time.Now().UTC().Format(time.RFC3339), "reason": reason})
	if err := os.WriteFile(r.markerPath(), b, 0o600); err != nil {
		rep.Warn("marqueur d'isolement non écrit (" + err.Error() + ") : la remise en lecture seule après redémarrage dépendra de la console")
	}
}

// Unfence lève l'isolement local.
func (r *Runner) Unfence() error {
	err := os.Remove(r.markerPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// GuardFence remet le serveur en lecture seule s'il est isolé et accessible en
// écriture. Renvoie vrai si une correction a été faite.
func (r *Runner) GuardFence(ctx context.Context, rep Reporter) (bool, error) {
	if !r.Fenced() {
		return false, nil
	}
	db, err := r.controlDB()
	if err != nil {
		return false, err
	}
	defer db.Close()
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	ro, err := scalar(cctx, db, "SELECT @@read_only")
	if err != nil || ro == "1" {
		return false, nil // MariaDB arrêté ou déjà protégé
	}
	if _, err := db.ExecContext(ctx, "SET GLOBAL read_only = ON"); err != nil {
		return false, fmt.Errorf("SET GLOBAL read_only : %w", err)
	}
	killed, _ := r.killClients(ctx, db, rep)
	rep.Warn(fmt.Sprintf("serveur isolé trouvé en écriture : remis en lecture seule, %d connexion(s) coupée(s)", killed))
	return true, nil
}
