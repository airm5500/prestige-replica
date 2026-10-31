// Package ops exécute les opérations autorisées du Lot 2. La liste est
// fermée : un type inconnu est refusé. Aucune opération n'accepte de SQL ou de
// ligne de commande provenant de la console ; les paramètres reçus sont
// validés (chemins relatifs, GTID, hôte, port) avant usage.
package ops

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/airm5500/prestige-replica-manager/agent/internal/config"
)

// Types d'opérations reconnus par l'agent.
const (
	TypePreflight        = "PREFLIGHT"
	TypeBackupFull       = "BACKUP_FULL"
	TypeRestoreTest      = "RESTORE_TEST"
	TypeProvisionReplica = "PROVISION_REPLICA"
	TypeBackupPrune      = "BACKUP_PRUNE"
	// Lot 3
	TypeReplicationPause  = "REPLICATION_PAUSE"
	TypeReplicationResume = "REPLICATION_RESUME"
	TypeSwitchoverDemote  = "SWITCHOVER_DEMOTE"
	TypeSwitchoverPromote = "SWITCHOVER_PROMOTE"
	TypeSwitchoverUndo    = "SWITCHOVER_UNDO"
	TypeReplicaAttach     = "REPLICA_ATTACH"
	// Lot 4
	TypeFence = "FENCE"
)

// Job est une opération reçue du Control Plane.
type Job struct {
	ID     string          `json:"id"`
	Type   string          `json:"type"`
	Params json.RawMessage `json:"params"`
}

// Reporter reçoit la progression d'une opération.
type Reporter interface {
	Step(message string)
	Warn(message string)
}

// ErrIntervention signale un état qui exige une action humaine (par exemple
// une reconstruction interrompue après l'arrêt du service).
var ErrIntervention = errors.New("intervention requise")

// Runner exécute les opérations avec la configuration locale.
type Runner struct {
	Cfg     *config.Config
	Monitor *sql.DB // connexion de supervision (lecture seule)
	Version string
}

// Execute lance l'opération et renvoie un résultat structuré.
func (r *Runner) Execute(ctx context.Context, j Job, rep Reporter) (map[string]any, error) {
	switch j.Type {
	case TypePreflight:
		var p PreflightParams
		if err := decode(j.Params, &p); err != nil {
			return nil, err
		}
		return r.Preflight(ctx, p, rep)
	case TypeBackupFull:
		if !r.Cfg.Backup.Enabled {
			return nil, errors.New("sauvegardes non activées dans la configuration locale de l'agent (backup.enabled)")
		}
		var p BackupParams
		if err := decode(j.Params, &p); err != nil {
			return nil, err
		}
		return r.Backup(ctx, p, rep)
	case TypeRestoreTest:
		if !r.Cfg.RestoreTest.Enabled {
			return nil, errors.New("restaurations de contrôle non activées localement (restoreTest.enabled)")
		}
		var p ArtifactParams
		if err := decode(j.Params, &p); err != nil {
			return nil, err
		}
		return r.RestoreTest(ctx, p, rep)
	case TypeProvisionReplica:
		if !r.Cfg.Provisioning.Enabled {
			return nil, errors.New("reconstruction non autorisée localement (provisioning.enabled) : opération refusée")
		}
		var p ProvisionParams
		if err := decode(j.Params, &p); err != nil {
			return nil, err
		}
		return r.Provision(ctx, p, rep)
	case TypeBackupPrune:
		if !r.Cfg.Backup.Enabled {
			return nil, errors.New("sauvegardes non activées localement")
		}
		var p PruneParams
		if err := decode(j.Params, &p); err != nil {
			return nil, err
		}
		return r.Prune(p, rep)
	case TypeReplicationPause, TypeReplicationResume, TypeSwitchoverDemote, TypeSwitchoverPromote, TypeSwitchoverUndo, TypeReplicaAttach, TypeFence:
		if !r.Cfg.ReplicationControl.Enabled {
			return nil, errors.New("pilotage de la réplication non autorisé localement (replicationControl.enabled) : opération refusée")
		}
		return r.control(ctx, j, rep)
	default:
		return nil, fmt.Errorf("type d'opération non autorisé : %q", j.Type)
	}
}

func (r *Runner) control(ctx context.Context, j Job, rep Reporter) (map[string]any, error) {
	switch j.Type {
	case TypeReplicationPause:
		var p PauseParams
		if err := decode(j.Params, &p); err != nil {
			return nil, err
		}
		return r.Pause(ctx, p, rep)
	case TypeReplicationResume:
		if err := decode(j.Params, &struct{}{}); err != nil {
			return nil, err
		}
		return r.Resume(ctx, rep)
	case TypeSwitchoverDemote:
		var p DemoteParams
		if err := decode(j.Params, &p); err != nil {
			return nil, err
		}
		return r.Demote(ctx, p, rep)
	case TypeSwitchoverPromote:
		var p PromoteParams
		if err := decode(j.Params, &p); err != nil {
			return nil, err
		}
		return r.Promote(ctx, p, rep)
	case TypeFence:
		var p FenceParams
		if err := decode(j.Params, &p); err != nil {
			return nil, err
		}
		return r.Fence(ctx, p, rep)
	case TypeSwitchoverUndo:
		if err := decode(j.Params, &struct{}{}); err != nil {
			return nil, err
		}
		return r.UndoDemote(ctx, rep)
	default: // TypeReplicaAttach
		var p AttachParams
		if err := decode(j.Params, &p); err != nil {
			return nil, err
		}
		return r.Attach(ctx, p, rep)
	}
}

func decode(raw json.RawMessage, v any) error {
	if len(raw) == 0 {
		raw = []byte("{}")
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("paramètres invalides: %w", err)
	}
	return nil
}

var (
	gtidRe    = regexp.MustCompile(`^\d+-\d+-\d+(,\d+-\d+-\d+)*$`)
	hostRe    = regexp.MustCompile(`^[A-Za-z0-9.\-:]{1,253}$`)
	sqlUserRe = regexp.MustCompile(`^[A-Za-z0-9_.\-]{1,80}$`)
	relPathRe = regexp.MustCompile(`^[A-Za-z0-9_\-]+(/[A-Za-z0-9_\-]+)*$`)
	sha256Re  = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// sqlString produit un littéral SQL sûr pour une valeur locale (mot de passe).
func sqlString(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `'`, `\'`, "\x00", `\0`, "\n", `\n`, "\r", `\r`, "\x1a", `\Z`)
	return "'" + r.Replace(s) + "'"
}
