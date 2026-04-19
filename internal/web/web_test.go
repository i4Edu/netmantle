package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEmbeddedAssets(t *testing.T) {
	srv := httptest.NewServer(Handler())
	defer srv.Close()
	for _, c := range []struct{ path, contains string }{
		{"/", "<title>NetMantle</title>"},
		{"/index.html", "data-route=\"audit\""},
		{"/app.tokens.css", "--status-ok"},
		{"/app.css", ".status-dot"},
		{"/app.js", "ROUTES"},
	} {
		r, err := http.Get(srv.URL + c.path)
		if err != nil {
			t.Fatalf("%s: %v", c.path, err)
		}
		body, _ := io.ReadAll(r.Body)
		r.Body.Close()
		if r.StatusCode != 200 {
			t.Fatalf("%s: status %d", c.path, r.StatusCode)
		}
		if !strings.Contains(string(body), c.contains) {
			t.Fatalf("%s: missing %q", c.path, c.contains)
		}
	}
}
