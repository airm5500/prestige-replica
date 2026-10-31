package ops

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/airm5500/prestige-replica-manager/agent/internal/config"
)

// ProvisionParams : sauvegarde vérifiée à restaurer et source de réplication.
type ProvisionParams struct {
	ArtifactParams
	SourceHost string `json:"sourceHost"`
	SourcePort int    `json:"sourcePort"`
	// DelaySeconds > 0 pour une réplique différée (MASTER_DELAY).
	DelaySeconds int `json:"delaySeconds"`
	// ConfirmDataDir doit reprendre exactement le datadir configuré localement :
	// garde-fou contre une erreur de cible.
	ConfirmDataDir string `json:"confirmDataDir"`
}

// Provision (re)construit CE serveur comme réplique à partir d'une sauvegarde :
// préparation hors ligne à côté du datadir, arrêt du service, échange des
// dossiers (l'ancien est conservé), redémarrage, configuration GTID et
// démarrage de la réplication. En cas d'échec après l'arrêt du service,
// l'ancien datadir est remis en place.
func (r *Runner) Provision(ctx context.Context, p ProvisionParams, rep Reporter) (map[string]any, error) {
	pc := r.Cfg.Provisioning
	if filepath.Clean(p.ConfirmDataDir) != filepath.Clean(pc.DataDir) {
		return nil, fmt.Errorf("confirmation du datadir incorrecte : %q attendu", pc.DataDir)
	}
	if !hostRe.MatchString(p.SourceHost) || p.SourcePort <= 0 || p.SourcePort > 65535 {
		return nil, errors.New("hôte ou port de la source invalide")
	}
	if !sqlUserRe.MatchString(pc.ReplicationUser) {
		return nil, errors.New("replicationUser invalide")
	}
	if p.DelaySeconds < 0 || p.DelaySeconds > 7*86400 {
		return nil, errors.New("délai de réplication invalide")
	}
	replPw, err := config.ReadSecret(pc.ReplicationPasswordFile)
	if err != nil {
		return nil, err
	}
	adminPw, err := config.ReadSecret(pc.AdminPasswordFile)
	if err != nil {
		return nil, err
	}
	// Refus si ce serveur est actuellement source d'autres serveurs ou porte des
	// écritures applicatives : la console l'a déjà vérifié, l'agent recontrôle.
	if err := r.refuseIfLooksPrimary(ctx); err != nil {
		return nil, err
	}

	artifact, m, key, err := r.openArtifact(p.RelativeDir, p.SHA256, rep)
	if err != nil {
		return nil, err
	}
	if m.GTID == "" || !gtidRe.MatchString(m.GTID) {
		return nil, errors.New("la sauvegarde ne contient pas de position GTID : impossible d'en faire une réplique (binlog désactivé sur la source ?)")
	}

	dataDir := filepath.Clean(pc.DataDir)
	stamp := time.Now().UTC().Format("20060102-150405")
	staging := dataDir + ".prm-new-" + stamp
	old := dataDir + ".prm-old-" + stamp
	defer os.RemoveAll(staging) // sans effet après l'échange

	// 1. Préparation hors ligne : l'instance actuelle continue de tourner.
	if err := r.extract(ctx, artifact, key, staging, rep); err != nil {
		return nil, err
	}
	if err := chownTree(staging, pc.FileOwner); err != nil {
		return nil, fmt.Errorf("droits des fichiers restaurés: %w", err)
	}

	// 2. Arrêt du service et échange des dossiers.
	rep.Step("arrêt du service MariaDB local")
	if err := r.service(ctx, false); err != nil {
		return nil, fmt.Errorf("arrêt du service: %w", err)
	}
	rollback := func(cause error) (map[string]any, error) {
		rep.Warn("échec : remise en place de l'ancien datadir")
		_ = r.service(context.Background(), false)
		failed := dataDir + ".prm-failed-" + stamp
		_ = os.Rename(dataDir, failed)
		if err := os.Rename(old, dataDir); err != nil {
			return nil, fmt.Errorf("%w : %v ; RESTAURATION MANUELLE NÉCESSAIRE (ancien datadir : %s) : %v", ErrIntervention, cause, old, err)
		}
		if err := r.service(context.Background(), true); err != nil {
			return nil, fmt.Errorf("%w : %v ; ancien datadir remis mais service non redémarré : %v", ErrIntervention, cause, err)
		}
		return nil, fmt.Errorf("%v (ancien datadir remis en place, serveur redémarré)", cause)
	}
	if err := os.Rename(dataDir, old); err != nil {
		_ = r.service(context.Background(), true)
		return nil, fmt.Errorf("déplacement de l'ancien datadir: %w", err)
	}
	if err := os.Rename(staging, dataDir); err != nil {
		return rollback(fmt.Errorf("mise en place du nouveau datadir: %w", err))
	}
	rep.Step("démarrage du service MariaDB sur les données restaurées")
	if err := r.service(ctx, true); err != nil {
		return rollback(fmt.Errorf("démarrage du service: %w", err))
	}

	// 3. Configuration de la réplication.
	admin, err := r.adminDB(r.Cfg.Provisioning.AdminUser, adminPw)
	if err != nil {
		return rollback(err)
	}
	defer admin.Close()
	if err := waitPing(ctx, admin, 5*time.Minute); err != nil {
		return rollback(fmt.Errorf("MariaDB ne répond pas après redémarrage: %w", err))
	}
	rep.Step("configuration GTID et démarrage de la réplication")
	stmts := []string{
		// Lecture seule d'abord : la réplique ne doit jamais accepter d'écritures.
		"SET GLOBAL read_only = ON",
		"STOP ALL SLAVES",
		"RESET SLAVE ALL",
		"SET GLOBAL gtid_slave_pos = '" + m.GTID + "'",
		fmt.Sprintf("CHANGE MASTER TO MASTER_HOST=%s, MASTER_PORT=%d, MASTER_USER=%s, MASTER_PASSWORD=%s, MASTER_USE_GTID=slave_pos, MASTER_DELAY=%d, MASTER_CONNECT_RETRY=10",
			sqlString(p.SourceHost), p.SourcePort, sqlString(pc.ReplicationUser), sqlString(replPw), p.DelaySeconds),
		"START SLAVE",
	}
	for _, s := range stmts {
		if _, err := admin.ExecContext(ctx, s); err != nil {
			// Le datadir restauré est cohérent : on ne revient pas en arrière,
			// la réplication se corrige sans nouvelle restauration.
			return nil, fmt.Errorf("%w : %s : %v", ErrIntervention, strings.SplitN(s, " MASTER_PASSWORD", 2)[0], err)
		}
	}

	// 4. Attente du rattrapage.
	rep.Step("attente du rattrapage de la réplique")
	state, err := waitReplicating(ctx, admin, 15*time.Minute)
	if err != nil {
		return map[string]any{"replication": state, "previousDataDir": old}, fmt.Errorf("%w : %v", ErrIntervention, err)
	}
	rep.Step("réplique opérationnelle ; ancien datadir conservé dans " + old)
	return map[string]any{
		"backupId":        m.BackupID,
		"gtidStart":       m.GTID,
		"replication":     state,
		"previousDataDir": old,
	}, nil
}

func (r *Runner) refuseIfLooksPrimary(ctx context.Context) error {
	rows, err := r.Monitor.QueryContext(ctx, "SHOW SLAVE HOSTS")
	if err != nil {
		return nil // privilège absent : contrôle laissé à la console
	}
	defer rows.Close()
	if rows.Next() {
		return errors.New("des répliques sont connectées à ce serveur : il agit comme source, reconstruction refusée")
	}
	return nil
}

func (r *Runner) adminDB(user, password string) (*sql.DB, error) {
	mc := mysql.NewConfig()
	mc.User = user
	mc.Passwd = password
	if strings.HasPrefix(r.Cfg.MariaDB.Address, "unix:") {
		mc.Net, mc.Addr = "unix", strings.TrimPrefix(r.Cfg.MariaDB.Address, "unix:")
	} else {
		mc.Net, mc.Addr = "tcp", r.Cfg.MariaDB.Address
	}
	mc.Timeout = 5 * time.Second
	db, err := sql.Open("mysql", mc.FormatDSN())
	if err != nil {
		return nil, errors.New("configuration du compte administrateur invalide")
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

func waitPing(ctx context.Context, db *sql.DB, max time.Duration) error {
	deadline := time.Now().Add(max)
	for {
		err := db.PingContext(ctx)
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

func waitReplicating(ctx context.Context, db *sql.DB, max time.Duration) (map[string]string, error) {
	deadline := time.Now().Add(max)
	state := map[string]string{}
	for {
		rows, err := db.QueryContext(ctx, "SHOW SLAVE STATUS")
		if err == nil {
			cols, _ := rows.Columns()
			if rows.Next() {
				vals := make([]sql.NullString, len(cols))
				ptrs := make([]any, len(cols))
				for i := range vals {
					ptrs[i] = &vals[i]
				}
				if rows.Scan(ptrs...) == nil {
					for i, c := range cols {
						switch c {
						case "Slave_IO_Running", "Slave_SQL_Running", "Seconds_Behind_Master", "Gtid_IO_Pos", "Last_IO_Error", "Last_SQL_Error":
							state[c] = vals[i].String
						}
					}
				}
			}
			rows.Close()
		}
		if state["Slave_SQL_Running"] == "No" && state["Last_SQL_Error"] != "" {
			return state, fmt.Errorf("erreur SQL de réplication : %s", state["Last_SQL_Error"])
		}
		if state["Slave_IO_Running"] == "Yes" && state["Slave_SQL_Running"] == "Yes" {
			if lag, err := strconv.Atoi(state["Seconds_Behind_Master"]); err == nil && lag <= 5 {
				return state, nil
			}
		}
		if time.Now().After(deadline) {
			return state, errors.New("la réplique n'a pas rattrapé la source dans le délai (" + state["Last_IO_Error"] + ")")
		}
		select {
		case <-ctx.Done():
			return state, ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
}

// service arrête ou démarre le service MariaDB local et attend la fin de l'action.
func (r *Runner) service(ctx context.Context, start bool) error {
	pc := r.Cfg.Provisioning
	cmd := pc.StopCommand
	if start {
		cmd = pc.StartCommand
	}
	if len(cmd) == 0 {
		name := pc.ServiceName
		if name == "" {
			name = map[bool]string{true: "MariaDB", false: "mariadb"}[runtime.GOOS == "windows"]
		}
		verb := map[bool]string{true: "start", false: "stop"}[start]
		if runtime.GOOS == "windows" {
			cmd = []string{"net", verb, name} // « net » attend la fin de l'arrêt/démarrage
		} else {
			cmd = []string{"systemctl", verb, name}
		}
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	_, err := run(cctx, nil, nil, cmd[0], cmd[1:]...)
	return err
}
