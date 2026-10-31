package router

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"
)

type fakeState struct {
	ro, repl, down bool
}

type fakeProber struct {
	mu sync.Mutex
	s  map[string]*fakeState
}

func (f *fakeProber) set(addr string, st fakeState) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.s[addr] = &st
}

func (f *fakeProber) Probe(_ context.Context, addr string) (bool, bool, int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := f.s[addr]
	if st == nil || st.down {
		return false, false, 0, io.ErrUnexpectedEOF
	}
	return st.ro, st.repl, 1, nil
}

// backend répond son nom à chaque ligne reçue.
func backend(t *testing.T, name string) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				sc := bufio.NewScanner(c)
				for sc.Scan() {
					c.Write([]byte(name + "\n"))
				}
				c.Close()
			}()
		}
	}()
	return ln.Addr().String()
}

func ask(t *testing.T, addr string) (string, net.Conn) {
	c, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	c.SetDeadline(time.Now().Add(time.Second))
	c.Write([]byte("?\n"))
	line, err := bufio.NewReader(c).ReadString('\n')
	if err != nil {
		return "", c
	}
	return line[:len(line)-1], c
}

func TestRoutesToSingleWritableAndFailsClosed(t *testing.T) {
	a, b := backend(t, "A"), backend(t, "B")
	fp := &fakeProber{s: map[string]*fakeState{}}
	fp.set(a, fakeState{})                     // primaire
	fp.set(b, fakeState{ro: true, repl: true}) // réplique
	cfg := Config{Enabled: true, Listen: "127.0.0.1:0", Targets: []string{a, b}, User: "u", ProbeIntervalMs: 200, FailuresBeforeDown: 3}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	r := newRouter(cfg, fp, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.Run(ctx)
	for r.Addr() == "" {
		time.Sleep(10 * time.Millisecond)
	}
	waitFor := func(want string) {
		deadline := time.Now().Add(3 * time.Second)
		for r.Snapshot().Current != want {
			if time.Now().After(deadline) {
				t.Fatalf("primaire attendu %q, obtenu %q (%s)", want, r.Snapshot().Current, r.Snapshot().Reason)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	waitFor(a)
	got, held := ask(t, r.Addr())
	if got != "A" {
		t.Fatalf("connexion routée vers %q, attendu A", got)
	}

	// Bascule : A en lecture seule, B pas encore promu → refus.
	fp.set(a, fakeState{ro: true})
	waitFor("")
	if got, c := ask(t, r.Addr()); got != "" {
		t.Fatalf("connexion acceptée sans primaire : %q", got)
	} else {
		c.Close()
	}
	// L'ancienne connexion vers A a été fermée.
	held.SetDeadline(time.Now().Add(time.Second))
	if _, err := held.Write([]byte("?\n")); err == nil {
		if _, err := bufio.NewReader(held).ReadString('\n'); err == nil {
			t.Fatal("la connexion vers l'ancien primaire est restée ouverte")
		}
	}

	// B promu.
	fp.set(b, fakeState{})
	waitFor(b)
	if got, c := ask(t, r.Addr()); got != "B" {
		t.Fatalf("après bascule : %q", got)
	} else {
		c.Close()
	}

	// A revient en écriture sans réplication : deux primaires → refus (split-brain).
	fp.set(a, fakeState{})
	waitFor("")
	if s := r.Snapshot(); s.SplitBrainEvents == 0 {
		t.Fatal("événement split-brain non compté")
	}
	// Une réplique accessible en écriture par erreur mais qui réplique n'est pas candidate.
	fp.set(a, fakeState{repl: true})
	waitFor(b)

	// Une sonde manquée isolée ne fait pas tomber le routage.
	fp.set(b, fakeState{down: true})
	time.Sleep(250 * time.Millisecond)
	fp.set(b, fakeState{})
	time.Sleep(250 * time.Millisecond)
	if r.Snapshot().Current != b {
		t.Fatal("routage perdu sur une seule sonde manquée")
	}
}
