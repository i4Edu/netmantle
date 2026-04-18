package observability

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMetricsExposition(t *testing.T) {
	m := New()
	m.ObserveHTTP("GET", 200, 12*time.Millisecond)
	m.ObserveHTTP("GET", 200, 50*time.Millisecond)
	m.ObserveHTTP("POST", 500, 1500*time.Millisecond)

	rr := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/metrics", nil)
	m.Handler().ServeHTTP(rr, r)

	body := rr.Body.String()
	for _, want := range []string{
		`netmantle_http_requests_total{method="GET",status="200"} 2`,
		`netmantle_http_requests_total{method="POST",status="500"} 1`,
		`netmantle_http_request_duration_seconds_bucket`,
		`netmantle_uptime_seconds`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in:\n%s", want, body)
		}
	}
}
