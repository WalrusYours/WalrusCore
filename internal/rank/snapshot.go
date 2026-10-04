package rank

import (
	"context"
	"errors"
	"maps"
	"math"
	"slices"
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
	gapSample      = 200
)

// snapshot is everything about a request that weights cannot change. Once built it is only read,
// so concurrent requests share it safely.
type snapshot struct {
	ranker      *Ranker
	c           *schema.Compiled
	sch         *schema.Schema
	recommender string
	spec        schema.RecommenderSpec
	typ         string // the entity type ranked
	user        domain.UserID
	now         time.Time
	env         expr.Env // what constraint and rule conditions read

	items        []domain.Entity
	index        map[domain.EntityID]int // item id to position in items
	interactions []domain.Interaction

	seed   []int // positions in items
	inSeed map[int]bool

	terms      []term                // the type's item similarity terms
	scales     map[string][2]float64 // smallest and largest value of each numeric attribute
	prepared   map[string]any        // per signal id, what its type works out in advance
	pairings   map[string]*pairing   // per co_occurrence signal id
	users      *userData             // interaction profiles, when the recommender compares people
	candidates []int                 // positions in items, after the hard constraints
	sims       map[int][][]float64   // candidate -> seed position -> term position; NaN: no value
	gaps       map[string]float64    // typical gap between two candidates on a numeric attribute
}

func (r *Ranker) snapshotFor(ctx context.Context, in recommend.RankInput) (*snapshot, error) {
	version, err := r.store.Version(ctx)
	if err != nil {
		return nil, err
	}
	key := snapshotKey(in, version)
	if s, ok := r.cache.get(key, r.now()); ok {
		return s, nil
	}
	s, err := r.build(ctx, in)
	if err != nil {
		return nil, err
	}
	r.builds.Add(1)
	r.cache.put(key, s, r.now())
	return s, nil
}

func (r *Ranker) build(ctx context.Context, in recommend.RankInput) (*snapshot, error) {
	sch := in.Compiled.Schema
	typ := ""
	if len(in.Spec.For) > 0 {
		typ = in.Spec.For[0]
	} else if t, ok := sch.Ranked(); ok {
		typ = t
	}
	if typ == "" {
		return nil, errors.New("rank: the recommender ranks no entity type")
	}

	s := &snapshot{
		ranker: r, c: in.Compiled, sch: sch, recommender: in.Recommender, spec: in.Spec, typ: typ,
		user: in.User, now: r.now().UTC(), prepared: map[string]any{}, pairings: map[string]*pairing{},
	}
	var err error
	if s.items, err = r.store.ListEntities(ctx, typ); err != nil {
		return nil, err
	}
	if s.interactions, err = r.store.Interactions(ctx); err != nil {
		return nil, err
	}
	s.index = make(map[domain.EntityID]int, len(s.items))
	for i, e := range s.items {
		s.index[e.ID] = i
	}
	s.env = make(expr.Env, len(in.Env)+8)
	maps.Copy(s.env, in.Env)
	if err := s.readUser(ctx); err != nil {
		return nil, err
	}

	s.findSeed(in.Seed)
	s.scales = s.numericScales()
	s.terms = s.itemTerms()
	s.prepareSignals()
	s.generate()
	s.applyConstraints()
	s.sims = s.seedSims()
	s.gaps = s.candidateGaps()
	return s, nil
}

// findSeed maps the request's seed to stored items. Ids the store does not know are ignored. A user
// seed is the items the user reacted positively to, newest first.
func (s *snapshot) findSeed(seed recommend.Seed) {
	s.inSeed = map[int]bool{}
	add := func(id domain.EntityID) {
		if i, ok := s.index[id]; ok && !s.inSeed[i] {
			s.inSeed[i] = true
			s.seed = append(s.seed, i)
		}
	}
	switch seed.Kind {
	case schema.SeedItem:
		add(seed.Item)
	case schema.SeedItems, schema.SeedSession:
		for _, id := range seed.Items {
			add(id)
		}
	case schema.SeedUser:
		for _, it := range slices.Backward(s.interactions) {
			if it.User == s.user && s.positive(it.Type) && len(s.seed) < maxSeedMembers {
				add(it.Target)
			}
		}
	}
}

func (s *snapshot) positive(interaction string) bool {
	spec, ok := s.sch.Interactions[interaction]
	return ok && spec.Positive()
}

// numericScales finds the range of every number attribute of the ranked type, for metrics and
// signals that compare numbers on the catalogue's scale.
func (s *snapshot) numericScales() map[string][2]float64 {
	out := map[string][2]float64{}
	for name, a := range s.sch.Entities[s.typ].Attributes {
		if a.Type != schema.TypeFloat && a.Type != schema.TypeInt {
			continue
		}
		lo, hi := math.Inf(1), math.Inf(-1)
		for _, e := range s.items {
			if f, ok := e.Attrs[name].AsFloat(); ok {
				lo, hi = min(lo, f), max(hi, f)
			}
		}
		if lo <= hi {
			out[name] = [2]float64{lo, hi}
		}
	}
	return out
}

func (s *snapshot) scale(attr string) (lo, hi float64) {
	r := s.scales[attr]
	return r[0], r[1]
}

// prepareSignals lets every signal type of the recommender work out what it can before weights are
// known, so re-scoring after a knob change reads it instead of recomputing it.
func (s *snapshot) prepareSignals() {
	for _, id := range s.signalIDs() {
		spec, ok := s.sch.Signals[id]
		if !ok {
			continue
		}
		if t, ok := signalTypes[spec.Type]; ok && t.prepare != nil {
			s.prepared[id] = t.prepare(s, id, spec)
		}
	}
}

// signalIDs are the recommender's signals, or every signal when it lists none.
func (s *snapshot) signalIDs() []string {
	if len(s.spec.Signals) > 0 {
		return s.spec.Signals
	}
	return s.sch.SignalIDs()
}

// seedSims is the value of every similarity term between each candidate and each seed item.
func (s *snapshot) seedSims() map[int][][]float64 {
	out := make(map[int][][]float64, len(s.candidates))
	if len(s.seed) == 0 || len(s.terms) == 0 {
		return out
	}
	for _, c := range s.candidates {
		rows := make([][]float64, len(s.seed))
		for k, seedItem := range s.seed {
			row := make([]float64, len(s.terms))
			for t := range s.terms {
				v, ok := s.termValue(c, seedItem, s.terms[t])
				if !ok {
					v = math.NaN()
				}
				row[t] = v
			}
			rows[k] = row
		}
		out[c] = rows
	}
	return out
}

// candidateGaps is the average difference between two candidates on each numeric attribute, from a
// sample of them. Explanations use it to tell a remarkable match from an ordinary one.
func (s *snapshot) candidateGaps() map[string]float64 {
	out := map[string]float64{}
	sample := s.candidates[:min(len(s.candidates), gapSample)]
	for attr := range s.scales {
		var vals []float64
		for _, i := range sample {
			if f, ok := s.items[i].Attrs[attr].AsFloat(); ok {
				vals = append(vals, f)
			}
		}
		sum, pairs := 0.0, 0
		for i := range vals {
			for j := i + 1; j < len(vals); j++ {
				sum += math.Abs(vals[i] - vals[j])
				pairs++
			}
		}
		if pairs > 0 {
			out[attr] = sum / float64(pairs)
		}
	}
	return out
}

// name is how an item is called in explanations: its title or name attribute, else its id.
func (s *snapshot) name(i int) string {
	for _, attr := range []string{"title", "name"} {
		if v, ok := s.items[i].Attrs[attr].AsString(); ok && v != "" {
			return v
		}
	}
	return string(s.items[i].ID)
}

// readUser exposes the requesting user's attributes as $user.<attribute> to the schema's
// conditions. A user the store has never seen has none, and conditions that read them do not apply.
func (s *snapshot) readUser(ctx context.Context) error {
	if s.user == "" {
		return nil
	}
	u, err := s.ranker.store.Entity(ctx, "user", domain.EntityID(s.user))
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	for name, v := range u.Attrs {
		s.env["$user."+name] = v
	}
	return nil
}
