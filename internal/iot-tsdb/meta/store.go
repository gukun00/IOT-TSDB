// Package meta persists point (series) metadata — description, unit, range,
// group — to a JSON file outside the tsdb data directory. The file is loaded
// once at startup and rewritten atomically on every mutation.
package meta

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/prometheus/prometheus/model/labels"
)

// PointMeta holds user-editable metadata for one point (series).
type PointMeta struct {
	Description string   `json:"description,omitempty"`
	Unit        string   `json:"unit,omitempty"`
	RangeMin    *float64 `json:"range_min,omitempty"`
	RangeMax    *float64 `json:"range_max,omitempty"`
	Group       string   `json:"group,omitempty"`
}

// Key builds the canonical point key from metric name and label set:
// "metric|k1=v1|k2=v2" with labels sorted by name. Readable and stable.
func Key(metric string, lset labels.Labels) string {
	var b strings.Builder
	b.WriteString(metric)
	lset.Range(func(l labels.Label) {
		if l.Name == labels.MetricName {
			return
		}
		b.WriteByte('|')
		b.WriteString(l.Name)
		b.WriteByte('=')
		b.WriteString(l.Value)
	})
	return b.String()
}

// ParseKey splits a key back into metric name and label pairs. Labels are
// returned sorted by name (as they were encoded).
func ParseKey(key string) (metric string, pairs [][2]string, err error) {
	parts := strings.Split(key, "|")
	if len(parts) == 0 || parts[0] == "" {
		return "", nil, fmt.Errorf("invalid point key %q", key)
	}
	metric = parts[0]
	for _, p := range parts[1:] {
		kv := strings.SplitN(p, "=", 2)
		if len(kv) != 2 {
			return "", nil, fmt.Errorf("invalid label pair %q in key %q", p, key)
		}
		pairs = append(pairs, [2]string{kv[0], kv[1]})
	}
	return metric, pairs, nil
}

// LabelsFromKey reconstructs a full label set (including __name__) from a key.
func LabelsFromKey(key string) (labels.Labels, error) {
	metric, pairs, err := ParseKey(key)
	if err != nil {
		return labels.EmptyLabels(), err
	}
	lb := labels.NewBuilder(labels.EmptyLabels())
	lb.Set(labels.MetricName, metric)
	for _, kv := range pairs {
		lb.Set(kv[0], kv[1])
	}
	return lb.Labels(), nil
}

// Store is a concurrency-safe in-memory map with JSON file persistence.
type Store struct {
	mu    sync.RWMutex
	path  string
	items map[string]PointMeta
}

// Open loads (or initializes) the store at path.
func Open(path string) (*Store, error) {
	s := &Store{path: path, items: map[string]PointMeta{}}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, fmt.Errorf("read pointmeta: %w", err)
	}
	if len(data) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(data, &s.items); err != nil {
		return nil, fmt.Errorf("parse pointmeta %s: %w", path, err)
	}
	return s, nil
}

// Get returns metadata for a key.
func (s *Store) Get(key string) (PointMeta, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	m, ok := s.items[key]
	return m, ok
}

// All returns a copy of the full map.
func (s *Store) All() map[string]PointMeta {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]PointMeta, len(s.items))
	for k, v := range s.items {
		out[k] = v
	}
	return out
}

// Len returns the number of stored entries.
func (s *Store) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.items)
}

// Upsert inserts or updates metadata for key and persists.
func (s *Store) Upsert(key string, m PointMeta) (created bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, existed := s.items[key]
	s.items[key] = m
	return !existed, s.saveLocked()
}

// Delete removes metadata for key and persists. Missing key is not an error.
func (s *Store) Delete(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.items[key]; !ok {
		return nil
	}
	delete(s.items, key)
	return s.saveLocked()
}

// ReplaceAll swaps the whole map (used by restore) and persists.
func (s *Store) ReplaceAll(items map[string]PointMeta) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if items == nil {
		items = map[string]PointMeta{}
	}
	s.items = items
	return s.saveLocked()
}

// Groups returns sorted distinct group names.
func (s *Store) Groups() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	seen := map[string]struct{}{}
	for _, m := range s.items {
		if m.Group != "" {
			seen[m.Group] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for g := range seen {
		out = append(out, g)
	}
	sort.Strings(out)
	return out
}

// saveLocked writes the map atomically: tmp file, then remove+rename
// (Windows os.Rename fails if the destination exists).
func (s *Store) saveLocked() error {
	data, err := json.MarshalIndent(s.items, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o777); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o666); err != nil {
		return err
	}
	if err := os.Remove(s.path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.Rename(tmp, s.path)
}
