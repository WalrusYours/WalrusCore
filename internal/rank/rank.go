// Package rank is the engine's Ranker. A request is ranked in two layers:
//
//   - A snapshot holds everything that does not depend on weights: the catalogue and interactions
//     read from the store, the seed, the candidates (generated, then filtered by the hard
//     constraints), the similarities between candidates and seed items, and what each signal can
//     work out in advance. Snapshots are cached, keyed by the schema, the store's version and what
//     the request is about (recommender, seed, user, context).
//   - A ranking combines a snapshot with the request's weights into scores, explanations and the
//     final order.
//
// Moving a Tune slider changes only weights, so the next request reuses the snapshot and only
// re-scores. That is the instant re-rank.
package rank

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/timurcravtov/walrus/internal/domain"
	"github.com/timurcravtov/walrus/internal/recommend"
	"github.com/timurcravtov/walrus/internal/schema"
	"github.com/timurcravtov/walrus/internal/store"
)

type Ranker struct {
	store  store.Store
	cache  *snapshotCache
	warned sync.Map
	now    func() time.Time
	builds atomic.Int64 // snapshots built, so tests can see the cache at work
}

var (
	_ recommend.Ranker  = (*Ranker)(nil)
	_ recommend.History = (*Ranker)(nil)
)

func New(st store.Store) *Ranker {
	return &Ranker{store: st, cache: newSnapshotCache(512, 2*time.Minute), now: time.Now}
}

func (r *Ranker) Rank(ctx context.Context, in recommend.RankInput) (recommend.RankOutput, error) {
	if in.Compiled == nil {
		return recommend.RankOutput{}, errors.New("rank: no schema")
	}
	snap, err := r.snapshotFor(ctx, in)
	if err != nil {
		return recommend.RankOutput{}, err
	}
	rk := newRanking(snap, in)
	scored := rk.applyRules(rk.score())

	out := recommend.RankOutput{
		Items:      make([]domain.ScoredItem, len(scored)),
		Reasons:    make(map[domain.EntityID]string, len(scored)),
		Because:    make(map[domain.EntityID]map[string]string, len(scored)),
		Types:      make(map[domain.EntityID]string, len(scored)),
		Candidates: len(scored),
	}
	for i, s := range scored {
		out.Items[i] = s.item
		out.Reasons[s.item.Item] = s.reason
		out.Because[s.item.Item] = s.because
		out.Types[s.item.Item] = snap.typ
	}
	return out, nil
}

// ProfileSize is how many distinct items the user reacted to positively, for `profile.size`.
func (r *Ranker) ProfileSize(ctx context.Context, c *schema.Compiled, user domain.UserID) (int, error) {
	history, err := r.store.UserInteractions(ctx, user)
	if err != nil {
		return 0, err
	}
	liked := map[domain.EntityID]bool{}
	for _, it := range history {
		if spec, ok := c.Schema.Interactions[it.Type]; ok && spec.Positive() {
			liked[it.Target] = true
		}
	}
	return len(liked), nil
}

// warnOnce reports a schema feature the ranker does not support yet, once per feature.
func (r *Ranker) warnOnce(kind, name string) {
	if _, seen := r.warned.LoadOrStore(kind+"/"+name, true); !seen {
		slog.Warn("rank: not supported yet, skipped", "kind", kind, "name", name)
	}
}
