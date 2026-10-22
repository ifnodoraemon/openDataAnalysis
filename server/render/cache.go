package render

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
)

// Cache stores encoded slice PNGs so an unchanged block is not re-encoded and
// re-sent through the capture path.
type Cache interface {
	Get(key string) ([]byte, bool)
	Put(key string, value []byte)
}

// MemoryCache is a bounded FIFO cache. It lives in process memory because slices
// are rebuildable: losing them costs a crop, not correctness.
type MemoryCache struct {
	mu         sync.Mutex
	maxEntries int
	entries    map[string][]byte
	order      []string
}

func NewMemoryCache(maxEntries int) *MemoryCache {
	if maxEntries <= 0 {
		maxEntries = 256
	}
	return &MemoryCache{
		maxEntries: maxEntries,
		entries:    make(map[string][]byte, maxEntries),
	}
}

func (c *MemoryCache) Get(key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	value, ok := c.entries[key]
	return value, ok
}

func (c *MemoryCache) Put(key string, value []byte) {
	if key == "" || len(value) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.entries[key]; exists {
		c.entries[key] = value
		return
	}
	for len(c.order) >= c.maxEntries {
		oldest := c.order[0]
		c.order = c.order[1:]
		delete(c.entries, oldest)
	}
	c.entries[key] = value
	c.order = append(c.order, key)
}

// SliceKey identifies one slice image. It must include everything that changes
// the pixels: report version, block identity, slice position, and the block's own
// content hash when the caller has one. Version alone is correct but coarse:
// every mutation invalidates the whole report, so a content hash is what makes
// dirty-block reuse possible.
func SliceKey(parts ...string) string {
	digest := sha256.New()
	for _, part := range parts {
		digest.Write([]byte(part))
		digest.Write([]byte{0})
	}
	return hex.EncodeToString(digest.Sum(nil))[:32]
}

// IsZeroKey reports whether a key is unusable, so callers can skip caching
// instead of silently colliding every slice on the empty string.
func IsZeroKey(key string) bool {
	return strings.TrimSpace(key) == ""
}
