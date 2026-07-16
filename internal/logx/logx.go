// Package logx is a tiny level filter on top of the standard log package. The
// server sets the level once from server.yaml's log_level (previously parsed
// but never actually read - see the config note); call sites that would
// otherwise be noisy on a busy server route through here instead of calling
// log.Printf directly, so an operator can quiet routine per-connection chatter
// down to warnings/errors, or turn it up to debug when diagnosing.
//
// Deliberately narrow: client-shared internal packages (the multiplexer,
// connpool, etc.) still log unconditionally via the standard log package -
// only server-side code that repeats per connection was moved over, to keep
// the change small and avoid touching the mobile/Windows log paths.
package logx

import (
	"log"
	"strings"
	"sync/atomic"
)

type Level int32

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

// current holds the active threshold; messages below it are dropped. Defaults
// to Info so a server started before SetLevel (or with log_level unset) behaves
// exactly as it did before this package existed for anything at Info or above.
var current atomic.Int32

func init() { current.Store(int32(LevelInfo)) }

// SetLevel sets the minimum level that will be emitted. Unknown/empty names map
// to Info, matching the config default.
func SetLevel(name string) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "debug":
		current.Store(int32(LevelDebug))
	case "warn", "warning":
		current.Store(int32(LevelWarn))
	case "error":
		current.Store(int32(LevelError))
	default:
		current.Store(int32(LevelInfo))
	}
}

func enabled(l Level) bool { return int32(l) >= current.Load() }

func Debugf(format string, args ...any) {
	if enabled(LevelDebug) {
		log.Printf(format, args...)
	}
}

func Infof(format string, args ...any) {
	if enabled(LevelInfo) {
		log.Printf(format, args...)
	}
}

func Warnf(format string, args ...any) {
	if enabled(LevelWarn) {
		log.Printf(format, args...)
	}
}

func Errorf(format string, args ...any) {
	if enabled(LevelError) {
		log.Printf(format, args...)
	}
}
