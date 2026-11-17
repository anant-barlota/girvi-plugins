package sandbox

import (
	"context"
	"maps"
	"sync"
	"time"
)

// MemoryStore is a Store in memory, for tests and the harness.
type MemoryStore struct {
	mu      sync.Mutex
	values  map[string][]byte
	pending int64
}

// NewMemoryStore starts with a copy of values, which may be nil.
func NewMemoryStore(values map[string][]byte) *MemoryStore {
	s := &MemoryStore{values: map[string][]byte{}}
	for k, v := range values {
		s.values[k] = append([]byte{}, v...)
	}
	return s
}

func (s *MemoryStore) Get(_ context.Context, key string) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.values[key]
	return v, ok, nil
}

func (s *MemoryStore) Set(_ context.Context, key string, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[key] = value
	return nil
}

func (s *MemoryStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.values, key)
	return nil
}

func (s *MemoryStore) Usage(context.Context) (keys, size int64, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, v := range s.values {
		size += int64(len(k) + len(v))
	}
	return int64(len(s.values)), size, nil
}

func (s *MemoryStore) PendingWakeUps(context.Context) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pending, nil
}

func (s *MemoryStore) WakeAt(context.Context, time.Time, string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending++
	return nil
}

// Values is a copy of what the store holds.
func (s *MemoryStore) Values() map[string][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return maps.Clone(s.values)
}
