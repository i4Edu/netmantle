package transport

import (
	"context"
	"encoding/base64"
	"io"
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

func TestRESTCONFEditConfigWithBasicAuth(t *testing.T) {
	payload := `{"system":{"config":{"hostname":"r1"}}}`
	cmd := "edit-config:running:" + base64.StdEncoding.EncodeToString([]byte(payload))
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "u" || pass != "p" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodPatch {
			http.Error(w, "method "+r.Method, http.StatusMethodNotAllowed)
			return
		}
		if got := r.URL.RequestURI(); got != "/restconf/data" {
			http.Error(w, "bad path "+got, http.StatusBadRequest)
			return
		}
		gotBody, _ := io.ReadAll(r.Body)
		if string(gotBody) != payload {
			http.Error(w, "bad payload", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"status":"ok"}`))
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
	got, err := sess.Run(context.Background(), cmd)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got != `{"status":"ok"}` {
		t.Fatalf("unexpected response body: %q", got)
	}
}

func TestRESTCONFBaseURLIPv6Normalization(t *testing.T) {
	cases := []struct {
		name    string
		address string
		want    string
	}{
		{
			name:    "raw ipv6 literal",
			address: "2001:db8::1",
			want:    "https://[2001:db8::1]:443/restconf",
		},
		{
			name:    "scheme bracketed ipv6",
			address: "https://[2001:db8::2]",
			want:    "https://[2001:db8::2]:443/restconf",
		},
		{
			name:    "raw ipv6 with path",
			address: "2001:db8::3/custom",
			want:    "https://[2001:db8::3]:443/custom",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := restconfBaseURL(tc.address, 443)
			if err != nil {
				t.Fatalf("restconfBaseURL: %v", err)
			}
			if got != tc.want {
				t.Fatalf("want %q, got %q", tc.want, got)
			}
		})
	}
}
