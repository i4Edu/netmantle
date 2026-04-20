package transport

import (
	"context"
	"testing"

	gpb "github.com/openconfig/gnmi/proto/gnmi"
	"google.golang.org/grpc"
)

type fakeGNMIGetter struct {
	resp *gpb.GetResponse
	err  error
	req  *gpb.GetRequest
}

func (f *fakeGNMIGetter) Get(_ context.Context, in *gpb.GetRequest, _ ...grpc.CallOption) (*gpb.GetResponse, error) {
	f.req = in
	return f.resp, f.err
}

func TestGNMISessionRun(t *testing.T) {
	getter := &fakeGNMIGetter{
		resp: &gpb.GetResponse{
			Notification: []*gpb.Notification{
				{
					Update: []*gpb.Update{
						{
							Path: &gpb.Path{Elem: []*gpb.PathElem{{Name: "interfaces"}, {Name: "interface"}}},
							Val:  &gpb.TypedValue{Value: &gpb.TypedValue_JsonIetfVal{JsonIetfVal: []byte(`{"name":"xe-0/0/0"}`)}},
						},
					},
				},
			},
		},
	}
	s := &gnmiSession{getter: getter, username: "u", password: "p"}
	got, err := s.Run(context.Background(), "get-config:running")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got == "" || getter.req == nil {
		t.Fatal("expected non-empty response and request")
	}
	if getter.req.GetType() != gpb.GetRequest_CONFIG {
		t.Fatalf("expected CONFIG type, got %v", getter.req.GetType())
	}
}

func TestBuildGNMIGetRequestRejectsUnsupportedCommand(t *testing.T) {
	if _, err := buildGNMIGetRequest("show run"); err == nil {
		t.Fatal("expected error for unsupported command")
	}
}

func TestParseGNMIPath(t *testing.T) {
	p := parseGNMIPath("/a/b/c")
	if len(p.GetElem()) != 3 {
		t.Fatalf("unexpected elem count: %d", len(p.GetElem()))
	}
	if got := gnmiPathToString(p); got != "/a/b/c" {
		t.Fatalf("unexpected path: %q", got)
	}
}

func TestGNMITargetIPv6Normalization(t *testing.T) {
	t.Run("ipv6 literal without port", func(t *testing.T) {
		got, err := gnmiTarget("2001:db8::1", 57400)
		if err != nil {
			t.Fatalf("gnmiTarget: %v", err)
		}
		if got != "[2001:db8::1]:57400" {
			t.Fatalf("unexpected target: %q", got)
		}
	})
	t.Run("scheme plus ipv6 literal", func(t *testing.T) {
		got, err := gnmiTarget("https://[2001:db8::2]", 57400)
		if err != nil {
			t.Fatalf("gnmiTarget: %v", err)
		}
		if got != "[2001:db8::2]:57400" {
			t.Fatalf("unexpected target: %q", got)
		}
	})
}
