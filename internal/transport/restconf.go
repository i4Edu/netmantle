package transport

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
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
	host := cfg.Address
	if _, _, hasScheme := strings.Cut(host, "://"); !hasScheme {
		host = "https://" + host
	}
	u, err := url.Parse(host)
	if err != nil {
		return nil, nil, fmt.Errorf("transport/restconf: parse address: %w", err)
	}
	if u.Port() == "" {
		u.Host = u.Hostname() + ":" + strconv.Itoa(cfg.Port)
	}
	basePath := strings.TrimRight(u.Path, "/")
	if basePath == "" {
		basePath = "/restconf"
	}
	baseURL := fmt.Sprintf("%s://%s%s", u.Scheme, u.Host, basePath)
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
		return "/data?content=candidate"
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
