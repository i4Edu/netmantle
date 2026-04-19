// Package logging configures the application logger.
//
// We use the standard library log/slog so we don't carry a third-party
// logging dependency. The package exposes a single Setup helper that
// returns a slog.Logger configured for either text (development) or
// JSON (production) output.
package logging

import (
	"io"
	"log/slog"
	"os"
	"strings"
)

// Setup builds a slog.Logger from a textual level ("debug", "info", "warn",
// "error") and format ("text" or "json"). Unknown values fall back to info
// + json, which matches the production default.
func Setup(level, format string) *slog.Logger {
	return SetupTo(os.Stdout, level, format)
}

// SetupTo is like Setup but writes to the supplied writer; useful in tests.
func SetupTo(w io.Writer, level, format string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{Level: lvl}
	var h slog.Handler
	switch strings.ToLower(format) {
	case "text":
		h = slog.NewTextHandler(w, opts)
	default:
		h = slog.NewJSONHandler(w, opts)
	}
	return slog.New(h)
}
