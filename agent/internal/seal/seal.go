// Package seal chiffre les sauvegardes en flux avant qu'elles ne quittent le
// serveur MariaDB (spécification §6 « chiffrement avant transfert »).
//
// Format : en-tête "PRMSEAL1" + identifiant de clé (8 octets) + préfixe de
// nonce aléatoire (7 octets), puis des blocs AES-256-GCM de 1 Mio au plus,
// chacun précédé de sa longueur (uint32). Le nonce de chaque bloc est
// préfixe || compteur (uint32) || drapeau de fin, ce qui empêche la
// réorganisation, la suppression ou la troncature des blocs (construction
// STREAM).
package seal

import (
	"bufio"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

const (
	magic     = "PRMSEAL1"
	chunkSize = 1 << 20
	prefixLen = 7
)

// ErrCorrupt signale un fichier altéré, tronqué ou chiffré avec une autre clé.
var ErrCorrupt = errors.New("sauvegarde chiffrée altérée, tronquée ou clé incorrecte")

// Key est une clé AES-256.
type Key [32]byte

// ID renvoie un identifiant public de la clé (jamais la clé elle-même).
func (k Key) ID() string {
	h := sha256.Sum256(append([]byte("prm-key-id:"), k[:]...))
	return hex.EncodeToString(h[:8])
}

// NewKey génère une clé aléatoire.
func NewKey() (Key, error) {
	var k Key
	_, err := rand.Read(k[:])
	return k, err
}

// LoadKey lit une clé hexadécimale (64 caractères) depuis un fichier.
func LoadKey(path string) (Key, error) {
	var k Key
	b, err := os.ReadFile(path)
	if err != nil {
		return k, fmt.Errorf("lecture de la clé: %w", err)
	}
	raw, err := hex.DecodeString(strings.TrimSpace(string(b)))
	if err != nil || len(raw) != 32 {
		return k, errors.New("clé invalide : 64 caractères hexadécimaux attendus")
	}
	copy(k[:], raw)
	return k, nil
}

func nonce(prefix []byte, counter uint32, final bool) []byte {
	n := make([]byte, 12)
	copy(n, prefix)
	binary.BigEndian.PutUint32(n[prefixLen:], counter)
	if final {
		n[11] = 1
	}
	return n
}

func gcm(k Key) (cipher.AEAD, error) {
	b, err := aes.NewCipher(k[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(b)
}

// Encrypt lit r jusqu'à EOF et écrit le flux chiffré dans w.
func Encrypt(w io.Writer, r io.Reader, k Key) error {
	aead, err := gcm(k)
	if err != nil {
		return err
	}
	prefix := make([]byte, prefixLen)
	if _, err := rand.Read(prefix); err != nil {
		return err
	}
	keyID, _ := hex.DecodeString(k.ID())
	header := append(append([]byte(magic), keyID...), prefix...)
	if _, err := w.Write(header); err != nil {
		return err
	}
	br := bufio.NewReaderSize(r, chunkSize)
	cur := make([]byte, chunkSize)
	next := make([]byte, chunkSize)
	n, err := io.ReadFull(br, cur)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return err
	}
	var counter uint32
	for {
		// Lecture anticipée pour savoir si ce bloc est le dernier.
		m, rerr := io.ReadFull(br, next)
		if rerr != nil && rerr != io.ErrUnexpectedEOF && rerr != io.EOF {
			return rerr
		}
		final := m == 0
		ct := aead.Seal(nil, nonce(prefix, counter, final), cur[:n], header)
		var lenBuf [4]byte
		binary.BigEndian.PutUint32(lenBuf[:], uint32(len(ct)))
		if _, err := w.Write(lenBuf[:]); err != nil {
			return err
		}
		if _, err := w.Write(ct); err != nil {
			return err
		}
		if final {
			return nil
		}
		if counter == ^uint32(0) {
			return errors.New("flux trop volumineux")
		}
		counter++
		cur, next = next, cur
		n = m
	}
}

// Decrypt vérifie et déchiffre le flux. Toute altération renvoie ErrCorrupt.
// Les données écrites avant une erreur ne doivent pas être utilisées.
func Decrypt(w io.Writer, r io.Reader, k Key) error {
	aead, err := gcm(k)
	if err != nil {
		return err
	}
	header := make([]byte, len(magic)+8+prefixLen)
	if _, err := io.ReadFull(r, header); err != nil || string(header[:len(magic)]) != magic {
		return ErrCorrupt
	}
	if hex.EncodeToString(header[len(magic):len(magic)+8]) != k.ID() {
		return fmt.Errorf("%w (identifiant de clé %s attendu)", ErrCorrupt, hex.EncodeToString(header[len(magic):len(magic)+8]))
	}
	prefix := header[len(magic)+8:]
	br := bufio.NewReaderSize(r, chunkSize+64)
	var counter uint32
	buf := make([]byte, chunkSize+aead.Overhead())
	for {
		var lenBuf [4]byte
		if _, err := io.ReadFull(br, lenBuf[:]); err != nil {
			return ErrCorrupt // fin de flux sans bloc final : troncature
		}
		l := binary.BigEndian.Uint32(lenBuf[:])
		if int(l) > len(buf) || int(l) < aead.Overhead() {
			return ErrCorrupt
		}
		if _, err := io.ReadFull(br, buf[:l]); err != nil {
			return ErrCorrupt
		}
		// On essaie d'abord « non final », puis « final ».
		pt, err := aead.Open(nil, nonce(prefix, counter, false), buf[:l], header)
		final := false
		if err != nil {
			pt, err = aead.Open(nil, nonce(prefix, counter, true), buf[:l], header)
			if err != nil {
				return ErrCorrupt
			}
			final = true
		}
		if _, err := w.Write(pt); err != nil {
			return err
		}
		if final {
			if _, err := br.ReadByte(); err != io.EOF {
				return ErrCorrupt // données après le bloc final
			}
			return nil
		}
		counter++
	}
}
