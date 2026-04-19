// Package fakesession provides a Session implementation backed by a static
// command/response map, for use in driver unit tests.
package fakesession

import (
	"context"
	"fmt"
)

// Session implements drivers.Session using a deterministic map.
type Session struct {
	Responses map[string]string
	Calls     []string
}

// New constructs a Session from a command->response map. Callers may
// inspect Calls after the test to verify command order.
func New(responses map[string]string) *Session {
	return &Session{Responses: responses}
}

// Run records the command and returns the configured response, or an error
// if the test forgot to register one.
func (s *Session) Run(ctx context.Context, cmd string) (string, error) {
	s.Calls = append(s.Calls, cmd)
	if r, ok := s.Responses[cmd]; ok {
		return r, nil
	}
	return "", fmt.Errorf("fakesession: no response registered for %q", cmd)
}
