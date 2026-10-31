// Package config charge la configuration de l'agent. Les secrets (mot de
// passe MariaDB, jeton d'agent) ne sont jamais écrits dans le fichier de
// configuration : ils sont lus depuis un fichier protégé ou une variable
// d'environnement.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
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
	// BufferSize : nombre maximal de rapports conservés hors connexion.
	BufferSize int `json:"bufferSize"`
	// ExtraDiskPaths : points de montage supplémentaires (binlogs, sauvegardes).
	ExtraDiskPaths []string `json:"extraDiskPaths"`
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
	c.applyDefaults()
	return &c, c.Validate()
}

func (c *Config) applyDefaults() {
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
	return nil
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
