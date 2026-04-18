// Package version exposes the build version, injected via -ldflags.
package version

// Version is overridden at build time via:
//
//	go build -ldflags "-X github.com/i4Edu/netmantle/internal/version.Version=$(git describe)"
var Version = "dev"
