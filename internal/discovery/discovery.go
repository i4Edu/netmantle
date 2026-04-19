// Package discovery scans IP ranges for live devices and tries to
// fingerprint them so they can be imported into inventory (Phase 5).
//
// The Phase 5 deliverable in the plan is broad (SNMP/CDP/LLDP, multi-NMS
// importers); this MVP ships:
//   - TCP-port-probe based ping sweep (port 22 by default — most NCM
//     targets expose SSH)
//   - SSH banner fingerprinting → suggested driver
//   - NetBox JSON importer (file or HTTP body)
package discovery

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
)

// Service runs scans and persists results.
type Service struct {
	DB     *sql.DB
	Dialer interface {
		DialContext(ctx context.Context, net, addr string) (net.Conn, error)
	}
}

// New constructs a Service.
func New(db *sql.DB) *Service {
	return &Service{DB: db, Dialer: &net.Dialer{Timeout: 2 * time.Second}}
}

// Scan represents one discovery run.
type Scan struct {
	ID        int64     `json:"id"`
	TenantID  int64     `json:"tenant_id"`
	CIDR      string    `json:"cidr"`
	StartedAt time.Time `json:"started_at"`
	Status    string    `json:"status"`
}

// Result is one device discovered.
type Result struct {
	Address         string `json:"address"`
	Fingerprint     string `json:"fingerprint"`
	SuggestedDriver string `json:"suggested_driver"`
}

// Run executes a sweep over a CIDR range and persists results.
// Concurrency is bounded; ports defaults to {22}.
func (s *Service) Run(ctx context.Context, tenantID int64, cidr string, port int, concurrency int) (*Scan, []Result, error) {
	if port <= 0 {
		port = 22
	}
	if concurrency <= 0 {
		concurrency = 64
	}
	addrs, err := expandCIDR(cidr)
	if err != nil {
		return nil, nil, err
	}
	now := time.Now().UTC()
	res, err := s.DB.ExecContext(ctx,
		`INSERT INTO discovery_scans(tenant_id, cidr, started_at, status) VALUES(?, ?, ?, 'running')`,
		tenantID, cidr, now.Format(time.RFC3339))
	if err != nil {
		return nil, nil, err
	}
	scanID, _ := res.LastInsertId()

	sem := make(chan struct{}, concurrency)
	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		out []Result
	)
	for _, a := range addrs {
		a := a
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			r, ok := s.probe(ctx, a, port)
			if !ok {
				return
			}
			mu.Lock()
			out = append(out, r)
			mu.Unlock()
		}()
	}
	wg.Wait()

	for _, r := range out {
		_, _ = s.DB.ExecContext(ctx,
			`INSERT INTO discovery_results(scan_id, address, fingerprint, suggested_driver) VALUES(?, ?, ?, ?)`,
			scanID, r.Address, r.Fingerprint, r.SuggestedDriver)
	}
	_, _ = s.DB.ExecContext(ctx,
		`UPDATE discovery_scans SET finished_at=?, status='complete' WHERE id=?`,
		time.Now().UTC().Format(time.RFC3339), scanID)
	return &Scan{ID: scanID, TenantID: tenantID, CIDR: cidr, StartedAt: now, Status: "complete"}, out, nil
}

func (s *Service) probe(ctx context.Context, addr string, port int) (Result, bool) {
	dctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	conn, err := s.Dialer.DialContext(dctx, "tcp", net.JoinHostPort(addr, fmt.Sprintf("%d", port)))
	if err != nil {
		return Result{}, false
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(1500 * time.Millisecond))
	buf := make([]byte, 256)
	n, _ := conn.Read(buf)
	banner := strings.TrimSpace(string(buf[:n]))
	return Result{Address: addr, Fingerprint: banner, SuggestedDriver: fingerprintToDriver(banner)}, true
}

// fingerprintToDriver maps an SSH banner to a builtin driver name.
func fingerprintToDriver(banner string) string {
	b := strings.ToLower(banner)
	switch {
	case strings.Contains(b, "cisco"):
		return "cisco_ios"
	case strings.Contains(b, "arista"):
		return "arista_eos"
	case strings.Contains(b, "openssh"), strings.HasPrefix(b, "ssh-"):
		return "generic_ssh"
	}
	return "generic_ssh"
}

// expandCIDR returns every host address in a CIDR (IPv4 only). Refuses to
// expand /0–/15 as a safety check.
func expandCIDR(cidr string) ([]string, error) {
	_, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, err
	}
	ones, bits := ipnet.Mask.Size()
	if bits != 32 {
		return nil, fmt.Errorf("discovery: only IPv4 supported")
	}
	if ones < 16 {
		return nil, fmt.Errorf("discovery: prefix /%d too large", ones)
	}
	var out []string
	ip := ipnet.IP.Mask(ipnet.Mask)
	for ; ipnet.Contains(ip); incIP(ip) {
		out = append(out, ip.String())
	}
	// Trim network/broadcast addresses for prefixes < /31.
	if ones <= 30 && len(out) >= 2 {
		out = out[1 : len(out)-1]
	}
	return out, nil
}

func incIP(ip net.IP) {
	for i := len(ip) - 1; i >= 0; i-- {
		ip[i]++
		if ip[i] != 0 {
			break
		}
	}
}

// NetBoxDevice is the subset of fields we read.
type NetBoxDevice struct {
	Name      string `json:"name"`
	PrimaryIP struct {
		Address string `json:"address"`
	} `json:"primary_ip"`
	DeviceType struct {
		Manufacturer struct {
			Slug string `json:"slug"`
		} `json:"manufacturer"`
	} `json:"device_type"`
}

// NetBoxResponse is the standard paginated list shape.
type NetBoxResponse struct {
	Results []NetBoxDevice `json:"results"`
}

// ImportNetBox parses a NetBox `dcim/devices` JSON payload and returns
// devices ready to insert via internal/devices.
type ImportItem struct {
	Hostname string
	Address  string
	Driver   string
}

// ImportNetBox reads a NetBox-format JSON document and returns importable
// records. It does not write to the DB; callers decide what to do.
func ImportNetBox(body []byte) ([]ImportItem, error) {
	var resp NetBoxResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	var out []ImportItem
	for _, d := range resp.Results {
		if d.Name == "" || d.PrimaryIP.Address == "" {
			continue
		}
		// Strip CIDR suffix from "1.2.3.4/24".
		addr := d.PrimaryIP.Address
		if i := strings.IndexByte(addr, '/'); i >= 0 {
			addr = addr[:i]
		}
		out = append(out, ImportItem{
			Hostname: d.Name, Address: addr,
			Driver: manufacturerToDriver(d.DeviceType.Manufacturer.Slug),
		})
	}
	return out, nil
}

func manufacturerToDriver(slug string) string {
	switch strings.ToLower(slug) {
	case "cisco":
		return "cisco_ios"
	case "arista":
		return "arista_eos"
	}
	return "generic_ssh"
}
