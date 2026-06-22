package media

import (
	"context"
	"fmt"
	"sync"

	"radio_engine/internal/playlist"
)

// MemoryStore is useful for deterministic tests and local embedding.
type MemoryStore struct {
	mu     sync.RWMutex
	assets map[string]Asset
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{assets: map[string]Asset{}}
}

func (s *MemoryStore) Put(ref playlist.MediaRef, asset Asset) {
	s.mu.Lock()
	s.assets[cacheKey(ref)] = asset
	s.mu.Unlock()
}

func (s *MemoryStore) Resolve(ctx context.Context, ref playlist.MediaRef) (Asset, error) {
	if err := ctx.Err(); err != nil {
		return Asset{}, err
	}
	s.mu.RLock()
	asset, ok := s.assets[cacheKey(ref)]
	s.mu.RUnlock()
	if !ok {
		return Asset{}, fmt.Errorf("media not found: s3://%s/%s", ref.Bucket, ref.ObjectKey)
	}
	return asset, nil
}

func (s *MemoryStore) Ping(context.Context) error { return nil }
