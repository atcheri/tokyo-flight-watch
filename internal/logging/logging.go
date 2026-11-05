// Package logging sets up the structured logger shared by the commands.
//
// Environment:
//
//	LOG_FORMAT  text (default, key=value lines) or json
//	LOG_LEVEL   debug, info (default), warn or error
package logging

import (
	"io"
	"log/slog"
	"os"
	"strings"
)

// Setup installs the default slog logger, writing to stderr.
func Setup() {
	slog.SetDefault(New(os.Stderr, os.Getenv("LOG_FORMAT"), os.Getenv("LOG_LEVEL")))
}

// New builds a logger for the given format and level names; unknown names fall back to text and info.
func New(w io.Writer, format, level string) *slog.Logger {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(strings.TrimSpace(level))); err != nil {
		lvl = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: lvl}
	if strings.EqualFold(strings.TrimSpace(format), "json") {
		return slog.New(slog.NewJSONHandler(w, opts))
	}
	return slog.New(slog.NewTextHandler(w, opts))
}

// Fatal logs msg at error level with the given attributes and exits with status 1.
func Fatal(msg string, args ...any) {
	slog.Error(msg, args...)
	os.Exit(1)
}
