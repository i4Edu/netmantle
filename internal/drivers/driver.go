// Package drivers defines the device-driver contract and a global registry.
//
// A Driver knows how to talk to one device family (e.g. Cisco IOS). Drivers
// are CLI-dialect modules: they receive a transport-agnostic Session, send
// platform-specific commands (disable paging, show running-config, ...) and
// return one or more ConfigArtifacts.
//
// Builtin drivers live under internal/drivers/builtin and self-register via
// init() so adding a driver is purely additive — no central switch
// statement to update.
package drivers

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

// ConfigArtifact is one piece of configuration retrieved from a device.
// Typical names are "running-config" and "startup-config".
type ConfigArtifact struct {
	Name    string
	Content []byte
}

// Session is the minimal command-execution surface a driver needs. The
// transport layer (SSH, Telnet, ...) implements it.
type Session interface {
	// Run executes a single command and returns the captured output.
	// Implementations strip the trailing prompt and pager artefacts.
	Run(ctx context.Context, cmd string) (string, error)
}

// Driver is implemented by every device driver.
type Driver interface {
	Name() string
	FetchConfig(ctx context.Context, sess Session) ([]ConfigArtifact, error)
}

var (
	regMu sync.RWMutex
	reg   = map[string]Driver{}
)

// Register adds a driver to the global registry. It is intended to be
// called from init() functions of builtin driver packages. Registering the
// same name twice panics — drivers must have unique names.
func Register(d Driver) {
	regMu.Lock()
	defer regMu.Unlock()
	name := d.Name()
	if _, exists := reg[name]; exists {
		panic(fmt.Sprintf("drivers: duplicate registration of %q", name))
	}
	reg[name] = d
}

// Get returns the driver registered under name, or an error if none.
func Get(name string) (Driver, error) {
	regMu.RLock()
	defer regMu.RUnlock()
	d, ok := reg[name]
	if !ok {
		return nil, fmt.Errorf("drivers: unknown driver %q", name)
	}
	return d, nil
}

// List returns the names of all registered drivers, sorted.
func List() []string {
	regMu.RLock()
	defer regMu.RUnlock()
	out := make([]string, 0, len(reg))
	for n := range reg {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// reset is a test helper.
func reset() {
	regMu.Lock()
	defer regMu.Unlock()
	reg = map[string]Driver{}
}
