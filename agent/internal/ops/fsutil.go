package ops

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// SafeJoin résout un chemin relatif reçu de la console et garantit qu'il
// reste sous la racine configurée localement (pas de traversée, pas de chemin
// absolu, pas de lien symbolique sortant).
func SafeJoin(root, rel string) (string, error) {
	if !relPathRe.MatchString(rel) {
		return "", fmt.Errorf("chemin relatif refusé : %q", rel)
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	p := filepath.Join(absRoot, filepath.FromSlash(rel))
	if !strings.HasPrefix(p, absRoot+string(filepath.Separator)) {
		return "", errors.New("chemin hors de la racine de stockage")
	}
	// Vérifie les composants existants (liens symboliques).
	if real, err := filepath.EvalSymlinks(filepath.Dir(p)); err == nil {
		realRoot, _ := filepath.EvalSymlinks(absRoot)
		if realRoot != "" && real != realRoot && !strings.HasPrefix(real, realRoot+string(filepath.Separator)) {
			return "", errors.New("lien symbolique sortant de la racine de stockage")
		}
	}
	return p, nil
}

// hashingWriter calcule l'empreinte et la taille de ce qui est écrit.
type hashingWriter struct {
	w io.Writer
	h hashState
	n int64
}

type hashState interface {
	io.Writer
	Sum([]byte) []byte
}

func newHashingWriter(w io.Writer) *hashingWriter { return &hashingWriter{w: w, h: sha256.New()} }

func (h *hashingWriter) Write(p []byte) (int, error) {
	n, err := h.w.Write(p)
	h.h.Write(p[:n])
	h.n += int64(n)
	return n, err
}

func (h *hashingWriter) Sum() string { return hex.EncodeToString(h.h.Sum(nil)) }

// FileSHA256 calcule l'empreinte d'un fichier.
func FileSHA256(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), n, err
}

// DirSize additionne la taille des fichiers d'un dossier.
func DirSize(dir string) (int64, error) {
	var total int64
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			if info, err := d.Info(); err == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total, err
}
