// Package buffer fournit une file bornée de rapports, utilisée lorsque le
// Control Plane est injoignable (spécification §19.1). Lorsque la file est
// pleine, les rapports les plus anciens sont abandonnés et comptés.
package buffer

import "sync"

// Ring est une file FIFO bornée et sûre en concurrence.
type Ring[T any] struct {
	mu      sync.Mutex
	items   []T
	max     int
	dropped uint64
}

// New crée une file de capacité max (>= 1).
func New[T any](max int) *Ring[T] {
	if max < 1 {
		max = 1
	}
	return &Ring[T]{max: max}
}

// Push ajoute un élément, en abandonnant le plus ancien si nécessaire.
func (r *Ring[T]) Push(v T) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.items) >= r.max {
		r.items = r.items[1:]
		r.dropped++
	}
	r.items = append(r.items, v)
}

// Peek renvoie jusqu'à n éléments les plus anciens sans les retirer.
func (r *Ring[T]) Peek(n int) []T {
	r.mu.Lock()
	defer r.mu.Unlock()
	if n > len(r.items) {
		n = len(r.items)
	}
	out := make([]T, n)
	copy(out, r.items[:n])
	return out
}

// Drop retire les n éléments les plus anciens (après envoi réussi).
func (r *Ring[T]) Drop(n int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if n > len(r.items) {
		n = len(r.items)
	}
	r.items = r.items[n:]
}

// Len renvoie le nombre d'éléments en attente.
func (r *Ring[T]) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.items)
}

// Dropped renvoie le nombre d'éléments abandonnés faute de place.
func (r *Ring[T]) Dropped() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.dropped
}
