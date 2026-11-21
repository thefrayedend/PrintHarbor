// Package store keeps small JSON documents on disk. It is deliberately simple:
// PrintHarbor's state (printers, projects, jobs) is tiny and human-readable.
package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

type Store struct {
	dir string
	mu  sync.Mutex
}

func New(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Store{dir: dir}, nil
}

func (s *Store) Dir() string { return s.dir }

// Path returns a path inside the data dir, creating parent folders.
func (s *Store) Path(parts ...string) string {
	p := filepath.Join(append([]string{s.dir}, parts...)...)
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	return p
}

// Save writes v as name.json atomically.
func (s *Store) Save(name string, v any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	p := s.Path(name + ".json")
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// Load reads name.json into v. A missing file leaves v untouched.
func (s *Store) Load(name string, v any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := os.ReadFile(filepath.Join(s.dir, name+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}
