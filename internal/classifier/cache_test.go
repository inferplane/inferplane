package classifier

import (
	"testing"
	"time"
)

func TestCacheHitsUntilTTL(t *testing.T) {
	now := time.Unix(1000, 0)
	c, err := newVerdictCache(4, time.Minute, func() time.Time { return now })
	if err != nil || c == nil {
		t.Fatal(c, err)
	}
	k := c.digest("kr", &Entry{Name: "laya"}, nil, "hello")
	if c.clear(k) {
		t.Fatal("hit on an empty cache")
	}
	c.store(k)
	if !c.clear(k) {
		t.Fatal("a stored key missed")
	}
	now = now.Add(time.Minute)
	if c.clear(k) || c.len() != 0 {
		t.Fatal("an expired key hit or stayed resident")
	}
}

func TestCacheEvictsLeastRecentlyUsed(t *testing.T) {
	c, _ := newVerdictCache(2, time.Hour, time.Now)
	e := &Entry{Name: "laya"}
	a, b, d := c.digest("t", e, nil, "a"), c.digest("t", e, nil, "b"), c.digest("t", e, nil, "d")
	c.store(a)
	c.store(b)
	c.clear(a) // a is now the most recent
	c.store(d) // evicts b
	if !c.clear(a) || c.clear(b) || !c.clear(d) || c.len() != 2 {
		t.Fatal("LRU order not respected")
	}
}

func TestCacheKeysSeparateEveryScope(t *testing.T) {
	c, _ := newVerdictCache(10, time.Hour, time.Now)
	e := &Entry{Name: "laya", Settings: [32]byte{1}}
	pol := []PolicyRef{{Name: "p", Generation: 1, Rule: "r"}}
	base := c.digest("kr", e, pol, "text")
	renamed, retuned := *e, *e
	renamed.Name = "other"
	retuned.Settings = [32]byte{2}
	for name, k := range map[string][32]byte{
		"team":              c.digest("us", e, pol, "text"),
		"entry":             c.digest("kr", &renamed, pol, "text"),
		"settings":          c.digest("kr", &retuned, pol, "text"),
		"policy generation": c.digest("kr", e, []PolicyRef{{Name: "p", Generation: 2, Rule: "r"}}, "text"),
		"no policy":         c.digest("kr", e, nil, "text"),
		"text":              c.digest("kr", e, pol, "text2"),
		"field boundary":    c.digest("k", e, pol, "rtext"),
	} {
		if k == base {
			t.Errorf("a %s change kept the key", name)
		}
	}
	other, _ := newVerdictCache(10, time.Hour, time.Now)
	if other.digest("kr", e, pol, "text") == base {
		t.Fatal("two generations share a key")
	}
}

func TestCacheDisabledAtZero(t *testing.T) {
	c, err := newVerdictCache(0, time.Hour, time.Now)
	if c != nil || err != nil {
		t.Fatal("max_entries 0 must disable the cache")
	}
}
