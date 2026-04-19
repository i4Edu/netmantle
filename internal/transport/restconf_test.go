package transport

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRESTCONFRunWithBasicAuth(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "u" || pass != "p" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if got := r.URL.RequestURI(); got != "/restconf/data" {
			http.Error(w, "bad path "+got, http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	sess, _, err := DialRESTCONF(context.Background(), RESTCONFConfig{
		Address:            srv.URL,
		Username:           "u",
		Password:           "p",
		InsecureSkipVerify: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := sess.Run(context.Background(), "get-config:running")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got != `{"ok":true}` {
		t.Fatalf("unexpected body: %q", got)
	}
}

func TestRESTCONFRunWithBearerToken(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if got := r.URL.RequestURI(); got != "/restconf/custom/path" {
			http.Error(w, "bad path "+got, http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`<ok/>`))
	}))
	defer srv.Close()

	sess, _, err := DialRESTCONF(context.Background(), RESTCONFConfig{
		Address:            srv.URL,
		BearerToken:        "tok",
		InsecureSkipVerify: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := sess.Run(context.Background(), "get-config:/custom/path")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got != `<ok/>` {
		t.Fatalf("unexpected body: %q", got)
	}
}

func TestRESTCONFPathParsing(t *testing.T) {
	cases := []struct {
		cmd  string
		want string
		ok   bool
	}{
		{cmd: "get-config", want: "/data", ok: true},
		{cmd: "get-config:running", want: "/data", ok: true},
		{cmd: "get-config:candidate", want: "/data?content=candidate", ok: true},
		{cmd: "get-config:/x", want: "/x", ok: true},
		{cmd: "show run", ok: false},
	}
	for _, tc := range cases {
		got, err := restconfPathFromCommand(tc.cmd)
		if tc.ok && err != nil {
			t.Fatalf("%q: %v", tc.cmd, err)
		}
		if !tc.ok && err == nil {
			t.Fatalf("%q: expected error", tc.cmd)
		}
		if tc.ok && got != tc.want {
			t.Fatalf("%q: want %q got %q", tc.cmd, tc.want, got)
		}
	}
}
