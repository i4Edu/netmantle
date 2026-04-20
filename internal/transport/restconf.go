package transport

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/i4Edu/netmantle/internal/drivers"
)

// RESTCONFConfig holds connection parameters for a RESTCONF session.
type RESTCONFConfig struct {
	Address            string
	Port               int
	Username           string
	Password           string
	BearerToken        string
	Timeout            time.Duration
	InsecureSkipVerify bool
}

type restconfSession struct {
	client      *http.Client
	baseURL     string
	username    string
	password    string
	bearerToken string
}

// DialRESTCONF opens a RESTCONF session adapter implementing drivers.Session.
func DialRESTCONF(_ context.Context, cfg RESTCONFConfig) (drivers.Session, func() error, error) {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	if cfg.Address == "" {
		return nil, nil, fmt.Errorf("transport/restconf: empty address")
	}
	if cfg.Port == 0 {
		cfg.Port = 443
	}
	baseURL, err := restconfBaseURL(cfg.Address, cfg.Port)
	if err != nil {
		return nil, nil, err
	}
	client := &http.Client{
		Timeout: cfg.Timeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: cfg.InsecureSkipVerify},
		},
	}
	sess := &restconfSession{
		client:      client,
		baseURL:     baseURL,
		username:    cfg.Username,
		password:    cfg.Password,
		bearerToken: cfg.BearerToken,
	}
	return sess, func() error { return nil }, nil
}

func restconfBaseURL(address string, defaultPort int) (string, error) {
	raw := strings.TrimSpace(address)
	if raw == "" {
		return "", fmt.Errorf("transport/restconf: empty address")
	}
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil {
			return "", fmt.Errorf("transport/restconf: parse address: %w", err)
		}
		if strings.TrimSpace(u.Host) == "" {
			return "", fmt.Errorf("transport/restconf: missing host in address %q", raw)
		}
		hostPort, err := normalizeHostPort(u.Host, defaultPort, "transport/restconf")
		if err != nil {
			return "", err
		}
		path := strings.TrimRight(u.Path, "/")
		if path == "" {
			path = "/restconf"
		}
		return fmt.Sprintf("%s://%s%s", u.Scheme, hostPort, path), nil
	}
	hostPart := raw
	path := "/restconf"
	if i := strings.IndexByte(raw, '/'); i >= 0 {
		hostPart = raw[:i]
		path = "/" + strings.TrimLeft(raw[i+1:], "/")
		if strings.TrimSpace(path) == "/" {
			path = "/restconf"
		}
	}
	hostPort, err := normalizeHostPort(hostPart, defaultPort, "transport/restconf")
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("https://%s%s", hostPort, strings.TrimRight(path, "/")), nil
}

func (s *restconfSession) Run(ctx context.Context, cmd string) (string, error) {
	path, err := restconfPathFromCommand(cmd)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.baseURL+path, nil)
	if err != nil {
		return "", fmt.Errorf("transport/restconf: build request: %w", err)
	}
	req.Header.Set("Accept", "application/yang-data+json, application/yang-data+xml, application/json, application/xml")
	if s.bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+s.bearerToken)
	} else if s.username != "" {
		req.SetBasicAuth(s.username, s.password)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("transport/restconf: request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("transport/restconf: read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("transport/restconf: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return strings.TrimSpace(string(body)), nil
}

func restconfPathFromCommand(cmd string) (string, error) {
	c := strings.TrimSpace(cmd)
	switch c {
	case "get-config", "get-config:running":
		return "/data", nil
	case "get-config:candidate":
		return "/data?content=candidate", nil
	}
	if strings.HasPrefix(c, "get-config:") {
		p := strings.TrimPrefix(c, "get-config:")
		if p == "" {
			return "", fmt.Errorf("transport/restconf: empty get-config selector")
		}
		if !strings.HasPrefix(p, "/") {
			p = "/" + p
		}
		return p, nil
	}
	return "", fmt.Errorf("transport/restconf: unsupported command %q (use get-config[:running|candidate|<path>])", c)
}
