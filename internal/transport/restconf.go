package transport

import (
	"context"
	"crypto/tls"
	"encoding/base64"
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
	method, path, body, contentType, err := restconfRequestFromCommand(cmd)
	if err != nil {
		return "", err
	}
	var reqBody io.Reader
	if method != http.MethodGet {
		reqBody = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, s.baseURL+path, reqBody)
	if err != nil {
		return "", fmt.Errorf("transport/restconf: build request: %w", err)
	}
	req.Header.Set("Accept", "application/yang-data+json, application/yang-data+xml, application/json, application/xml")
	if method != http.MethodGet {
		req.Header.Set("Content-Type", contentType)
	}
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
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("transport/restconf: read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("transport/restconf: status %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	return strings.TrimSpace(string(respBody)), nil
}

func restconfRequestFromCommand(cmd string) (method, path, body, contentType string, err error) {
	c := strings.TrimSpace(cmd)
	if strings.HasPrefix(c, "edit-config:") {
		path, body, contentType, err = restconfWritePathAndBody(c)
		if err != nil {
			return "", "", "", "", err
		}
		return http.MethodPatch, path, body, contentType, nil
	}
	path, err = restconfPathFromCommand(c)
	if err != nil {
		return "", "", "", "", err
	}
	return http.MethodGet, path, "", "", nil
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

func restconfWritePathAndBody(cmd string) (path, body, contentType string, err error) {
	rest := strings.TrimPrefix(strings.TrimSpace(cmd), "edit-config:")
	parts := strings.SplitN(rest, ":", 2)
	if len(parts) != 2 {
		return "", "", "", fmt.Errorf("transport/restconf: invalid edit-config command")
	}
	selector := strings.TrimSpace(parts[0])
	payload, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil {
		return "", "", "", fmt.Errorf("transport/restconf: invalid base64 payload")
	}
	switch selector {
	case "running":
		path = "/data"
	case "candidate":
		path = "/data?content=candidate"
	default:
		if !strings.HasPrefix(selector, "/") {
			selector = "/" + selector
		}
		path = selector
	}
	body = string(payload)
	if strings.HasPrefix(strings.TrimSpace(body), "<") {
		contentType = "application/yang-data+xml"
	} else {
		contentType = "application/yang-data+json"
	}
	return path, body, contentType, nil
}
