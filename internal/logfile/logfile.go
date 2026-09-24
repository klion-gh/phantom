// Package logfile is an append-only log kept as hourly segment files, holding
// only the last Retention of history.
//
// Hourly segments rather than one growing file: expiring old history is then
// deleting whole files, which is instant and can't corrupt the part being
// kept - trimming the front of a single file means rewriting all of it, on
// every trim, while it is still being appended to. The Android client keeps
// the same layout in FileLog.kt.
package logfile

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// Retention is how much history is kept.
	Retention = 24 * time.Hour
	// maxSegmentBytes stops a single hour from growing without bound if
	// something starts logging in a tight loop - the rest of that hour is
	// dropped (with one line saying so) rather than filling the disk.
	maxSegmentBytes = 20 << 20
	prefix          = "phantom-"
	suffix          = ".log"
	// Segment names use UTC, so a daylight-saving change can't make two
	// hours share a name or the retention check misjudge a segment's age.
	stamp = "20060102-15"
)

// Writer appends to the current hour's segment, switching segments on the
// hour and deleting any older than Retention as it does. Safe for concurrent
// use (the standard log package serialises its own writes anyway).
type Writer struct {
	dir string
	now func() time.Time

	mu      sync.Mutex
	hour    string
	f       *os.File
	written int64
	capped  bool
}

// Open prepares dir for writing and prunes whatever has already expired.
func Open(dir string) (*Writer, error) {
	return open(dir, time.Now)
}

func open(dir string, now func() time.Time) (*Writer, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	w := &Writer{dir: dir, now: now}
	prune(dir, now())
	return w, nil
}

func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if err := w.rollLocked(); err != nil {
		return 0, err
	}
	if w.capped {
		return len(p), nil // swallowed on purpose - see maxSegmentBytes
	}
	if w.written+int64(len(p)) > maxSegmentBytes {
		w.capped = true
		msg := fmt.Sprintf("%s logfile: this hour's segment reached %d MB - dropping further lines until the next hour\n",
			w.now().Format("2006/01/02 15:04:05"), maxSegmentBytes>>20)
		w.f.WriteString(msg)
		return len(p), nil
	}
	n, err := w.f.Write(p)
	w.written += int64(n)
	return n, err
}

// rollLocked moves to the current hour's segment if the hour has changed
// since the last write, pruning expired segments as it does.
func (w *Writer) rollLocked() error {
	now := w.now()
	hour := now.UTC().Format(stamp)
	if w.f != nil && hour == w.hour {
		return nil
	}
	if w.f != nil {
		w.f.Close()
		w.f = nil
	}
	f, err := os.OpenFile(filepath.Join(w.dir, prefix+hour+suffix), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	info, _ := f.Stat()
	w.f = f
	w.hour = hour
	w.written = 0
	if info != nil {
		w.written = info.Size()
	}
	w.capped = w.written >= maxSegmentBytes
	prune(w.dir, now)
	return nil
}

func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}

// segments lists dir's segment files oldest first, with the hour each covers.
func segments(dir string) ([]string, []time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil
	}
	type seg struct {
		name string
		hour time.Time
	}
	var segs []seg
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, suffix) {
			continue
		}
		hour, err := time.Parse(stamp, strings.TrimSuffix(strings.TrimPrefix(name, prefix), suffix))
		if err != nil {
			continue
		}
		segs = append(segs, seg{name, hour})
	}
	sort.Slice(segs, func(i, j int) bool { return segs[i].hour.Before(segs[j].hour) })
	names := make([]string, len(segs))
	hours := make([]time.Time, len(segs))
	for i, s := range segs {
		names[i] = filepath.Join(dir, s.name)
		hours[i] = s.hour
	}
	return names, hours
}

// prune deletes every segment whose whole hour ended more than Retention ago.
// A segment still partly inside the window is kept whole, so the log always
// covers at least the last Retention and at most one hour more.
func prune(dir string, now time.Time) {
	names, hours := segments(dir)
	cutoff := now.Add(-Retention)
	for i, name := range names {
		if hours[i].Add(time.Hour).Before(cutoff) {
			os.Remove(name)
		}
	}
}

// ReadAll returns every retained segment, oldest first.
func ReadAll(dir string) (string, error) {
	var b strings.Builder
	if err := copyAll(dir, &b); err != nil {
		return "", err
	}
	return b.String(), nil
}

// ReadTail returns at most the last maxBytes of the retained log, starting at
// a line boundary. What the in-app viewer shows: a day of diagnostics is too
// much text for a UI to render, and the newest part is what's being looked at.
func ReadTail(dir string, maxBytes int) (string, error) {
	names, _ := segments(dir)
	if len(names) == 0 {
		return "", ErrEmpty
	}
	var chunks []string
	total := 0
	for i := len(names) - 1; i >= 0 && total < maxBytes; i-- {
		data, err := os.ReadFile(names[i])
		if err != nil {
			continue
		}
		chunks = append(chunks, string(data))
		total += len(data)
	}
	var b strings.Builder
	for i := len(chunks) - 1; i >= 0; i-- {
		b.WriteString(chunks[i])
	}
	s := b.String()
	if len(s) > maxBytes {
		s = s[len(s)-maxBytes:]
		if nl := strings.IndexByte(s, '\n'); nl >= 0 {
			s = s[nl+1:]
		}
	}
	return s, nil
}

// ErrEmpty means there is no log yet.
var ErrEmpty = errors.New("logfile: no log yet")

func copyAll(dir string, w io.Writer) error {
	names, _ := segments(dir)
	if len(names) == 0 {
		return ErrEmpty
	}
	for _, name := range names {
		f, err := os.Open(name)
		if err != nil {
			continue
		}
		io.Copy(w, f)
		f.Close()
	}
	return nil
}
