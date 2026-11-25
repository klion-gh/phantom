package main

import (
	_ "embed"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"phantom/internal/geoip"
)

// Task is one card on the protocol-improvement board.
type Task struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Priority  string `json:"priority"` // P0..P3
	Area      string `json:"area"`
	Status    string `json:"status"` // todo | doing | done
	Files     string `json:"files,omitempty"`
	Detail    string `json:"detail,omitempty"`
	Order     int64  `json:"order"`
	UpdatedAt string `json:"updatedAt,omitempty"`
}

// News is one report about TSPU blocking, with an analysis of what it means
// for Phantom. Written by Claude on request, not fetched by the console: the
// analysis is the point, and a feed reader can't produce it.
type News struct {
	ID         string       `json:"id"`
	Date       string       `json:"date"` // YYYY-MM-DD of the event or report
	Title      string       `json:"title"`
	Impact     string       `json:"impact"`     // high | medium | low: how much it bears on Phantom
	Confidence string       `json:"confidence"` // confirmed | partial | rumour
	Status     string       `json:"status"`     // new | covered: covered = Phantom already accounts for it
	Summary    string       `json:"summary"`    // what happened
	ForPhantom string       `json:"forPhantom"` // what it means for the protocol and the apps
	Actions    []NewsAction `json:"actions,omitempty"`
	Sources    []NewsSource `json:"sources,omitempty"`
}

// NewsAction is one thing to do about a report. TaskID links it to a card
// on the board, either one that already existed or one added from the news.
type NewsAction struct {
	Text     string `json:"text"`
	TaskID   string `json:"taskId,omitempty"`
	Priority string `json:"priority,omitempty"` // for adding it as a task
	Area     string `json:"area,omitempty"`
	Files    string `json:"files,omitempty"`
}

type NewsSource struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// Check is the outcome of the last "Проверить" for one saved config.
type Check struct {
	At        string `json:"at"`
	Status    string `json:"status"` // ok | slow | fail
	LatencyMs int64  `json:"latencyMs,omitempty"`
	IP        string `json:"ip,omitempty"`
	Kind      string `json:"kind,omitempty"` // for fail: timeout | reset | refused | dns | auth | other
	Error     string `json:"error,omitempty"`
}

// state is everything the console keeps of its own. The app's saved configs
// stay in its configs.json, which the console only reads; Configs holds the
// ones added in the console itself, yaml and all, which is why the file is
// written 0600.
type state struct {
	Configs []savedConfig             `json:"configs,omitempty"`
	Geo     map[string]geoip.Location `json:"geo"`    // by IP: looked up once, kept for good
	Checks  map[string]Check          `json:"checks"` // by config id
	Tasks   []Task                    `json:"tasks"`
	Seeded  bool                      `json:"seeded"`

	News          []News `json:"news,omitempty"`
	NewsUpdatedAt string `json:"newsUpdatedAt,omitempty"`
}

//go:embed tasks.json
var defaultTasks []byte

type store struct {
	mu   sync.Mutex
	path string
	st   state
}

func openStore(path string) (*store, error) {
	s := &store{path: path}
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		if err := json.Unmarshal(data, &s.st); err != nil {
			return nil, err
		}
	}
	if s.st.Geo == nil {
		s.st.Geo = map[string]geoip.Location{}
	}
	if s.st.Checks == nil {
		s.st.Checks = map[string]Check{}
	}
	// The board starts with the roadmap once; after that an emptied board
	// stays empty.
	if !s.st.Seeded {
		if err := json.Unmarshal(defaultTasks, &s.st.Tasks); err != nil {
			return nil, err
		}
		s.st.Seeded = true
		if err := s.saveLocked(); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// saveLocked writes through a temp file so a crash mid-write can't leave
// half a JSON document behind. Caller holds mu.
func (s *store) saveLocked() error {
	data, err := json.MarshalIndent(s.st, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *store) geo(ip string) (geoip.Location, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	loc, ok := s.st.Geo[ip]
	return loc, ok
}

func (s *store) setGeo(ip string, loc geoip.Location) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.st.Geo[ip] = loc
	return s.saveLocked()
}

func (s *store) check(id string) (Check, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.st.Checks[id]
	return c, ok
}

func (s *store) setCheck(id string, c Check) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.st.Checks[id] = c
	return s.saveLocked()
}

func (s *store) configs() []savedConfig {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]savedConfig(nil), s.st.Configs...)
}

func (s *store) addConfig(c savedConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.st.Configs = append(s.st.Configs, c)
	return s.saveLocked()
}

// deleteConfig removes a console-added config and what was recorded about
// it. Reports false when id is not one of the console's own.
func (s *store) deleteConfig(id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, c := range s.st.Configs {
		if c.ID == id {
			s.st.Configs = append(s.st.Configs[:i], s.st.Configs[i+1:]...)
			delete(s.st.Checks, id)
			return true, s.saveLocked()
		}
	}
	return false, nil
}

func (s *store) news() ([]News, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]News(nil), s.st.News...), s.st.NewsUpdatedAt
}

// putNews inserts n, or replaces the item with the same id. touch marks the
// news as refreshed now - set when a report is added or rewritten, not when
// only an action is linked to a task.
func (s *store) putNews(n News, touch bool, now string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if touch {
		s.st.NewsUpdatedAt = now
	}
	for i := range s.st.News {
		if s.st.News[i].ID == n.ID {
			s.st.News[i] = n
			return s.saveLocked()
		}
	}
	s.st.News = append(s.st.News, n)
	return s.saveLocked()
}

func (s *store) deleteNews(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.st.News[:0]
	for _, n := range s.st.News {
		if n.ID != id {
			kept = append(kept, n)
		}
	}
	s.st.News = kept
	return s.saveLocked()
}

func (s *store) tasks() []Task {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Task(nil), s.st.Tasks...)
}

// putTask inserts t, or replaces the task with the same id.
func (s *store) putTask(t Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.st.Tasks {
		if s.st.Tasks[i].ID == t.ID {
			s.st.Tasks[i] = t
			return s.saveLocked()
		}
	}
	s.st.Tasks = append(s.st.Tasks, t)
	return s.saveLocked()
}

func (s *store) deleteTask(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.st.Tasks[:0]
	for _, t := range s.st.Tasks {
		if t.ID != id {
			kept = append(kept, t)
		}
	}
	s.st.Tasks = kept
	return s.saveLocked()
}
