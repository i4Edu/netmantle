// Package poller implements the registration + heartbeat side of the
// remote-poller protocol (Phase 7).
//
// The gRPC wire protocol is defined in proto/poller.proto. Once protoc is
// run to generate the pollerv1 package (see proto/poller.proto for
// instructions), a future gRPC server implementation can use the service
// interfaces defined there.
package poller

// (existing code continues in poller.go)
