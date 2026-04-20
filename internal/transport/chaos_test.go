package transport

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	gpb "github.com/openconfig/gnmi/proto/gnmi"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// blockingGNMIGetter blocks until the context is canceled, then returns ctx.Err().
type blockingGNMIGetter struct{}

func (b *blockingGNMIGetter) Get(ctx context.Context, _ *gpb.GetRequest, _ ...grpc.CallOption) (*gpb.GetResponse, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// grpcStatusGNMIGetter returns a gRPC status error with the configured code.
type grpcStatusGNMIGetter struct {
	code codes.Code
	msg  string
}

func (g *grpcStatusGNMIGetter) Get(_ context.Context, _ *gpb.GetRequest, _ ...grpc.CallOption) (*gpb.GetResponse, error) {
	return nil, status.Error(g.code, g.msg)
}

func TestGNMIChaos(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		getter   gnmiGetter
		setupCtx func() (context.Context, context.CancelFunc)
		wantErr  bool
		errCheck func(t *testing.T, err error)
	}{
		{
			name:   "deadline exceeded propagates from getter",
			getter: &blockingGNMIGetter{},
			setupCtx: func() (context.Context, context.CancelFunc) {
				return context.WithTimeout(context.Background(), 50*time.Millisecond)
			},
			wantErr: true,
			errCheck: func(t *testing.T, err error) {
				t.Helper()
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("expected DeadlineExceeded in error chain, got: %v", err)
				}
			},
		},
		{
			name: "grpc Unavailable status bubbles through Run",
			getter: &grpcStatusGNMIGetter{
				code: codes.Unavailable,
				msg:  "connection reset by peer",
			},
			setupCtx: func() (context.Context, context.CancelFunc) {
				return context.WithCancel(context.Background())
			},
			wantErr: true,
			errCheck: func(t *testing.T, err error) {
				t.Helper()
				st, ok := status.FromError(errors.Unwrap(err))
				if !ok {
					// unwrap one more level (transport/gnmi: get: <status>)
					inner := err
					for inner != nil {
						if s, ok2 := status.FromError(inner); ok2 && s.Code() != codes.OK {
							st = s
							ok = true
							break
						}
						inner = errors.Unwrap(inner)
					}
				}
				if !ok {
					t.Fatalf("expected gRPC status error in chain, got: %v", err)
				}
				if st.Code() != codes.Unavailable {
					t.Fatalf("expected codes.Unavailable, got %v", st.Code())
				}
			},
		},
		{
			name: "empty GetResponse returns valid empty-object JSON",
			getter: &fakeGNMIGetter{
				resp: &gpb.GetResponse{},
			},
			setupCtx: func() (context.Context, context.CancelFunc) {
				return context.WithCancel(context.Background())
			},
			wantErr: false,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := tc.setupCtx()
			defer cancel()

			sess := &gnmiSession{getter: tc.getter, username: "testuser", password: "testpass"}
			got, err := sess.Run(ctx, "get-config:running")

			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if tc.errCheck != nil {
					tc.errCheck(t, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			// Empty response must produce valid JSON object, not a parse error.
			if got != "{}" {
				t.Fatalf("expected empty JSON object '{}', got %q", got)
			}
		})
	}
}

func TestRESTCONFChaos(t *testing.T) {
	t.Parallel()

	t.Run("server latency exceeds client timeout", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(500 * time.Millisecond)
			_, _ = w.Write([]byte(`{"delayed":true}`))
		}))
		defer srv.Close()

		sess, _, err := DialRESTCONF(context.Background(), RESTCONFConfig{
			Address:            srv.URL,
			Username:           "u",
			Password:           "p",
			InsecureSkipVerify: true,
			Timeout:            30 * time.Millisecond,
		})
		if err != nil {
			t.Fatal(err)
		}

		_, runErr := sess.Run(context.Background(), "get-config:running")
		if runErr == nil {
			t.Fatal("expected timeout error, got nil")
		}
		var netErr net.Error
		if !errors.As(runErr, &netErr) || !netErr.Timeout() {
			t.Fatalf("expected net timeout error, got: %v", runErr)
		}
	})

	t.Run("server closes connection mid-response", func(t *testing.T) {
		t.Parallel()
		// Use NewUnstartedServer + force HTTP/1.1 so http.Hijacker is always
		// available in the handler. httptest.NewTLSServer includes "h2" in
		// NextProtos which prevents Hijack on HTTP/2 connections.
		srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hj, ok := w.(http.Hijacker)
			if !ok {
				http.Error(w, "hijack not supported", http.StatusInternalServerError)
				return
			}
			conn, _, hjErr := hj.Hijack()
			if hjErr != nil {
				return
			}
			// Write a response that advertises more body than we actually send.
			_, _ = conn.Write([]byte(
				"HTTP/1.1 200 OK\r\n" +
					"Content-Type: application/json\r\n" +
					"Content-Length: 1024\r\n" +
					"\r\n" +
					`{"partial":`,
			))
			conn.Close()
		}))
		// Restrict to HTTP/1.1 only — excludes "h2" so the server never
		// upgrades to HTTP/2, guaranteeing Hijack support.
		srv.TLS = &tls.Config{NextProtos: []string{"http/1.1"}}
		srv.StartTLS()
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

		_, runErr := sess.Run(context.Background(), "get-config:running")
		if runErr == nil {
			t.Fatal("expected error from abrupt server closure, got nil")
		}
	})
}
