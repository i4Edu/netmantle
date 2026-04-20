package transport

import (
	"context"
	"runtime"
	"testing"

	gpb "github.com/openconfig/gnmi/proto/gnmi"
	"google.golang.org/grpc"
)

type fixedLargeGNMIGetter struct {
	resp *gpb.GetResponse
}

func (f fixedLargeGNMIGetter) Get(_ context.Context, _ *gpb.GetRequest, _ ...grpc.CallOption) (*gpb.GetResponse, error) {
	return f.resp, nil
}

func (f fixedLargeGNMIGetter) Set(_ context.Context, _ *gpb.SetRequest, _ ...grpc.CallOption) (*gpb.SetResponse, error) {
	return &gpb.SetResponse{}, nil
}

func TestGNMIJSONIETFMappingMemoryGrowthBounded(t *testing.T) {
	sess := &gnmiSession{client: fixedLargeGNMIGetter{resp: largeGNMIResponse()}}

	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	for i := 0; i < 300; i++ {
		if _, err := sess.Run(context.Background(), "get-config:running"); err != nil {
			t.Fatal(err)
		}
	}
	runtime.GC()
	var mid runtime.MemStats
	runtime.ReadMemStats(&mid)
	for i := 0; i < 300; i++ {
		if _, err := sess.Run(context.Background(), "get-config:running"); err != nil {
			t.Fatal(err)
		}
	}
	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)

	firstGrowth := int64(mid.HeapAlloc) - int64(before.HeapAlloc)
	secondGrowth := int64(after.HeapAlloc) - int64(mid.HeapAlloc)
	// Guardrail: the second batch should not balloon compared to the first.
	if secondGrowth > firstGrowth+8*1024*1024 {
		t.Fatalf("heap growth suggests leak: first=%d second=%d", firstGrowth, secondGrowth)
	}
}

func BenchmarkGNMIJSONIETFMassiveStateCapture(b *testing.B) {
	sess := &gnmiSession{client: fixedLargeGNMIGetter{resp: largeGNMIResponse()}}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := sess.Run(context.Background(), "get-config:running"); err != nil {
			b.Fatal(err)
		}
	}
}

func largeGNMIResponse() *gpb.GetResponse {
	updates := make([]*gpb.Update, 0, 2000)
	for i := 0; i < 2000; i++ {
		updates = append(updates, &gpb.Update{
			Path: &gpb.Path{Elem: []*gpb.PathElem{
				{Name: "interfaces"},
				{Name: "interface"},
				{Name: "state"},
				{Name: "counter"},
			}},
			Val: &gpb.TypedValue{
				Value: &gpb.TypedValue_JsonIetfVal{JsonIetfVal: []byte(`{"rx-pkts":12345,"tx-pkts":67890,"admin-status":"up"}`)},
			},
		})
	}
	return &gpb.GetResponse{
		Notification: []*gpb.Notification{{
			Prefix: &gpb.Path{Elem: []*gpb.PathElem{{Name: "network-instances"}, {Name: "network-instance"}, {Name: "default"}}},
			Update: updates,
		}},
	}
}
