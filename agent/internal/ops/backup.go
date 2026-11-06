package ops

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	"github.com/airm5500/prestige-replica-manager/agent/internal/collector"
	"github.com/airm5500/prestige-replica-manager/agent/internal/config"
	"github.com/airm5500/prestige-replica-manager/agent/internal/seal"
)

const (
	artifactName = "backup.xb.prmseal"
	manifestName = "manifest.json"
)

// BackupParams : dossier relatif sous backup.root, attribué par la console
// (tenants/<uuid>/sites/<uuid>/backups/<uuid>).
type BackupParams struct {
	BackupID    string `json:"backupId"`
	RelativeDir string `json:"relativeDir"`
}

// Manifest décrit une sauvegarde. Il est signé (HMAC-SHA256 avec une clé
// dérivée de la clé de chiffrement) pour détecter toute modification.
type Manifest struct {
	BackupID       string    `json:"backupId"`
	Kind           string    `json:"kind"`
	Hostname       string    `json:"hostname"`
	ServerVersion  string    `json:"serverVersion"`
	ToolVersion    string    `json:"toolVersion"`
	AgentVersion   string    `json:"agentVersion"`
	StartedAt      time.Time `json:"startedAt"`
	FinishedAt     time.Time `json:"finishedAt"`
	Artifact       string    `json:"artifact"`
	SizeBytes      int64     `json:"sizeBytes"`
	SHA256         string    `json:"sha256"`
	Encryption     string    `json:"encryption"`
	KeyID          string    `json:"keyId"`
	GTID           string    `json:"gtid"`
	BinlogFile     string    `json:"binlogFile"`
	BinlogPosition int64     `json:"binlogPosition"`
	Signature      string    `json:"signature,omitempty"`
}

func manifestMAC(m Manifest, k seal.Key) string {
	m.Signature = ""
	b, _ := json.Marshal(m)
	mk := sha256.Sum256(append([]byte("prm-manifest:"), k[:]...))
	mac := hmac.New(sha256.New, mk[:])
	mac.Write(b)
	return hex.EncodeToString(mac.Sum(nil))
}

// ReadManifest lit et authentifie un manifeste.
func ReadManifest(dir string, k seal.Key) (Manifest, error) {
	var m Manifest
	b, err := os.ReadFile(filepath.Join(dir, manifestName))
	if err != nil {
		return m, fmt.Errorf("manifeste illisible: %w", err)
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return m, fmt.Errorf("manifeste invalide: %w", err)
	}
	if !hmac.Equal([]byte(m.Signature), []byte(manifestMAC(m, k))) {
		return m, errors.New("signature du manifeste invalide : sauvegarde modifiée ou clé différente")
	}
	return m, nil
}

// mariadb-backup 10.x : « MySQL binlog position: filename 'x', position 'y', GTID of the last change 'z' »
var binlogPosRe = regexp.MustCompile(`binlog position: filename '([^']*)', position '(\d+)'(?:, GTID of the last change '([^']*)')?`)

// Backup produit une sauvegarde physique cohérente, chiffrée en flux, sans
// arrêt de l'exploitation (mariadb-backup --stream=xbstream).
func (r *Runner) Backup(ctx context.Context, p BackupParams, rep Reporter) (map[string]any, error) {
	dir, err := SafeJoin(r.Cfg.Backup.Root, p.RelativeDir)
	if err != nil {
		return nil, err
	}
	key, err := seal.LoadKey(r.Cfg.Backup.EncryptionKeyFile)
	if err != nil {
		return nil, err
	}
	tool, err := r.FindTool("mariadb-backup")
	if err != nil {
		return nil, err
	}
	toolVersion, _ := ToolVersion(ctx, tool)
	col := &collector.Collector{DB: r.Monitor, AgentVersion: r.Version, Timeout: 10 * time.Second}
	snap := col.Collect(ctx)
	if snap.Server == nil {
		return nil, errors.New("MariaDB injoignable : sauvegarde impossible")
	}
	if toolVersion != "" && majorMinor(toolVersion) != majorMinor(snap.Server.Version) {
		return nil, fmt.Errorf("mariadb-backup %s incompatible avec le serveur %s", toolVersion, snap.Server.Version)
	}
	if _, err := os.Stat(filepath.Join(dir, manifestName)); err == nil {
		return nil, errors.New("une sauvegarde existe déjà dans ce dossier : refus d'écraser")
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	pw, err := config.ReadSecret(r.Cfg.Backup.PasswordFile)
	if err != nil {
		return nil, err
	}
	cnf, cleanup, err := defaultsFile(r.Cfg.Backup.User, pw, r.Cfg.MariaDB.Address)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	work, err := os.MkdirTemp("", "prm-backup-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(work)

	partial := filepath.Join(dir, artifactName+".partial")
	out, err := os.OpenFile(partial, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
	if err != nil {
		return nil, err
	}
	started := time.Now().UTC()
	rep.Step(fmt.Sprintf("sauvegarde physique en cours (%s, %d threads)", filepath.Base(tool), r.Cfg.Backup.Parallel))

	hw := newHashingWriter(out)
	pr, pw2 := io.Pipe()
	encErr := make(chan error, 1)
	go func() {
		err := seal.Encrypt(hw, pr, key)
		pr.CloseWithError(err)
		encErr <- err
	}()
	stderr, runErr := run(ctx, pw2, nil, tool, "--defaults-extra-file="+cnf, "--backup", "--stream=xbstream",
		"--target-dir="+work, "--parallel="+strconv.Itoa(r.Cfg.Backup.Parallel))
	pw2.CloseWithError(runErr)
	if e := <-encErr; runErr == nil && e != nil {
		runErr = fmt.Errorf("chiffrement: %w", e)
	}
	if cerr := out.Close(); runErr == nil && cerr != nil {
		runErr = cerr
	}
	if runErr != nil {
		os.Remove(partial)
		return map[string]any{"log": stderr}, runErr
	}
	final := filepath.Join(dir, artifactName)
	if err := os.Rename(partial, final); err != nil {
		return nil, err
	}

	m := Manifest{
		BackupID: p.BackupID, Kind: "FULL", Hostname: snap.Hostname, ServerVersion: snap.Server.Version,
		ToolVersion: toolVersion, AgentVersion: r.Version, StartedAt: started, FinishedAt: time.Now().UTC(),
		Artifact: artifactName, SizeBytes: hw.n, SHA256: hw.Sum(), Encryption: "AES-256-GCM/STREAM-1MiB", KeyID: key.ID(),
	}
	if mm := binlogPosRe.FindStringSubmatch(stderr); mm != nil {
		m.BinlogFile = mm[1]
		m.BinlogPosition, _ = strconv.ParseInt(mm[2], 10, 64)
		m.GTID = mm[3]
	}
	if m.GTID == "" && snap.Server.LogBin {
		rep.Warn("position GTID non trouvée dans la sortie de mariadb-backup")
	}
	m.Signature = manifestMAC(m, key)
	mb, _ := json.MarshalIndent(m, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, manifestName), mb, 0o640); err != nil {
		return nil, err
	}
	rep.Step(fmt.Sprintf("sauvegarde terminée : %d Mo en %s", m.SizeBytes>>20, m.FinishedAt.Sub(m.StartedAt).Round(time.Second)))
	return map[string]any{"manifest": m, "durationSeconds": int(m.FinishedAt.Sub(m.StartedAt).Seconds())}, nil
}

// PruneParams : sauvegardes expirées selon la politique de rétention.
type PruneParams struct {
	RelativeDirs []string `json:"relativeDirs"`
}

// Prune supprime les sauvegardes expirées. Seuls les dossiers contenant un
// manifeste authentique sont supprimés : jamais un chemin arbitraire.
func (r *Runner) Prune(p PruneParams, rep Reporter) (map[string]any, error) {
	key, err := seal.LoadKey(r.Cfg.Backup.EncryptionKeyFile)
	if err != nil {
		return nil, err
	}
	var deleted, skipped []string
	for _, rel := range p.RelativeDirs {
		dir, err := SafeJoin(r.Cfg.Backup.Root, rel)
		if err != nil {
			skipped = append(skipped, rel)
			continue
		}
		if _, err := ReadManifest(dir, key); err != nil {
			rep.Warn(fmt.Sprintf("%s ignoré : %v", rel, err))
			skipped = append(skipped, rel)
			continue
		}
		if err := os.RemoveAll(dir); err != nil {
			rep.Warn(fmt.Sprintf("%s : %v", rel, err))
			skipped = append(skipped, rel)
			continue
		}
		deleted = append(deleted, rel)
	}
	return map[string]any{"deleted": deleted, "skipped": skipped}, nil
}

// openArtifact vérifie l'empreinte et le manifeste d'une sauvegarde.
func (r *Runner) openArtifact(rel, expectedSHA string, rep Reporter) (string, Manifest, seal.Key, error) {
	var m Manifest
	key, err := seal.LoadKey(r.Cfg.Backup.EncryptionKeyFile)
	if err != nil {
		return "", m, key, err
	}
	dir, err := SafeJoin(r.Cfg.Backup.Root, rel)
	if err != nil {
		return "", m, key, err
	}
	m, err = ReadManifest(dir, key)
	if err != nil {
		return "", m, key, err
	}
	if expectedSHA != "" && (!sha256Re.MatchString(expectedSHA) || expectedSHA != m.SHA256) {
		return "", m, key, errors.New("l'empreinte du catalogue ne correspond pas au manifeste")
	}
	rep.Step("vérification de l'empreinte SHA-256")
	path := filepath.Join(dir, m.Artifact)
	sum, size, err := FileSHA256(path)
	if err != nil {
		return "", m, key, err
	}
	if sum != m.SHA256 || size != m.SizeBytes {
		return "", m, key, fmt.Errorf("empreinte ou taille différente du manifeste (fichier altéré ou incomplet)")
	}
	return path, m, key, nil
}

// extract déchiffre et décompresse l'archive dans dest puis prépare les fichiers.
func (r *Runner) extract(ctx context.Context, artifact string, key seal.Key, dest string, rep Reporter) error {
	mbstream, err := r.FindTool("mbstream")
	if err != nil {
		return err
	}
	tool, err := r.FindTool("mariadb-backup")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dest, 0o750); err != nil {
		return err
	}
	f, err := os.Open(artifact)
	if err != nil {
		return err
	}
	defer f.Close()
	rep.Step("déchiffrement et extraction")
	pr, pw := io.Pipe()
	decErr := make(chan error, 1)
	go func() {
		err := seal.Decrypt(pw, f, key)
		pw.CloseWithError(err)
		decErr <- err
	}()
	_, runErr := run(ctx, nil, pr, mbstream, "-x", "-C", dest)
	pr.Close()
	if e := <-decErr; e != nil {
		return fmt.Errorf("déchiffrement: %w", e)
	}
	if runErr != nil {
		return runErr
	}
	rep.Step("préparation (application du journal redo)")
	if _, err := run(ctx, nil, nil, tool, "--prepare", "--target-dir="+dest); err != nil {
		return err
	}
	return nil
}
