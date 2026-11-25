package main

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// The dashboard's "История" section is HISTORY.md at the repository root, the
// log every Claude session keeps (see CLAUDE.md). It's read on every request
// rather than cached, so an entry written by another session shows up on the
// next reload; the page never writes it.

// HistoryEntry is one top-level "- " item: its date (when the item starts
// with YYYY-MM-DD) and the rest of it as markdown, nested lines included.
type HistoryEntry struct {
	Date string `json:"date"`
	Text string `json:"text"`
}

type History struct {
	Path     string         `json:"path"`
	Actions  []HistoryEntry `json:"actions"`
	Versions []HistoryEntry `json:"versions"`
}

// findHistory returns path if given, otherwise the first HISTORY.md found
// walking up from the working directory and then from the executable's -
// `go run ./cmd/console` from the repo and a built console.exe in bin/ both
// find it.
func findHistory(path string) string {
	if path != "" {
		return path
	}
	var starts []string
	if wd, err := os.Getwd(); err == nil {
		starts = append(starts, wd)
	}
	if exe, err := os.Executable(); err == nil {
		starts = append(starts, filepath.Dir(exe))
	}
	for _, dir := range starts {
		for {
			p := filepath.Join(dir, "HISTORY.md")
			if _, err := os.Stat(p); err == nil {
				return p
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	return ""
}

var historyDate = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})\s*(?:[—–-]\s*)?`)

func readHistory(path string) (History, error) {
	h := History{Path: path, Actions: []HistoryEntry{}, Versions: []HistoryEntry{}}
	if path == "" {
		return h, errors.New("HISTORY.md not found - run the console from the repository, or pass -history")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return h, err
	}
	var section *[]HistoryEntry
	var cur []string
	flush := func() {
		if section != nil && len(cur) > 0 {
			first := strings.TrimPrefix(cur[0], "- ")
			e := HistoryEntry{}
			if m := historyDate.FindStringSubmatch(first); m != nil {
				e.Date = m[1]
				first = first[len(m[0]):]
			}
			lines := append([]string{first}, cur[1:]...)
			e.Text = strings.TrimRight(strings.Join(lines, "\n"), "\n ")
			*section = append(*section, e)
		}
		cur = nil
	}
	for _, line := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		switch {
		case strings.HasPrefix(line, "## "):
			flush()
			switch strings.TrimSpace(line[3:]) {
			case "Действия":
				section = &h.Actions
			case "Версии":
				section = &h.Versions
			default:
				section = nil
			}
		case strings.HasPrefix(line, "- "):
			flush()
			cur = []string{line}
		case cur != nil && (strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")):
			cur = append(cur, strings.TrimPrefix(strings.TrimPrefix(line, "  "), "\t"))
		case cur != nil && strings.TrimSpace(line) == "":
			// a blank line inside an item is kept; trailing ones are trimmed
			cur = append(cur, "")
		default:
			flush()
		}
	}
	flush()
	return h, nil
}
