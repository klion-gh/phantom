//go:build !windows

package mobile

import (
	"log"
	"strings"
	"sync"
)

// LogSink receives every line the Go core logs. Implemented on the Kotlin
// side by writing into the app's own log file (FileLog).
//
// Without it the core's output - tunnel stalls, dropped connections, DNS
// going unanswered, routing decisions (internal/diag) - went only to logcat:
// visible over adb, but not in the log a user can open and share from the
// phone, which is the only log there is for a bug that happens out in the
// world rather than on a desk.
type LogSink interface {
	Log(line string)
}

// SetLogSink routes the standard logger - which every internal package logs
// through - into sink. Call once, early (Application.onCreate), so lines from
// before any tunnel exists are captured too. Timestamps are left to the sink,
// which stamps every line of the file the same way.
func SetLogSink(sink LogSink) {
	if sink == nil {
		return
	}
	log.SetFlags(0)
	log.SetOutput(&sinkWriter{sink: sink})
}

type sinkWriter struct {
	mu   sync.Mutex
	sink LogSink
}

func (w *sinkWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		if line != "" {
			w.sink.Log(line)
		}
	}
	return len(p), nil
}
