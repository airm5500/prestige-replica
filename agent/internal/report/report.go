// Package report définit le contrat JSON envoyé par l'agent au Control Plane.
// Toute évolution doit rester rétro-compatible avec au moins une version
// d'agent précédente (spécification §16.9).
package report

import "time"

// SchemaVersion est incrémenté à chaque changement incompatible du contrat.
const SchemaVersion = 1

// Report est un instantané complet, collecté en lecture seule.
type Report struct {
	SchemaVersion int       `json:"schemaVersion"`
	AgentVersion  string    `json:"agentVersion"`
	CollectedAt   time.Time `json:"collectedAt"`
	Hostname      string    `json:"hostname"`
	OS            string    `json:"os"`
	Arch          string    `json:"arch"`

	// Capabilities : opérations activées localement (backup, restoreTest,
	// provisioning, replicationControl).
	Capabilities []string `json:"capabilities,omitempty"`

	Reachable bool      `json:"reachable"`
	Server    *Server   `json:"server,omitempty"`
	Binlogs   *Binlogs  `json:"binlogs,omitempty"`
	Channels  []Channel `json:"channels"`
	Disks     []Disk    `json:"disks"`
	// Errors contient les erreurs de collecte (jamais de mot de passe).
	Errors []string `json:"errors"`
}

// Server regroupe les variables et compteurs utiles à la supervision.
type Server struct {
	Version             string   `json:"version"`
	VersionComment      string   `json:"versionComment"`
	ServerID            int64    `json:"serverId"`
	ReadOnly            bool     `json:"readOnly"`
	LogBin              bool     `json:"logBin"`
	LogSlaveUpdates     bool     `json:"logSlaveUpdates"`
	BinlogFormat        string   `json:"binlogFormat"`
	BinlogRowImage      string   `json:"binlogRowImage"`
	GtidDomainID        int64    `json:"gtidDomainId"`
	GtidStrictMode      bool     `json:"gtidStrictMode"`
	GtidCurrentPos      string   `json:"gtidCurrentPos"`
	GtidBinlogPos       string   `json:"gtidBinlogPos"`
	GtidSlavePos        string   `json:"gtidSlavePos"`
	BinlogExpireSeconds int64    `json:"binlogExpireSeconds"`
	SyncBinlog          int64    `json:"syncBinlog"`
	InnodbFlushAtCommit int64    `json:"innodbFlushLogAtTrxCommit"`
	SemiSyncMasterOn    bool     `json:"semiSyncMasterEnabled"`
	SemiSyncSlaveOn     bool     `json:"semiSyncSlaveEnabled"`
	TimeZone            string   `json:"timeZone"`
	SystemTimeZone      string   `json:"systemTimeZone"`
	DataDir             string   `json:"dataDir"`
	HaveSSL             string   `json:"haveSsl"`
	UptimeSeconds       int64    `json:"uptimeSeconds"`
	ThreadsConnected    int64    `json:"threadsConnected"`
	MaxConnections      int64    `json:"maxConnections"`
	Questions           int64    `json:"questions"`
	BinlogBytesWritten  int64    `json:"binlogBytesWritten"`
	Databases           []string `json:"databases"`
}

// Binlogs résume les journaux binaires présents sur le serveur.
type Binlogs struct {
	Count      int    `json:"count"`
	TotalBytes int64  `json:"totalBytes"`
	First      string `json:"first"`
	Last       string `json:"last"`
}

// Channel correspond à une ligne de SHOW ALL SLAVES STATUS.
type Channel struct {
	ConnectionName           string `json:"connectionName"`
	MasterHost               string `json:"masterHost"`
	MasterPort               int64  `json:"masterPort"`
	MasterServerID           int64  `json:"masterServerId"`
	IORunning                string `json:"ioRunning"`
	SQLRunning               string `json:"sqlRunning"`
	SecondsBehindMaster      *int64 `json:"secondsBehindMaster"`
	SQLDelay                 int64  `json:"sqlDelay"`
	SQLRemainingDelay        *int64 `json:"sqlRemainingDelay"`
	LastIOErrno              int64  `json:"lastIoErrno"`
	LastIOError              string `json:"lastIoError"`
	LastSQLErrno             int64  `json:"lastSqlErrno"`
	LastSQLError             string `json:"lastSqlError"`
	UsingGtid                string `json:"usingGtid"`
	GtidIOPos                string `json:"gtidIoPos"`
	MasterLogFile            string `json:"masterLogFile"`
	ReadMasterLogPos         int64  `json:"readMasterLogPos"`
	RelayMasterLogFile       string `json:"relayMasterLogFile"`
	ExecMasterLogPos         int64  `json:"execMasterLogPos"`
	RelayLogSpace            int64  `json:"relayLogSpace"`
	SlaveSQLState            string `json:"slaveSqlRunningState"`
	MasterSSLAllowed         string `json:"masterSslAllowed"`
	ReplicateDoDB            string `json:"replicateDoDb"`
	ReplicateIgnoreDB        string `json:"replicateIgnoreDb"`
	ReplicateDoTable         string `json:"replicateDoTable"`
	ReplicateIgnoreTable     string `json:"replicateIgnoreTable"`
	ReplicateWildDoTable     string `json:"replicateWildDoTable"`
	ReplicateWildIgnoreTable string `json:"replicateWildIgnoreTable"`
	RetriedTransactions      int64  `json:"retriedTransactions"`
}

// Disk décrit l'espace d'un point de montage surveillé.
type Disk struct {
	Path       string `json:"path"`
	TotalBytes uint64 `json:"totalBytes"`
	FreeBytes  uint64 `json:"freeBytes"`
}
