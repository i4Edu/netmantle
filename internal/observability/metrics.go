// Package observability provides Prometheus metrics for the HTTP layer.
//
// We use a tiny in-process registry (stdlib expvar-shaped) rather than
// pulling the Prometheus client library, to keep dependencies small. The
// exposition format is Prometheus text format 0.0.4.
package observability

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"
)

// Metrics is the metrics registry.
type Metrics struct {
	mu       sync.Mutex
	counters map[string]uint64 // labelled counters: name|labels
	hist     map[string]*histogram
	startup  time.Time
}

type histogram struct {
	buckets []float64
	counts  []uint64
	sum     float64
	count   uint64
}

// New constructs a Metrics with default HTTP histograms.
func New() *Metrics {
	return &Metrics{
		counters: map[string]uint64{},
		hist:     map[string]*histogram{},
		startup:  time.Now(),
	}
}

// ObserveHTTP records one HTTP request.
func (m *Metrics) ObserveHTTP(method string, status int, dur time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := fmt.Sprintf(`netmantle_http_requests_total{method="%s",status="%d"}`, method, status)
	m.counters[key]++

	hkey := fmt.Sprintf(`netmantle_http_request_duration_seconds{method="%s"}`, method)
	h, ok := m.hist[hkey]
	if !ok {
		h = &histogram{
			buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
		}
		h.counts = make([]uint64, len(h.buckets))
		m.hist[hkey] = h
	}
	v := dur.Seconds()
	h.sum += v
	h.count++
	for i, b := range h.buckets {
		if v <= b {
			h.counts[i]++
		}
	}
}

// Inc increments a labelled counter by 1.
func (m *Metrics) Inc(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.counters[name]++
}

// Handler returns an http.Handler exposing /metrics in Prometheus text format.
func (m *Metrics) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")

		fmt.Fprintf(w, "# TYPE netmantle_uptime_seconds gauge\nnetmantle_uptime_seconds %d\n",
			int64(time.Since(m.startup).Seconds()))

		// Counters
		keys := make([]string, 0, len(m.counters))
		for k := range m.counters {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(w, "%s %d\n", k, m.counters[k])
		}

		// Histograms
		hkeys := make([]string, 0, len(m.hist))
		for k := range m.hist {
			hkeys = append(hkeys, k)
		}
		sort.Strings(hkeys)
		for _, hk := range hkeys {
			h := m.hist[hk]
			base, labels := splitMetric(hk)
			for i, b := range h.buckets {
				fmt.Fprintf(w, "%s_bucket{%sle=\"%s\"} %d\n", base, labels, formatFloat(b), h.counts[i])
			}
			fmt.Fprintf(w, "%s_bucket{%sle=\"+Inf\"} %d\n", base, labels, h.count)
			fmt.Fprintf(w, "%s_sum{%s} %g\n", base, trimTrailingComma(labels), h.sum)
			fmt.Fprintf(w, "%s_count{%s} %d\n", base, trimTrailingComma(labels), h.count)
		}
	})
}

// splitMetric splits `name{a="b",c="d"}` into ("name", `a="b",c="d",`) so
// we can append le= without re-parsing.
func splitMetric(s string) (string, string) {
	for i := 0; i < len(s); i++ {
		if s[i] == '{' {
			inner := s[i+1 : len(s)-1]
			if inner == "" {
				return s[:i], ""
			}
			return s[:i], inner + ","
		}
	}
	return s, ""
}

func trimTrailingComma(s string) string {
	if len(s) == 0 {
		return s
	}
	if s[len(s)-1] == ',' {
		return s[:len(s)-1]
	}
	return s
}

func formatFloat(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }
