// Package router fournit une adresse stable à Prestige : il relaie les
// connexions TCP vers l'unique serveur MariaDB qui accepte les écritures.
//
// Le primaire est déterminé en interrogeant directement les serveurs, sans
// dépendre de la console : un candidat est joignable, read_only=OFF et ne
// réplique depuis personne. S'il n'y a pas exactement un candidat (bascule en
// cours, panne, ou deux primaires après un incident), le routeur refuse les
// nouvelles connexions : il préfère une interruption visible à des écritures
// sur deux serveurs divergents.
package router

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-sql-driver/mysql"
)

// Config du routeur.
type Config struct {
	Enabled bool `json:"enabled"`
	// Listen : adresse d'écoute utilisée par Prestige (ex. « 0.0.0.0:3306 »).
	Listen string `json:"listen"`
	// Targets : adresses hôte:port des serveurs MariaDB de la topologie.
	Targets []string `json:"targets"`
	// User / PasswordFile : compte de supervision (prm_monitor) pour les sondes.
	User         string `json:"user"`
	PasswordFile string `json:"passwordFile"`
	// ProbeIntervalMs : fréquence des sondes (défaut 1000 ms).
	ProbeIntervalMs int `json:"probeIntervalMs"`
	// FailuresBeforeDown : sondes manquées avant de considérer un serveur perdu (défaut 2).
	FailuresBeforeDown int `json:"failuresBeforeDown"`
}

// Validate complète les valeurs par défaut et vérifie la configuration.
func (c *Config) Validate() error {
	if !c.Enabled {
		return nil
	}
	if c.Listen == "" || len(c.Targets) == 0 || c.User == "" {
		return errors.New("router : listen, targets et user sont obligatoires")
	}
	for _, t := range c.Targets {
		if _, _, err := net.SplitHostPort(t); err != nil {
			return fmt.Errorf("router : cible invalide %q (hôte:port attendu)", t)
		}
	}
	if c.ProbeIntervalMs <= 0 {
		c.ProbeIntervalMs = 1000
	}
	if c.ProbeIntervalMs < 200 {
		c.ProbeIntervalMs = 200
	}
	if c.FailuresBeforeDown <= 0 {
		c.FailuresBeforeDown = 2
	}
	return nil
}

// TargetState est le dernier état connu d'un serveur.
type TargetState struct {
	Address     string `json:"address"`
	Reachable   bool   `json:"reachable"`
	ReadOnly    bool   `json:"readOnly"`
	Replicating bool   `json:"replicating"`
	ServerID    int64  `json:"serverId"`
	Error       string `json:"error,omitempty"`
	failures    int
}

// Status est publié dans les rapports de l'agent et sur la CLI.
type Status struct {
	Listen           string        `json:"listen"`
	Current          string        `json:"current"`
	Reason           string        `json:"reason"`
	Targets          []TargetState `json:"targets"`
	ActiveConns      int           `json:"activeConnections"`
	TotalConns       int64         `json:"totalConnections"`
	Refused          int64         `json:"refusedConnections"`
	LastChangeAt     time.Time     `json:"lastChangeAt"`
	SplitBrainEvents int64         `json:"splitBrainEvents"`
}

// Prober interroge un serveur ; remplaçable dans les tests.
type Prober interface {
	Probe(ctx context.Context, addr string) (readOnly, replicating bool, serverID int64, err error)
}

// Router relaie les connexions vers le primaire courant.
type Router struct {
	cfg    Config
	prober Prober
	log    *slog.Logger
	dial   func(ctx context.Context, addr string) (net.Conn, error)

	mu      sync.Mutex
	status  Status
	targets map[string]*TargetState
	conns   map[net.Conn]string // connexion cliente → serveur
	ln      net.Listener
}

// New crée un routeur. password est lu par l'appelant (fichier protégé).
func New(cfg Config, password string, log *slog.Logger) *Router {
	return newRouter(cfg, &sqlProber{user: cfg.User, password: password, dbs: map[string]*sql.DB{}}, log)
}

func newRouter(cfg Config, p Prober, log *slog.Logger) *Router {
	r := &Router{cfg: cfg, prober: p, log: log, targets: map[string]*TargetState{}, conns: map[net.Conn]string{}}
	d := &net.Dialer{Timeout: 3 * time.Second, KeepAlive: 30 * time.Second}
	r.dial = func(ctx context.Context, addr string) (net.Conn, error) { return d.DialContext(ctx, "tcp", addr) }
	for _, t := range cfg.Targets {
		r.targets[t] = &TargetState{Address: t}
	}
	r.status = Status{Listen: cfg.Listen, Reason: "démarrage : premières sondes en cours"}
	return r
}

// Run écoute et sonde jusqu'à l'annulation du contexte.
func (r *Router) Run(ctx context.Context) error {
	ln, err := net.Listen("tcp", r.cfg.Listen)
	if err != nil {
		return fmt.Errorf("routeur : écoute sur %s impossible : %w", r.cfg.Listen, err)
	}
	r.mu.Lock()
	r.ln = ln
	r.mu.Unlock()
	r.log.Info("routeur démarré", "écoute", r.cfg.Listen, "cibles", strings.Join(r.cfg.Targets, ", "))
	r.probeOnce(ctx)
	go r.probeLoop(ctx)
	go func() { <-ctx.Done(); ln.Close() }()
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				r.closeAll()
				return nil
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			time.Sleep(100 * time.Millisecond)
			continue
		}
		go r.handle(ctx, c)
	}
}

// Addr renvoie l'adresse effective d'écoute (utile si le port est 0).
func (r *Router) Addr() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ln == nil {
		return ""
	}
	return r.ln.Addr().String()
}

// Snapshot renvoie une copie de l'état courant.
func (r *Router) Snapshot() Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.status
	s.ActiveConns = len(r.conns)
	s.Targets = make([]TargetState, 0, len(r.targets))
	for _, t := range r.cfg.Targets {
		s.Targets = append(s.Targets, *r.targets[t])
	}
	return s
}

func (r *Router) probeLoop(ctx context.Context) {
	t := time.NewTicker(time.Duration(r.cfg.ProbeIntervalMs) * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.probeOnce(ctx)
		}
	}
}

func (r *Router) probeOnce(ctx context.Context) {
	timeout := time.Duration(r.cfg.ProbeIntervalMs) * time.Millisecond
	if timeout < 800*time.Millisecond {
		timeout = 800 * time.Millisecond
	}
	type res struct {
		addr     string
		ro, repl bool
		id       int64
		err      error
	}
	out := make(chan res, len(r.cfg.Targets))
	for _, a := range r.cfg.Targets {
		go func(a string) {
			pctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			ro, repl, id, err := r.prober.Probe(pctx, a)
			out <- res{a, ro, repl, id, err}
		}(a)
	}
	results := make([]res, 0, len(r.cfg.Targets))
	for range r.cfg.Targets {
		results = append(results, <-out)
	}
	r.apply(func(set func(addr string, ro, repl bool, id int64, err error)) {
		for _, x := range results {
			set(x.addr, x.ro, x.repl, x.id, x.err)
		}
	})
}

// apply met à jour l'état des cibles puis choisit le primaire.
func (r *Router) apply(update func(set func(addr string, ro, repl bool, id int64, err error))) {
	r.mu.Lock()
	update(func(addr string, ro, repl bool, id int64, err error) {
		t := r.targets[addr]
		if err != nil {
			t.failures++
			t.Error = err.Error()
			if t.failures >= r.cfg.FailuresBeforeDown {
				t.Reachable = false
			}
			return
		}
		t.failures, t.Error = 0, ""
		t.Reachable, t.ReadOnly, t.Replicating, t.ServerID = true, ro, repl, id
	})
	var candidates []string
	for _, a := range r.cfg.Targets {
		t := r.targets[a]
		if t.Reachable && !t.ReadOnly && !t.Replicating {
			candidates = append(candidates, a)
		}
	}
	sort.Strings(candidates)
	next, reason := "", ""
	switch len(candidates) {
	case 1:
		next, reason = candidates[0], "unique serveur accessible en écriture"
	case 0:
		reason = "aucun serveur accessible en écriture (bascule en cours ou primaire perdu) : connexions refusées"
	default:
		reason = "plusieurs serveurs acceptent les écritures (" + strings.Join(candidates, ", ") + ") : connexions refusées pour éviter deux historiques divergents"
	}
	prev := r.status.Current
	r.status.Reason = reason
	var toClose []net.Conn
	if next != prev {
		if len(candidates) > 1 && prev != "" {
			r.status.SplitBrainEvents++
		}
		r.status.Current = next
		r.status.LastChangeAt = time.Now().UTC()
		// Toute connexion vers un autre serveur que le primaire courant est
		// fermée : Prestige se reconnecte et arrive sur la bonne base.
		for c, b := range r.conns {
			if b != next {
				toClose = append(toClose, c)
			}
		}
	}
	r.mu.Unlock()
	if next != prev {
		if next == "" {
			r.log.Warn("routeur : aucun primaire", "raison", reason, "précédent", prev)
		} else {
			r.log.Info("routeur : nouveau primaire", "primaire", next, "précédent", prev)
		}
	}
	for _, c := range toClose {
		c.Close()
	}
}

func (r *Router) handle(ctx context.Context, c net.Conn) {
	r.mu.Lock()
	backend := r.status.Current
	r.status.TotalConns++
	if backend == "" {
		r.status.Refused++
		r.mu.Unlock()
		c.Close()
		return
	}
	r.mu.Unlock()
	b, err := r.dial(ctx, backend)
	if err != nil {
		r.mu.Lock()
		r.status.Refused++
		r.mu.Unlock()
		c.Close()
		return
	}
	r.mu.Lock()
	if r.status.Current != backend { // bascule pendant l'ouverture
		r.status.Refused++
		r.mu.Unlock()
		c.Close()
		b.Close()
		return
	}
	r.conns[c] = backend
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.conns, c)
		r.mu.Unlock()
	}()
	done := make(chan struct{}, 2)
	go func() { io.Copy(b, c); b.Close(); done <- struct{}{} }()
	go func() { io.Copy(c, b); c.Close(); done <- struct{}{} }()
	<-done
	c.Close()
	b.Close()
	<-done
}

func (r *Router) closeAll() {
	r.mu.Lock()
	cs := make([]net.Conn, 0, len(r.conns))
	for c := range r.conns {
		cs = append(cs, c)
	}
	r.mu.Unlock()
	for _, c := range cs {
		c.Close()
	}
}

// sqlProber sonde avec le compte de supervision.
type sqlProber struct {
	user, password string
	mu             sync.Mutex
	dbs            map[string]*sql.DB
}

func (p *sqlProber) db(addr string) (*sql.DB, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if db, ok := p.dbs[addr]; ok {
		return db, nil
	}
	mc := mysql.NewConfig()
	mc.User, mc.Passwd, mc.Net, mc.Addr = p.user, p.password, "tcp", addr
	mc.Timeout, mc.ReadTimeout, mc.WriteTimeout = 2*time.Second, 2*time.Second, 2*time.Second
	db, err := sql.Open("mysql", mc.FormatDSN())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(5 * time.Minute)
	p.dbs[addr] = db
	return db, nil
}

func (p *sqlProber) Probe(ctx context.Context, addr string) (bool, bool, int64, error) {
	db, err := p.db(addr)
	if err != nil {
		return false, false, 0, err
	}
	var ro, id int64
	if err := db.QueryRowContext(ctx, "SELECT @@read_only, @@server_id").Scan(&ro, &id); err != nil {
		return false, false, 0, err
	}
	rows, err := db.QueryContext(ctx, "SHOW SLAVE STATUS")
	if err != nil {
		return false, false, 0, err
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	replicating := false
	if rows.Next() {
		vals := make([]sql.NullString, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return false, false, 0, err
		}
		for i, c := range cols {
			// Un canal configuré dont l'un des threads tourne (ou tente de se
			// connecter) désigne une réplique, même si read_only a été oublié.
			if (c == "Slave_IO_Running" || c == "Slave_SQL_Running") && (vals[i].String == "Yes" || vals[i].String == "Connecting") {
				replicating = true
			}
		}
	}
	return ro == 1, replicating, id, rows.Err()
}
