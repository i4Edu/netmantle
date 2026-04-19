// Package poller implements the registration + heartbeat side of the
// remote-poller protocol (Phase 7).
//
// The gRPC wire protocol is defined in proto/poller.proto. WireService in this
// package provides a transport-agnostic core adapter for the authenticate /
// claim / report flow so the gRPC+mTLS listener shell can delegate directly
// to persisted queue logic once RPC handlers are registered.
package poller

// (existing code continues in poller.go)
