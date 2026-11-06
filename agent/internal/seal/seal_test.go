package seal

import (
	"bytes"
	"crypto/rand"
	"errors"
	"testing"
)

func roundTrip(t *testing.T, size int) {
	t.Helper()
	k, _ := NewKey()
	plain := make([]byte, size)
	rand.Read(plain)
	var enc, dec bytes.Buffer
	if err := Encrypt(&enc, bytes.NewReader(plain), k); err != nil {
		t.Fatal(err)
	}
	if err := Decrypt(&dec, bytes.NewReader(enc.Bytes()), k); err != nil {
		t.Fatalf("taille %d: %v", size, err)
	}
	if !bytes.Equal(plain, dec.Bytes()) {
		t.Fatalf("taille %d: contenu différent", size)
	}
}

func TestRoundTrip(t *testing.T) {
	for _, n := range []int{0, 1, chunkSize - 1, chunkSize, chunkSize + 1, 3*chunkSize + 17} {
		roundTrip(t, n)
	}
}

func encrypted(t *testing.T, k Key, size int) []byte {
	plain := make([]byte, size)
	rand.Read(plain)
	var enc bytes.Buffer
	if err := Encrypt(&enc, bytes.NewReader(plain), k); err != nil {
		t.Fatal(err)
	}
	return enc.Bytes()
}

func TestTamperingDetected(t *testing.T) {
	k, _ := NewKey()
	enc := encrypted(t, k, 2*chunkSize+10)
	flipped := append([]byte(nil), enc...)
	flipped[len(flipped)/2] ^= 1
	if err := Decrypt(&bytes.Buffer{}, bytes.NewReader(flipped), k); !errors.Is(err, ErrCorrupt) {
		t.Fatal("bit modifié non détecté")
	}
	// Troncature exactement à une frontière de bloc : le bloc final manque.
	cut := len(magic) + 8 + prefixLen + 4 + chunkSize + 16
	if err := Decrypt(&bytes.Buffer{}, bytes.NewReader(enc[:cut]), k); !errors.Is(err, ErrCorrupt) {
		t.Fatal("troncature non détectée")
	}
	if err := Decrypt(&bytes.Buffer{}, bytes.NewReader(append(enc, 0)), k); !errors.Is(err, ErrCorrupt) {
		t.Fatal("ajout non détecté")
	}
}

func TestWrongKey(t *testing.T) {
	k1, _ := NewKey()
	k2, _ := NewKey()
	enc := encrypted(t, k1, 100)
	if err := Decrypt(&bytes.Buffer{}, bytes.NewReader(enc), k2); !errors.Is(err, ErrCorrupt) {
		t.Fatal("mauvaise clé acceptée")
	}
}
