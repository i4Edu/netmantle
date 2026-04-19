// Package poller implements the registration + heartbeat side of the
// remote-poller protocol (Phase 7).
//
// The gRPC wire protocol is defined in proto/poller.proto. When the
// generated pollerv1 package is available (run `buf generate` or
// `protoc` — see proto/poller.proto for instructions) the GRPCServer
// type in grpcserver.go attaches to the interfaces below.
package poller

// (existing code continues in poller.go)
