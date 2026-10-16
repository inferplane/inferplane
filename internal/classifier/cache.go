package classifier

import (
	"container/list"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"hash"
	"strconv"
	"sync"
	"time"
)

// PolicyRef identifies an applicable privacy rule; part of every cache key.
type PolicyRef struct {
	Name       string
	Generation int64
	Rule       string
}

// verdictCache remembers which units a classifier already judged clear. Keys
// are HMAC-SHA256 under a per-generation random key, so neither a key nor its
// presence means anything outside this generation. It stores no text and no
// verdict other than Clear.
type verdictCache struct {
	mu    sync.Mutex
	key   []byte
	max   int
	ttl   time.Duration
	now   func() time.Time
	order *list.List // front = most recently used
	items map[[32]byte]*list.Element
}

type cacheItem struct {
	key [32]byte
	at  time.Time
}

// newVerdictCache returns nil, nil when max <= 0 (cache disabled).
func newVerdictCache(max int, ttl time.Duration, now func() time.Time) (*verdictCache, error) {
	if max <= 0 {
		return nil, nil
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("classifier: verdict cache key: %w", err)
	}
	return &verdictCache{key: key, max: max, ttl: ttl, now: now, order: list.New(), items: make(map[[32]byte]*list.Element)}, nil
}

func (c *verdictCache) digest(team string, e *Entry, policies []PolicyRef, text string) [32]byte {
	m := hmac.New(sha256.New, c.key)
	field(m, team)
	field(m, e.Name)
	field(m, string(e.Settings[:]))
	field(m, strconv.Itoa(len(policies)))
	for _, p := range policies {
		field(m, p.Name)
		field(m, strconv.FormatInt(p.Generation, 10))
		field(m, p.Rule)
	}
	field(m, text)
	var out [32]byte
	copy(out[:], m.Sum(nil))
	return out
}

// field writes a length-prefixed value so adjacent fields cannot run together.
func field(h hash.Hash, s string) {
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(len(s)))
	h.Write(n[:])
	h.Write([]byte(s))
}

// clear reports a live hit and refreshes its recency; an expired entry is removed.
func (c *verdictCache) clear(k [32]byte) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[k]
	if !ok {
		return false
	}
	if c.now().Sub(el.Value.(*cacheItem).at) >= c.ttl {
		c.order.Remove(el)
		delete(c.items, k)
		return false
	}
	c.order.MoveToFront(el)
	return true
}

func (c *verdictCache) store(k [32]byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[k]; ok {
		el.Value.(*cacheItem).at = c.now()
		c.order.MoveToFront(el)
		return
	}
	c.items[k] = c.order.PushFront(&cacheItem{key: k, at: c.now()})
	for c.order.Len() > c.max {
		last := c.order.Back()
		c.order.Remove(last)
		delete(c.items, last.Value.(*cacheItem).key)
	}
}

func (c *verdictCache) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
}
