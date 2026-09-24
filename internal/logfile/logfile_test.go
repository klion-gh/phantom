package logfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The whole point of the package: history older than Retention is gone, the
// last Retention of it is not.
func TestOldSegmentsAreDeletedRecentOnesKept(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 9, 24, 12, 30, 0, 0, time.UTC)

	for _, ago := range []time.Duration{26 * time.Hour, 25 * time.Hour, 23 * time.Hour, time.Hour} {
		name := prefix + now.Add(-ago).Format(stamp) + suffix
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	w, err := open(dir, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	names, _ := segments(dir)
	if len(names) != 2 {
		t.Fatalf("expected only the 23h- and 1h-old segments to survive, got %v", names)
	}
}

// Writing across an hour boundary starts a new segment and expires whatever
// fell out of the window meanwhile - a log left running for days must stay
// at about a day, not only be trimmed at startup.
func TestRollingOverAnHourPrunesAsItGoes(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	w, err := open(dir, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	for h := 0; h < 30; h++ {
		if _, err := w.Write([]byte("line\n")); err != nil {
			t.Fatal(err)
		}
		now = now.Add(time.Hour)
	}
	w.Write([]byte("last\n"))

	names, _ := segments(dir)
	if len(names) > 26 {
		t.Fatalf("expected about a day of hourly segments, got %d", len(names))
	}
	all, err := ReadAll(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(all, "last\n") {
		t.Fatalf("expected the newest line at the end, got %q", all[max(0, len(all)-40):])
	}
}

// The viewer's tail starts on a whole line and ends with the newest one.
func TestReadTailStartsOnALineBoundary(t *testing.T) {
	dir := t.TempDir()
	w, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		w.Write([]byte("0123456789 line\n"))
	}
	w.Write([]byte("newest\n"))
	w.Close()

	tail, err := ReadTail(dir, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(tail) > 100 || !strings.HasSuffix(tail, "newest\n") || !strings.HasPrefix(tail, "0123456789") {
		t.Fatalf("unexpected tail %q", tail)
	}
}
