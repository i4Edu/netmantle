// Package notify dispatches notifications to user-configured channels.
//
// Phase 2 ships three channel kinds:
//   - webhook : POST application/json to an arbitrary URL
//   - slack   : webhook with a Slack-compatible payload
//   - email   : SMTP (plain or TLS), no attachments
//
// Channel configuration is stored as JSON in the notification_channels
// table. Secrets (SMTP passwords) live encrypted in the JSON via the
// envelope sealer, so dumping the DB row never reveals plaintext.
package notify

import (
	"bytes"
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/smtp"
	"strconv"
	"sync"
	"time"

	"github.com/i4Edu/netmantle/internal/crypto"
)

// Event is the payload sent through a channel.
type Event struct {
	Kind      string         `json:"kind"`
	Subject   string         `json:"subject"`
	Body      string         `json:"body"`
	Fields    map[string]any `json:"fields,omitempty"`
	Timestamp time.Time      `json:"timestamp"`
}

// Channel is one configured destination.
type Channel struct {
	ID        int64           `json:"id"`
	TenantID  int64           `json:"tenant_id"`
	Name      string          `json:"name"`
	Kind      string          `json:"kind"`
	Config    json.RawMessage `json:"config"`
	CreatedAt time.Time       `json:"created_at"`
}

// WebhookConfig is the JSON inside a webhook/slack channel row.
// The URL is stored in url_envelope (sealed) when first saved; the
// plaintext "url" field is removed before persistence. This closes
// threat-model gap T10: webhook / Slack tokens are now envelope-encrypted.
type WebhookConfig struct {
	URL         string `json:"url,omitempty"`
	URLEnvelope string `json:"url_envelope,omitempty"`
}

// EmailConfig is the JSON inside an email channel row. Password is stored
// as a sealed envelope so it can be safely round-tripped through the DB.
type EmailConfig struct {
	Host             string `json:"host"`
	Port             int    `json:"port"`
	Username         string `json:"username"`
	PasswordEnvelope string `json:"password_envelope"`
	From             string `json:"from"`
	To               string `json:"to"`
	StartTLS         bool   `json:"starttls"`
}

// Service persists channels/rules and dispatches events.
type Service struct {
	DB     *sql.DB
	Sealer *crypto.Sealer
	HTTP   *http.Client
	Logger *slog.Logger

	dialSMTP func(addr string) (smtpClient, error) // overridable for tests
}

// New constructs a Service.
func New(db *sql.DB, s *crypto.Sealer, log *slog.Logger) *Service {
	return &Service{
		DB:     db,
		Sealer: s,
		HTTP:   &http.Client{Timeout: 10 * time.Second},
		Logger: log,
	}
}

// CreateChannel stores a channel. For email channels with a plaintext
// "password" field in the supplied config JSON, the password is moved to
// "password_envelope" and the plaintext field removed.
func (s *Service) CreateChannel(ctx context.Context, tenantID int64, name, kind string, cfg json.RawMessage) (Channel, error) {
	if name == "" || kind == "" {
		return Channel{}, errors.New("notify: name and kind required")
	}
	switch kind {
	case "webhook", "slack":
		var raw map[string]any
		if err := json.Unmarshal(cfg, &raw); err != nil {
			return Channel{}, fmt.Errorf("notify: parse webhook config: %w", err)
		}
		// Accept either a plain "url" or an already-sealed "url_envelope".
		// If a plaintext "url" is supplied, seal it and remove the field
		// (closes threat-model gap T10).
		if urlVal, ok := raw["url"].(string); ok && urlVal != "" {
			env, err := s.Sealer.Seal([]byte(urlVal))
			if err != nil {
				return Channel{}, fmt.Errorf("notify: seal webhook url: %w", err)
			}
			raw["url_envelope"] = env
			delete(raw, "url")
			cfg, _ = json.Marshal(raw)
		}
		if raw["url_envelope"] == nil {
			return Channel{}, errors.New("notify: webhook/slack config requires url")
		}
	case "email":
		// Allow an optional plaintext "password" — seal it.
		var raw map[string]any
		if err := json.Unmarshal(cfg, &raw); err != nil {
			return Channel{}, fmt.Errorf("notify: parse email config: %w", err)
		}
		if pw, ok := raw["password"].(string); ok && pw != "" {
			env, err := s.Sealer.Seal([]byte(pw))
			if err != nil {
				return Channel{}, err
			}
			raw["password_envelope"] = env
			delete(raw, "password")
			cfg, _ = json.Marshal(raw)
		}
	default:
		return Channel{}, fmt.Errorf("notify: unknown kind %q", kind)
	}
	now := time.Now().UTC()
	res, err := s.DB.ExecContext(ctx,
		`INSERT INTO notification_channels(tenant_id, name, kind, config, created_at) VALUES(?, ?, ?, ?, ?)`,
		tenantID, name, kind, string(cfg), now.Format(time.RFC3339))
	if err != nil {
		return Channel{}, err
	}
	id, _ := res.LastInsertId()
	return Channel{ID: id, TenantID: tenantID, Name: name, Kind: kind, Config: cfg, CreatedAt: now}, nil
}

// ListChannels returns all channels for a tenant.
func (s *Service) ListChannels(ctx context.Context, tenantID int64) ([]Channel, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT id, tenant_id, name, kind, config, created_at FROM notification_channels WHERE tenant_id=? ORDER BY name`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Channel
	for rows.Next() {
		var (
			c   Channel
			cfg string
			ts  string
		)
		if err := rows.Scan(&c.ID, &c.TenantID, &c.Name, &c.Kind, &cfg, &ts); err != nil {
			return nil, err
		}
		c.Config = json.RawMessage(cfg)
		c.CreatedAt, _ = time.Parse(time.RFC3339, ts)
		out = append(out, c)
	}
	return out, rows.Err()
}

// DeleteChannel removes a channel.
func (s *Service) DeleteChannel(ctx context.Context, tenantID, id int64) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM notification_channels WHERE tenant_id=? AND id=?`, tenantID, id)
	return err
}

// CreateRule stores a notification rule routing an event type to a channel.
func (s *Service) CreateRule(ctx context.Context, tenantID int64, name, eventType string, channelID int64) error {
	_, err := s.DB.ExecContext(ctx,
		`INSERT INTO notification_rules(tenant_id, name, event_type, channel_id, created_at) VALUES(?, ?, ?, ?, ?)`,
		tenantID, name, eventType, channelID, time.Now().UTC().Format(time.RFC3339))
	return err
}

// ListRules returns all notification rules for a tenant.
func (s *Service) ListRules(ctx context.Context, tenantID int64) ([]map[string]any, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT id, name, event_type, channel_id, created_at FROM notification_rules WHERE tenant_id=? ORDER BY id`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var (
			id, ch                  int64
			name, evType, createdAt string
		)
		if err := rows.Scan(&id, &name, &evType, &ch, &createdAt); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{
			"id": id, "name": name, "event_type": evType, "channel_id": ch, "created_at": createdAt,
		})
	}
	return out, rows.Err()
}

// Dispatch sends an event to every channel routed by a matching rule for
// the tenant. Errors per channel are logged but do not abort the loop.
func (s *Service) Dispatch(ctx context.Context, tenantID int64, ev Event) {
	rows, err := s.DB.QueryContext(ctx, `
        SELECT c.id, c.kind, c.config FROM notification_channels c
        JOIN notification_rules r ON r.channel_id = c.id
        WHERE c.tenant_id = ? AND r.tenant_id = ? AND r.event_type = ?`,
		tenantID, tenantID, ev.Kind)
	if err != nil {
		s.Logger.Warn("notify: list rules failed", "err", err)
		return
	}
	defer rows.Close()
	type target struct {
		id   int64
		kind string
		cfg  string
	}
	var targets []target
	for rows.Next() {
		var t target
		if err := rows.Scan(&t.id, &t.kind, &t.cfg); err != nil {
			continue
		}
		targets = append(targets, t)
	}
	rows.Close()

	var wg sync.WaitGroup
	for _, t := range targets {
		t := t
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.sendOne(ctx, t.kind, []byte(t.cfg), ev); err != nil {
				s.Logger.Warn("notify: send failed", "channel_id", t.id, "kind", t.kind, "err", err)
			}
		}()
	}
	wg.Wait()
}

func (s *Service) sendOne(ctx context.Context, kind string, cfg []byte, ev Event) error {
	switch kind {
	case "webhook":
		url, err := s.unsealWebhookURL(cfg)
		if err != nil {
			return err
		}
		body, _ := json.Marshal(ev)
		return s.postJSON(ctx, url, body)
	case "slack":
		url, err := s.unsealWebhookURL(cfg)
		if err != nil {
			return err
		}
		payload := map[string]string{
			"text": fmt.Sprintf("*%s*\n%s", ev.Subject, ev.Body),
		}
		body, _ := json.Marshal(payload)
		return s.postJSON(ctx, url, body)
	case "email":
		var e EmailConfig
		if err := json.Unmarshal(cfg, &e); err != nil {
			return err
		}
		return s.sendEmail(e, ev)
	default:
		return fmt.Errorf("unknown kind %q", kind)
	}
}

// unsealWebhookURL retrieves the URL from a webhook/slack channel config.
// It supports both the legacy plaintext "url" field (for rows stored before
// T10 sealing was introduced) and the new sealed "url_envelope" field.
func (s *Service) unsealWebhookURL(cfg []byte) (string, error) {
	var w WebhookConfig
	if err := json.Unmarshal(cfg, &w); err != nil {
		return "", fmt.Errorf("notify: parse webhook config: %w", err)
	}
	if w.URLEnvelope != "" {
		pt, err := s.Sealer.Open(w.URLEnvelope)
		if err != nil {
			return "", fmt.Errorf("notify: unseal webhook url: %w", err)
		}
		return string(pt), nil
	}
	// Fallback: legacy rows have plaintext url.
	if w.URL != "" {
		return w.URL, nil
	}
	return "", errors.New("notify: no url configured for webhook channel")
}

func (s *Service) postJSON(ctx context.Context, url string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("http %d", resp.StatusCode)
	}
	return nil
}

// smtpClient is the minimal interface we use; substitutable in tests.
type smtpClient interface {
	StartTLS(*tls.Config) error
	Auth(smtp.Auth) error
	Mail(string) error
	Rcpt(string) error
	Data() (interface {
		Write([]byte) (int, error)
		Close() error
	}, error)
	Quit() error
}

func (s *Service) sendEmail(cfg EmailConfig, ev Event) error {
	if s.dialSMTP != nil {
		// Test path.
		c, err := s.dialSMTP(net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)))
		if err != nil {
			return err
		}
		return runSMTP(c, cfg, s.unsealPW(cfg), ev)
	}
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	pw := s.unsealPW(cfg)
	auth := smtp.PlainAuth("", cfg.Username, pw, cfg.Host)
	body := []byte(fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\n\r\n%s",
		cfg.From, cfg.To, ev.Subject, ev.Body))
	return smtp.SendMail(addr, auth, cfg.From, []string{cfg.To}, body)
}

func (s *Service) unsealPW(cfg EmailConfig) string {
	if cfg.PasswordEnvelope == "" {
		return ""
	}
	pt, err := s.Sealer.Open(cfg.PasswordEnvelope)
	if err != nil {
		return ""
	}
	return string(pt)
}

// runSMTP is exported only for tests through the package-private dialSMTP
// hook; this version writes the message via the supplied smtpClient.
func runSMTP(c smtpClient, cfg EmailConfig, pw string, ev Event) error {
	if cfg.StartTLS {
		if err := c.StartTLS(&tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12}); err != nil {
			return err
		}
	}
	if cfg.Username != "" {
		if err := c.Auth(smtp.PlainAuth("", cfg.Username, pw, cfg.Host)); err != nil {
			return err
		}
	}
	if err := c.Mail(cfg.From); err != nil {
		return err
	}
	if err := c.Rcpt(cfg.To); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write([]byte(fmt.Sprintf(
		"From: %s\r\nTo: %s\r\nSubject: %s\r\n\r\n%s",
		cfg.From, cfg.To, ev.Subject, ev.Body))); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}
