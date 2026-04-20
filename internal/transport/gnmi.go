package transport

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/i4Edu/netmantle/internal/drivers"
	gpb "github.com/openconfig/gnmi/proto/gnmi"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
)

// GNMIConfig holds connection parameters for a gNMI session.
type GNMIConfig struct {
	Address            string
	Port               int
	Username           string
	Password           string
	BearerToken        string
	Timeout            time.Duration
	InsecureSkipVerify bool
}

type gnmiGetter interface {
	Get(ctx context.Context, in *gpb.GetRequest, opts ...grpc.CallOption) (*gpb.GetResponse, error)
}

type gnmiSetter interface {
	Set(ctx context.Context, in *gpb.SetRequest, opts ...grpc.CallOption) (*gpb.SetResponse, error)
}

type gnmiClient interface {
	gnmiGetter
	gnmiSetter
}

type gnmiSession struct {
	client      gnmiClient
	username    string
	password    string
	bearerToken string
}

// DialGNMI opens a gNMI session adapter implementing drivers.Session.
func DialGNMI(ctx context.Context, cfg GNMIConfig) (drivers.Session, func() error, error) {
	if cfg.Address == "" {
		return nil, nil, fmt.Errorf("transport/gnmi: empty address")
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	if cfg.Port == 0 {
		cfg.Port = 57400
	}
	target, err := gnmiTarget(cfg.Address, cfg.Port)
	if err != nil {
		return nil, nil, err
	}
	dialCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	conn, err := grpc.DialContext(
		dialCtx,
		target,
		grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: cfg.InsecureSkipVerify,
		})),
		grpc.WithBlock(),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("transport/gnmi: dial: %w", err)
	}
	return &gnmiSession{
		client:      gpb.NewGNMIClient(conn),
		username:    cfg.Username,
		password:    cfg.Password,
		bearerToken: cfg.BearerToken,
	}, conn.Close, nil
}

func gnmiTarget(address string, defaultPort int) (string, error) {
	raw := strings.TrimSpace(address)
	if raw == "" {
		return "", fmt.Errorf("transport/gnmi: empty address")
	}
	hostPort := raw
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil {
			return "", fmt.Errorf("transport/gnmi: parse address: %w", err)
		}
		if strings.TrimSpace(u.Host) == "" {
			return "", fmt.Errorf("transport/gnmi: missing host in address %q", raw)
		}
		hostPort = u.Host
	}
	return normalizeHostPort(hostPort, defaultPort, "transport/gnmi")
}

func normalizeHostPort(hostPort string, defaultPort int, label string) (string, error) {
	hostPort = strings.TrimSpace(hostPort)
	if hostPort == "" {
		return "", fmt.Errorf("%s: empty host", label)
	}
	if _, _, err := net.SplitHostPort(hostPort); err == nil {
		return hostPort, nil
	}
	trimmed := strings.TrimPrefix(strings.TrimSuffix(hostPort, "]"), "[")
	if ip, err := netip.ParseAddr(trimmed); err == nil {
		return net.JoinHostPort(ip.String(), strconv.Itoa(defaultPort)), nil
	}
	if strings.Contains(hostPort, ":") {
		return "", fmt.Errorf("%s: invalid host:port %q", label, hostPort)
	}
	return net.JoinHostPort(hostPort, strconv.Itoa(defaultPort)), nil
}

func (s *gnmiSession) Run(ctx context.Context, cmd string) (string, error) {
	cmd = strings.TrimSpace(cmd)
	ctx = withGNMIAuth(ctx, s.username, s.password, s.bearerToken)
	if strings.HasPrefix(cmd, "get-config") {
		req, err := buildGNMIGetRequest(cmd)
		if err != nil {
			return "", err
		}
		resp, err := s.client.Get(ctx, req)
		if err != nil {
			return "", fmt.Errorf("transport/gnmi: get: %w", err)
		}
		out := map[string]any{}
		for _, notif := range resp.GetNotification() {
			prefix := gnmiPathToString(notif.GetPrefix())
			for _, upd := range notif.GetUpdate() {
				key := joinGNMIPath(prefix, gnmiPathToString(upd.GetPath()))
				out[key] = gnmiTypedValueToAny(upd.GetVal())
			}
			for _, del := range notif.GetDelete() {
				key := joinGNMIPath(prefix, gnmiPathToString(del))
				out[key] = nil
			}
		}
		body, err := json.Marshal(out)
		if err != nil {
			return "", fmt.Errorf("transport/gnmi: marshal response: %w", err)
		}
		return string(body), nil
	}
	if strings.HasPrefix(cmd, "edit-config:") || strings.HasPrefix(cmd, "set-config:") {
		req, err := buildGNMISetRequest(cmd)
		if err != nil {
			return "", err
		}
		resp, err := s.client.Set(ctx, req)
		if err != nil {
			return "", fmt.Errorf("transport/gnmi: set: %w", err)
		}
		results := make([]map[string]any, 0, len(resp.GetResponse()))
		for _, r := range resp.GetResponse() {
			results = append(results, map[string]any{
				"path":      gnmiPathToString(r.GetPath()),
				"operation": r.GetOp().String(),
			})
		}
		body, err := json.Marshal(map[string]any{
			"timestamp": resp.GetTimestamp(),
			"results":   results,
		})
		if err != nil {
			return "", fmt.Errorf("transport/gnmi: marshal set response: %w", err)
		}
		return string(body), nil
	}
	return "", fmt.Errorf("transport/gnmi: unsupported command (use get-config or edit-config:<running|candidate|path>:<base64-json>)")
}

func withGNMIAuth(ctx context.Context, username, password, bearer string) context.Context {
	md := metadata.New(nil)
	if bearer != "" {
		md.Set("authorization", "Bearer "+bearer)
	} else if username != "" {
		md.Set("username", username)
		md.Set("password", password)
	}
	return metadata.NewOutgoingContext(ctx, md)
}

func buildGNMIGetRequest(cmd string) (*gpb.GetRequest, error) {
	c := strings.TrimSpace(cmd)
	switch c {
	case "get-config", "get-config:running", "get-config:candidate":
		return &gpb.GetRequest{
			Path:     []*gpb.Path{{}},
			Type:     gpb.GetRequest_CONFIG,
			Encoding: gpb.Encoding_JSON_IETF,
		}, nil
	}
	if strings.HasPrefix(c, "get-config:") {
		pathExpr := strings.TrimPrefix(c, "get-config:")
		if pathExpr == "" {
			return nil, fmt.Errorf("transport/gnmi: empty get-config selector")
		}
		return &gpb.GetRequest{
			Path:     []*gpb.Path{parseGNMIPath(pathExpr)},
			Type:     gpb.GetRequest_CONFIG,
			Encoding: gpb.Encoding_JSON_IETF,
		}, nil
	}
	return nil, fmt.Errorf("transport/gnmi: unsupported command (use get-config[:running|candidate|<path>])")
}

func buildGNMISetRequest(cmd string) (*gpb.SetRequest, error) {
	path, payload, err := parseGNMISetCommand(cmd)
	if err != nil {
		return nil, err
	}
	return &gpb.SetRequest{
		Replace: []*gpb.Update{{
			Path: path,
			Val:  &gpb.TypedValue{Value: &gpb.TypedValue_JsonIetfVal{JsonIetfVal: payload}},
		}},
	}, nil
}

func parseGNMISetCommand(cmd string) (*gpb.Path, []byte, error) {
	c := strings.TrimSpace(cmd)
	c = strings.TrimPrefix(c, "edit-config:")
	c = strings.TrimPrefix(c, "set-config:")
	parts := strings.SplitN(c, ":", 2)
	if len(parts) != 2 {
		return nil, nil, fmt.Errorf("transport/gnmi: invalid edit-config command")
	}
	selector := strings.TrimSpace(parts[0])
	payload, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, nil, fmt.Errorf("transport/gnmi: invalid base64 payload")
	}
	switch selector {
	case "running", "candidate":
		return &gpb.Path{}, payload, nil
	default:
		if selector == "" {
			return nil, nil, fmt.Errorf("transport/gnmi: empty edit-config selector")
		}
		return parseGNMIPath(selector), payload, nil
	}
}

func parseGNMIPath(p string) *gpb.Path {
	p = strings.TrimSpace(strings.TrimPrefix(p, "/"))
	if p == "" {
		return &gpb.Path{}
	}
	parts := strings.Split(p, "/")
	elems := make([]*gpb.PathElem, 0, len(parts))
	for _, part := range parts {
		if part == "" {
			continue
		}
		elems = append(elems, &gpb.PathElem{Name: part})
	}
	return &gpb.Path{Elem: elems}
}

func gnmiPathToString(p *gpb.Path) string {
	if p == nil || len(p.GetElem()) == 0 {
		return "/"
	}
	parts := make([]string, 0, len(p.GetElem()))
	for _, e := range p.GetElem() {
		if e == nil {
			continue
		}
		parts = append(parts, e.GetName())
	}
	if len(parts) == 0 {
		return "/"
	}
	return "/" + strings.Join(parts, "/")
}

func joinGNMIPath(prefix, path string) string {
	if prefix == "/" {
		return path
	}
	if path == "/" {
		return prefix
	}
	return strings.TrimRight(prefix, "/") + path
}

func gnmiTypedValueToAny(v *gpb.TypedValue) any {
	if v == nil {
		return nil
	}
	switch x := v.GetValue().(type) {
	case *gpb.TypedValue_JsonIetfVal:
		var out any
		if err := json.Unmarshal(x.JsonIetfVal, &out); err == nil {
			return out
		}
		return string(x.JsonIetfVal)
	case *gpb.TypedValue_JsonVal:
		var out any
		if err := json.Unmarshal(x.JsonVal, &out); err == nil {
			return out
		}
		return string(x.JsonVal)
	case *gpb.TypedValue_StringVal:
		return x.StringVal
	case *gpb.TypedValue_IntVal:
		return x.IntVal
	case *gpb.TypedValue_UintVal:
		return x.UintVal
	case *gpb.TypedValue_BoolVal:
		return x.BoolVal
	case *gpb.TypedValue_FloatVal:
		return x.FloatVal
	case *gpb.TypedValue_DoubleVal:
		return x.DoubleVal
	case *gpb.TypedValue_BytesVal:
		return string(x.BytesVal)
	case *gpb.TypedValue_LeaflistVal:
		out := make([]any, 0, len(x.LeaflistVal.GetElement()))
		for _, el := range x.LeaflistVal.GetElement() {
			out = append(out, gnmiTypedValueToAny(el))
		}
		return out
	default:
		return fmt.Sprintf("%v", v)
	}
}
