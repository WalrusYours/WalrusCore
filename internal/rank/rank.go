// Package rank is the engine's Ranker: it reads the store, builds candidates, applies the schema's
// hard constraints, scores each candidate on the recommender's signals, and applies its rules.
//
// Candidate generation does not look at weights; only the final sum does. That is what lets a knob
// change re-rank without regenerating candidates.
package rank

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/timurcravtov/walrus/internal/domain"
	"github.com/timurcravtov/walrus/internal/recommend"
	"github.com/timurcravtov/walrus/internal/schema"
	"github.com/timurcravtov/walrus/internal/schema/expr"
	"github.com/timurcravtov/walrus/internal/store"
)

const (
	defaultCap     = 200
	maxCandidates  = 2000
	maxSeedMembers = 50
)

type Ranker struct {
	store  store.Store
	warned sync.Map
	now    func() time.Time
}

var _ recommend.Ranker = (*Ranker)(nil)

func New(st store.Store) *Ranker { return &Ranker{store: st, now: time.Now} }

// warnOnce reports a schema feature the ranker does not support yet, once per feature.
func (r *Ranker) warnOnce(kind, name string) {
	if _, seen := r.warned.LoadOrStore(kind+"/"+name, true); !seen {
		slog.Warn("rank: not supported yet, skipped", "kind", kind, "name", name)
	}
}

// run holds what one Rank call works on.
type run struct {
	*Ranker
	in   recommend.RankInput
	sch  *schema.Schema
	typ  string
	now  time.Time
	env  expr.Env
	ents []domain.Entity
	idx  map[domain.EntityID]int
	seed []int // indices into ents
	inSd map[int]bool
	ints []domain.Interaction
	// seedAgg blends how seed items combine: 0 is the mean, 1 is the best match
	seedAgg float64
	cands   []int // indices into ents
	scales  map[string][2]float64
}

func (r *Ranker) Rank(ctx context.Context, in recommend.RankInput) (recommend.RankOutput, error) {
	sch := in.Schema
	if sch == nil {
		return recommend.RankOutput{}, errors.New("rank: no schema")
	}
	typ := ""
	if len(in.Spec.For) > 0 {
		typ = in.Spec.For[0]
	} else if t, ok := sch.Ranked(); ok {
		typ = t
	}
	if typ == "" {
		return recommend.RankOutput{}, errors.New("rank: the recommender ranks no entity type")
	}

	x := &run{Ranker: r, in: in, sch: sch, typ: typ, now: r.now().UTC(), scales: map[string][2]float64{}}
	var err error
	if x.ents, err = r.store.ListEntities(ctx, typ); err != nil {
		return recommend.RankOutput{}, err
	}
	if x.ints, err = r.store.Interactions(ctx); err != nil {
		return recommend.RankOutput{}, err
	}
	x.idx = make(map[domain.EntityID]int, len(x.ents))
	for i, e := range x.ents {
		x.idx[e.ID] = i
	}
	x.env = x.environment()
	x.findSeed()
	x.seedAgg = x.aggregate()

	x.generate()
	x.applyConstraints()

	scored := x.score()
	scored = x.applyRules(scored)

	out := recommend.RankOutput{
		Items:      make([]domain.ScoredItem, len(scored)),
		Reasons:    make(map[domain.EntityID]string, len(scored)),
		Types:      make(map[domain.EntityID]string, len(scored)),
		Candidates: len(scored),
	}
	for i, s := range scored {
		out.Items[i] = s.item
		out.Reasons[s.item.Item] = s.reason
		out.Types[s.item.Item] = typ
	}
	return out, nil
}

func (x *run) environment() expr.Env {
	e := expr.Env{"seed.size": domain.Num(float64(x.in.Seed.Size()))}
	for name, v := range x.in.Context {
		e["$context."+name] = v
	}
	return e
}

// findSeed maps the request's seed to stored items. Ids the store does not know are ignored. A user
// seed is the items the user reacted positively to.
func (x *run) findSeed() {
	x.inSd = map[int]bool{}
	add := func(id domain.EntityID) {
		if i, ok := x.idx[id]; ok && !x.inSd[i] {
			x.inSd[i] = true
			x.seed = append(x.seed, i)
		}
	}
	switch x.in.Seed.Kind {
	case schema.SeedItem:
		add(x.in.Seed.Item)
	case schema.SeedItems, schema.SeedSession:
		for _, id := range x.in.Seed.Items {
			add(id)
		}
	case schema.SeedUser:
		for _, it := range slices.Backward(x.ints) {
			if it.User == x.in.User && x.positive(it.Type) && len(x.seed) < maxSeedMembers {
				add(it.Target)
			}
		}
	}
}

func (x *run) positive(interaction string) bool {
	spec, ok := x.sch.Interactions[interaction]
	return ok && spec.Positive()
}

// aggregate reads how seed items combine: the recommender's own seed_aggregate, or the knob that
// drives it.
func (x *run) aggregate() float64 {
	if v, ok := x.in.Meta["recommenders."+x.in.Recommender+".seed_aggregate"]; ok {
		return min(1, max(0, v))
	}
	if x.in.Spec.SeedAggregate == "max" {
		return 1
	}
	return 0
}

// combine folds one value per seed item into one number.
func (x *run) combine(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	sum, best := 0.0, vals[0]
	for _, v := range vals {
		sum += v
		best = max(best, v)
	}
	return (1-x.seedAgg)*sum/float64(len(vals)) + x.seedAgg*best
}

func (x *run) spec(signal string) (schema.SignalSpec, bool) {
	s, ok := x.sch.Signals[signal]
	return s, ok
}

// signalIDs are the recommender's signals in a fixed order.
func (x *run) signalIDs() []string {
	if len(x.in.Spec.Signals) > 0 {
		return x.in.Spec.Signals
	}
	ids := make([]string, 0, len(x.in.Weights))
	for id := range x.in.Weights {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

type scoredCand struct {
	item   domain.ScoredItem
	reason string
	attrs  map[string]domain.Value
}

func (x *run) name(i int) string {
	for _, attr := range []string{"title", "name"} {
		if s, ok := x.ents[i].Attrs[attr].AsString(); ok && s != "" {
			return s
		}
	}
	return string(x.ents[i].ID)
}
