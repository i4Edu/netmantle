// Package poller implements the registration + heartbeat side of the
// remote-poller protocol (Phase 7).
//
// The gRPC wire protocol is defined in proto/poller.proto. WireService in this
// package provides a transport-agnostic core adapter for the authenticate /
// claim / report flow so the eventual gRPC+mTLS endpoint can delegate directly
// to persisted queue logic.
package poller

// (existing code continues in poller.go)
