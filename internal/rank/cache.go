package rank

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/timurcravtov/walrus/internal/recommend"
)

// snapshotCache keeps recent snapshots for a short time, oldest dropped first when it is full.
// Anything written to the store changes its version and so every key, so a cached snapshot never
// outlives the data it was built from; the time limit only bounds how stale "now" can get for
// time-based signals.
type snapshotCache struct {
	mu      sync.Mutex
	max     int
	ttl     time.Duration
	entries map[string]cached
	order   []string
}

type cached struct {
	snap *snapshot
	at   time.Time
}

func newSnapshotCache(max int, ttl time.Duration) *snapshotCache {
	return &snapshotCache{max: max, ttl: ttl, entries: map[string]cached{}}
}

func (c *snapshotCache) get(key string, now time.Time) (*snapshot, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || now.Sub(e.at) > c.ttl {
		return nil, false
	}
	return e.snap, true
}

func (c *snapshotCache) put(key string, s *snapshot, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.entries[key]; !ok {
		c.order = append(c.order, key)
	}
	c.entries[key] = cached{snap: s, at: now}
	for len(c.order) > c.max {
		delete(c.entries, c.order[0])
		c.order = c.order[1:]
	}
}

// snapshotKey names everything a snapshot depends on. Weights, knobs, the limit and the locale are
// left out on purpose: they only change scoring.
func snapshotKey(in recommend.RankInput, version uint64) string {
	var b strings.Builder
	// the compiled schema is cached per version and experiment variant, so its address names both
	fmt.Fprintf(&b, "%p|%d|%s|%s|%s|%s", in.Compiled, version, in.Recommender, in.User, in.Seed.Kind, in.Seed.Item)
	for _, id := range in.Seed.Items {
		b.WriteString("|i:" + string(id))
	}
	for _, u := range in.Seed.Users {
		b.WriteString("|u:" + string(u))
	}
	names := make([]string, 0, len(in.Context))
	for n := range in.Context {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, n := range names {
		b.WriteString("|c:" + n + "=" + in.Context[n].String())
	}
	return b.String()
}
