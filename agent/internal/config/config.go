// Package config charge la configuration de l'agent. Les secrets (mot de
// passe MariaDB, jeton d'agent) ne sont jamais écrits dans le fichier de
// configuration : ils sont lus depuis un fichier protégé ou une variable
// d'environnement.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/airm5500/prestige-replica-manager/agent/internal/router"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Config est la configuration complète de l'agent.
type Config struct {
	// ControlPlaneURL, par ex. https://replication.exemple:8443. Vide = mode
	// autonome (CLI uniquement, aucun envoi).
	ControlPlaneURL string `json:"controlPlaneUrl"`
	// AgentTokenFile contient le jeton délivré à l'enregistrement du nœud.
	AgentTokenFile string `json:"agentTokenFile"`
	// TLS mutuel vers le Control Plane.
	TLS TLSConfig `json:"tls"`

	MariaDB MariaDBConfig `json:"mariadb"`

	// IntervalSeconds entre deux collectes (défaut 30 s).
	IntervalSeconds int `json:"intervalSeconds"`
	// JobPollSeconds : consultation des opérations en attente (défaut 5 s,
	// accélérée à la demande de la console pendant une bascule).
	JobPollSeconds int `json:"jobPollSeconds"`
	// BufferSize : nombre maximal de rapports conservés hors connexion.
	BufferSize int `json:"bufferSize"`
	// ExtraDiskPaths : points de montage supplémentaires (binlogs, sauvegardes).
	ExtraDiskPaths []string `json:"extraDiskPaths"`

	// Lot 2 : opérations. Chaque capacité est désactivée tant qu'elle n'est pas
	// configurée localement : la console ne peut rien activer à distance.
	Tools        ToolsConfig        `json:"tools"`
	Backup       BackupConfig       `json:"backup"`
	RestoreTest  RestoreTestConfig  `json:"restoreTest"`
	Provisioning ProvisioningConfig `json:"provisioning"`
	// Lot 3 : pause/reprise, bascule planifiée, rattachement à une nouvelle source.
	ReplicationControl ReplicationControlConfig `json:"replicationControl"`
	// Archivage continu des binlogs du primaire (restauration à un instant précis).
	BinlogArchive BinlogArchiveConfig `json:"binlogArchive"`
	// Lot 4 : routeur de connexion (adresse stable pour Prestige).
	Router router.Config `json:"router"`
	// StateDir : état local persistant (marqueur d'isolement). Défaut : dossier du fichier de configuration.
	StateDir string `json:"stateDir"`
}

// ReplicationControlConfig autorise le pilotage de la réplication de CE serveur
// (STOP/START SLAVE, read_only, CHANGE MASTER). Ne remplace jamais de données.
type ReplicationControlConfig struct {
	Enabled bool `json:"enabled"`
	// Compte administrateur local ; à défaut, celui de provisioning.
	AdminUser         string `json:"adminUser"`
	AdminPasswordFile string `json:"adminPasswordFile"`
	// Compte de réplication utilisé pour se connecter à une nouvelle source.
	ReplicationUser         string `json:"replicationUser"`
	ReplicationPasswordFile string `json:"replicationPasswordFile"`
	// ProtectedUsers : comptes dont les connexions ne sont jamais coupées lors
	// d'une rétrogradation (en plus du compte administrateur et de supervision).
	ProtectedUsers []string `json:"protectedUsers"`
}

// ToolsConfig indique où trouver les binaires MariaDB officiels.
type ToolsConfig struct {
	// BinDir : dossier contenant mariadb-backup, mbstream et mariadbd. Vide =
	// recherche dans le PATH puis dans « C:\Program Files\MariaDB *\bin ».
	BinDir string `json:"binDir"`
}

// BackupConfig active les sauvegardes physiques sur ce nœud.
type BackupConfig struct {
	Enabled bool `json:"enabled"`
	// Root : racine du stockage (dossier local, partage SMB \\serveur\prm ou montage).
	Root string `json:"root"`
	// Compte MariaDB dédié aux sauvegardes (RELOAD, PROCESS, LOCK TABLES, BINLOG MONITOR).
	User              string `json:"user"`
	PasswordFile      string `json:"passwordFile"`
	EncryptionKeyFile string `json:"encryptionKeyFile"`
	// Parallel : threads de copie de mariadb-backup (défaut 2).
	Parallel int `json:"parallel"`
}

// BinlogArchiveConfig : l'agent (en général celui du serveur de sauvegarde)
// récupère les binlogs du primaire comme le ferait une réplique, les chiffre
// et les range sous backup.root. Compte : REPLICATION SLAVE et BINLOG MONITOR.
type BinlogArchiveConfig struct {
	Enabled bool `json:"enabled"`
	// À défaut, le compte de réplication (replicationControl puis provisioning).
	User         string `json:"user"`
	PasswordFile string `json:"passwordFile"`
}

// RestoreTestConfig active les restaurations de contrôle dans une instance isolée.
type RestoreTestConfig struct {
	Enabled bool `json:"enabled"`
	// ScratchDir : dossier de travail, sur un disque disposant de 2,5 fois la taille de la base.
	ScratchDir string `json:"scratchDir"`
	// BufferPoolMB : mémoire allouée à l'instance temporaire (défaut 512).
	BufferPoolMB int `json:"bufferPoolMb"`
	// RunAsUser : sous Linux, utilisateur système de l'instance temporaire (défaut « mysql »).
	RunAsUser string `json:"runAsUser"`
}

// ProvisioningConfig autorise la reconstruction de CE serveur comme réplique.
// Opération destructive : n'activer que sur une réplique, jamais sur le primaire.
type ProvisioningConfig struct {
	Enabled bool `json:"enabled"`
	// DataDir : datadir de l'instance MariaDB locale.
	DataDir string `json:"dataDir"`
	// ServiceName : nom du service (Windows « MariaDB », Linux « mariadb »).
	ServiceName string `json:"serviceName"`
	// StopCommand / StartCommand : commandes explicites (sinon dérivées du service).
	StopCommand  []string `json:"stopCommand"`
	StartCommand []string `json:"startCommand"`
	// FileOwner : sous Linux, propriétaire des fichiers restaurés (défaut « mysql »).
	FileOwner string `json:"fileOwner"`
	// Compte administrateur local pour CHANGE MASTER / START SLAVE.
	AdminUser         string `json:"adminUser"`
	AdminPasswordFile string `json:"adminPasswordFile"`
	// Compte de réplication créé sur le primaire (REPLICATION SLAVE uniquement).
	ReplicationUser         string `json:"replicationUser"`
	ReplicationPasswordFile string `json:"replicationPasswordFile"`
}

// TLSConfig décrit l'identité mTLS de l'agent.
type TLSConfig struct {
	CAFile             string `json:"caFile"`
	CertFile           string `json:"certFile"`
	KeyFile            string `json:"keyFile"`
	ServerName         string `json:"serverName"`
	InsecureSkipVerify bool   `json:"insecureSkipVerify"`
}

// MariaDBConfig décrit le compte de supervision local.
type MariaDBConfig struct {
	// Address : "127.0.0.1:3306" ou chemin de socket "unix:/run/mysqld/mysqld.sock".
	Address      string `json:"address"`
	User         string `json:"user"`
	PasswordFile string `json:"passwordFile"`
	// PasswordEnv : nom de la variable d'environnement contenant le mot de passe.
	PasswordEnv    string `json:"passwordEnv"`
	TimeoutSeconds int    `json:"timeoutSeconds"`
}

// Load lit et valide un fichier JSON.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("lecture configuration: %w", err)
	}
	var c Config
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("configuration invalide: %w", err)
	}
	if c.StateDir == "" {
		c.StateDir = filepath.Dir(path)
	}
	c.applyDefaults()
	return &c, c.Validate()
}

func (c *Config) applyDefaults() {
	if c.JobPollSeconds <= 0 || c.JobPollSeconds > 60 {
		c.JobPollSeconds = 5
	}
	if c.IntervalSeconds <= 0 {
		c.IntervalSeconds = 30
	}
	if c.BufferSize <= 0 {
		c.BufferSize = 2880 // 24 h à 30 s
	}
	if c.MariaDB.Address == "" {
		c.MariaDB.Address = "127.0.0.1:3306"
	}
	if c.MariaDB.User == "" {
		c.MariaDB.User = "prm_monitor"
	}
	if c.MariaDB.TimeoutSeconds <= 0 {
		c.MariaDB.TimeoutSeconds = 5
	}
	if c.Backup.Parallel <= 0 {
		c.Backup.Parallel = 2
	}
	if c.RestoreTest.BufferPoolMB <= 0 {
		c.RestoreTest.BufferPoolMB = 512
	}
	if c.Provisioning.FileOwner == "" {
		c.Provisioning.FileOwner = "mysql"
	}
	if c.RestoreTest.RunAsUser == "" {
		c.RestoreTest.RunAsUser = "mysql"
	}
}

// Validate refuse les combinaisons dangereuses.
func (c *Config) Validate() error {
	if c.IntervalSeconds < 5 {
		return errors.New("intervalSeconds doit être >= 5")
	}
	if c.ControlPlaneURL != "" {
		if !strings.HasPrefix(c.ControlPlaneURL, "https://") && !isLoopbackHTTP(c.ControlPlaneURL) {
			return errors.New("controlPlaneUrl doit utiliser https:// (http:// n'est accepté que vers 127.0.0.1/localhost)")
		}
		if c.AgentTokenFile == "" {
			return errors.New("agentTokenFile est obligatoire lorsque controlPlaneUrl est défini")
		}
		if (c.TLS.CertFile == "") != (c.TLS.KeyFile == "") {
			return errors.New("tls.certFile et tls.keyFile doivent être fournis ensemble")
		}
	}
	if c.Backup.Enabled && (c.Backup.Root == "" || c.Backup.User == "" || c.Backup.EncryptionKeyFile == "") {
		return errors.New("backup : root, user et encryptionKeyFile sont obligatoires")
	}
	if c.RestoreTest.Enabled && (c.RestoreTest.ScratchDir == "" || c.Backup.Root == "" || c.Backup.EncryptionKeyFile == "") {
		return errors.New("restoreTest : scratchDir, backup.root et backup.encryptionKeyFile sont obligatoires")
	}
	if c.BinlogArchive.Enabled && (c.Backup.Root == "" || c.Backup.EncryptionKeyFile == "") {
		return errors.New("binlogArchive : backup.root et backup.encryptionKeyFile sont obligatoires")
	}
	if c.Provisioning.Enabled && (c.Provisioning.DataDir == "" || c.Provisioning.AdminUser == "" || c.Provisioning.ReplicationUser == "" ||
		c.Backup.Root == "" || c.Backup.EncryptionKeyFile == "") {
		return errors.New("provisioning : dataDir, adminUser, replicationUser, backup.root et backup.encryptionKeyFile sont obligatoires")
	}
	if c.Router.Enabled {
		if c.Router.User == "" {
			c.Router.User, c.Router.PasswordFile = c.MariaDB.User, c.MariaDB.PasswordFile
		}
		if err := c.Router.Validate(); err != nil {
			return err
		}
	}
	rc := &c.ReplicationControl
	if rc.AdminUser == "" {
		rc.AdminUser, rc.AdminPasswordFile = c.Provisioning.AdminUser, c.Provisioning.AdminPasswordFile
	}
	if rc.ReplicationUser == "" {
		rc.ReplicationUser, rc.ReplicationPasswordFile = c.Provisioning.ReplicationUser, c.Provisioning.ReplicationPasswordFile
	}
	if rc.Enabled && (rc.AdminUser == "" || rc.ReplicationUser == "") {
		return errors.New("replicationControl : adminUser et replicationUser sont obligatoires (ou ceux de provisioning)")
	}
	if ba := &c.BinlogArchive; ba.User == "" {
		ba.User, ba.PasswordFile = rc.ReplicationUser, rc.ReplicationPasswordFile
	}
	if c.BinlogArchive.Enabled && c.BinlogArchive.User == "" {
		return errors.New("binlogArchive : user obligatoire (ou le compte de réplication)")
	}
	return nil
}

// Capabilities liste les capacités activées localement (affichées par la console).
func (c *Config) Capabilities() []string {
	out := []string{}
	if c.Backup.Enabled {
		out = append(out, "backup")
	}
	if c.RestoreTest.Enabled {
		out = append(out, "restoreTest")
	}
	if c.Provisioning.Enabled {
		out = append(out, "provisioning")
	}
	if c.ReplicationControl.Enabled {
		out = append(out, "replicationControl")
	}
	if c.BinlogArchive.Enabled {
		out = append(out, "binlogArchive")
	}
	return out
}

// ReadSecret lit un secret d'une ligne depuis un fichier protégé.
func ReadSecret(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("lecture du secret: %w", err)
	}
	return strings.TrimRight(string(b), "\r\n"), nil
}

func isLoopbackHTTP(u string) bool {
	return strings.HasPrefix(u, "http://127.0.0.1") || strings.HasPrefix(u, "http://localhost")
}

// Interval renvoie l'intervalle de collecte.
func (c *Config) Interval() time.Duration { return time.Duration(c.IntervalSeconds) * time.Second }

// MariaDBPassword lit le mot de passe depuis le fichier ou l'environnement.
func (c *Config) MariaDBPassword() (string, error) {
	if c.MariaDB.PasswordFile != "" {
		b, err := os.ReadFile(c.MariaDB.PasswordFile)
		if err != nil {
			return "", fmt.Errorf("lecture du fichier de mot de passe: %w", err)
		}
		return strings.TrimRight(string(b), "\r\n"), nil
	}
	if c.MariaDB.PasswordEnv != "" {
		return os.Getenv(c.MariaDB.PasswordEnv), nil
	}
	return "", nil
}

// AgentToken lit le jeton de l'agent.
func (c *Config) AgentToken() (string, error) {
	b, err := os.ReadFile(c.AgentTokenFile)
	if err != nil {
		return "", fmt.Errorf("lecture du jeton d'agent: %w", err)
	}
	t := strings.TrimSpace(string(b))
	if t == "" {
		return "", errors.New("jeton d'agent vide")
	}
	return t, nil
}
