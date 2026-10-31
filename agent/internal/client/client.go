// Package client envoie les rapports au Control Plane par HTTPS, avec TLS
// mutuel lorsqu'un certificat d'agent est configuré. L'agent initie toujours
// la connexion : aucun port entrant n'est ouvert sur le site.
package client

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/airm5500/prestige-replica-manager/agent/internal/config"
	"github.com/airm5500/prestige-replica-manager/agent/internal/report"
)

// ErrUnauthorized indique un jeton ou certificat révoqué : inutile de réessayer.
var ErrUnauthorized = errors.New("agent non autorisé par le Control Plane")

// Client envoie des lots de rapports.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// New construit un client à partir de la configuration.
func New(c *config.Config, token string) (*Client, error) {
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: c.TLS.ServerName, InsecureSkipVerify: c.TLS.InsecureSkipVerify} //nolint:gosec // option explicite de laboratoire
	if c.TLS.CAFile != "" {
		pem, err := os.ReadFile(c.TLS.CAFile)
		if err != nil {
			return nil, fmt.Errorf("lecture CA: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("CA invalide")
		}
		tlsCfg.RootCAs = pool
	}
	if c.TLS.CertFile != "" {
		cert, err := tls.LoadX509KeyPair(c.TLS.CertFile, c.TLS.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("certificat d'agent: %w", err)
		}
		tlsCfg.Certificates = []tls.Certificate{cert}
	}
	return &Client{
		baseURL: strings.TrimRight(c.ControlPlaneURL, "/"),
		token:   token,
		http: &http.Client{
			Timeout:   15 * time.Second,
			Transport: &http.Transport{TLSClientConfig: tlsCfg, Proxy: http.ProxyFromEnvironment},
		},
	}, nil
}

// Send envoie un lot de rapports, du plus ancien au plus récent.
func (c *Client) Send(ctx context.Context, batch []report.Report) error {
	body, err := json.Marshal(map[string]any{"reports": batch})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/v1/agent/reports", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("%w (HTTP %d)", ErrUnauthorized, resp.StatusCode)
	case resp.StatusCode >= 300:
		return fmt.Errorf("Control Plane HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return nil
}
