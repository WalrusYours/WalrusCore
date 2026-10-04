package recommend

import (
	"context"
	"maps"
	"math"
	"slices"

	"github.com/timurcravtov/walrus/internal/domain"
	"github.com/timurcravtov/walrus/internal/knobs"
	"github.com/timurcravtov/walrus/internal/schema"
	"github.com/timurcravtov/walrus/internal/schema/expr"
)

// tuning is what the knobs and weights come to for one recommender.
type tuning struct {
	weights map[string]float64 // signal id to weight
	meta    map[string]float64 // meta-parameters knobs set (term weights, seed_aggregate...)
	knobs   map[string]float64 // the knob values used, for the knobs the recommender offers
	clamped []string
}

// tuningFor turns knobs and weights into the numbers for one recommender: knob defaults, preset,
// the user's saved values and the request's overrides (only knobs the recommender offers), then
// its own `weights`, which apply to a signal no in-scope knob drives.
func (s *Service) tuningFor(ctx context.Context, v *view, name string, spec schema.RecommenderSpec, req Request, env expr.Env) (tuning, error) {
	var scope []string // nil: every knob
	if len(spec.Knobs) > 0 {
		scope = slices.Clone(spec.Knobs)
	}
	if err := checkKnobs(v.c, name, scope, req.Knobs); err != nil {
		return tuning{}, err
	}
	saved, err := s.savedKnobs(ctx, v.c, req.User)
	if err != nil {
		return tuning{}, err
	}
	res, err := knobs.Resolve(v.c, knobs.Input{Preset: req.Preset, Saved: saved, Overrides: req.Knobs, Scope: scope})
	if err != nil {
		return tuning{}, fail(400, "validation_error", "%v", err)
	}

	weights, err := signalWeights(v, name, spec, scope, res.Weights, env)
	if err != nil {
		return tuning{}, err
	}
	t := tuning{weights: weights, clamped: res.Clamped, meta: map[string]float64{}, knobs: map[string]float64{}}
	for i, n := range v.c.MetaNames {
		t.meta[n] = res.Meta[i]
	}
	for i, k := range v.c.Knobs {
		if scope == nil || slices.Contains(scope, k.ID) {
			t.knobs[k.ID] = res.Knobs[i]
		}
	}
	return t, nil
}

// checkKnobs rejects request overrides the recommender cannot take.
func checkKnobs(c *schema.Compiled, name string, scope []string, asked map[string]float64) error {
	for _, id := range slices.Sorted(maps.Keys(asked)) {
		if _, ok := c.KnobIndex[id]; !ok {
			return fail(400, "validation_error", "unknown knob %q", id)
		}
		if scope != nil && !slices.Contains(scope, id) {
			return fail(400, "validation_error", "knob %q is not offered by %s (it offers %v)", id, name, scope)
		}
		if f := asked[id]; math.IsNaN(f) || math.IsInf(f, 0) {
			return fail(400, "validation_error", "knob %q must be a finite number", id)
		}
	}
	return nil
}

// savedKnobs reads the user's saved values. A knob removed by a schema change is ignored.
func (s *Service) savedKnobs(ctx context.Context, c *schema.Compiled, user string) (map[string]float64, error) {
	if s.profiles == nil || user == "" {
		return nil, nil
	}
	got, err := s.profiles.SavedKnobs(ctx, domain.UserID(user))
	if err != nil {
		return nil, fail(500, "internal", "reading saved knobs: %v", err)
	}
	saved := map[string]float64{}
	for id, val := range got {
		if _, ok := c.KnobIndex[id]; ok {
			saved[id] = val
		}
	}
	return saved, nil
}

// signalWeights is the weight of each of the recommender's signals: the resolved knob value, or
// the recommender's own `weights` expression for a signal no knob in scope drives (a recommender
// weight must not undo the user's choice).
func signalWeights(v *view, name string, spec schema.RecommenderSpec, scope []string, resolved []float64, env expr.Env) (map[string]float64, error) {
	driven := map[int]bool{}
	for _, k := range v.c.Knobs {
		if scope != nil && !slices.Contains(scope, k.ID) {
			continue
		}
		for _, b := range k.Bindings {
			if b.Signal >= 0 {
				driven[b.Signal] = true
			}
		}
	}

	ids := spec.Signals
	if len(ids) == 0 {
		ids = v.sch.SignalIDs()
	}
	weights := make(map[string]float64, len(ids))
	for _, id := range ids {
		idx, ok := v.c.SignalIndex[id]
		if !ok {
			continue
		}
		w := resolved[idx]
		if rw, has := spec.Weights[id]; has && !driven[idx] {
			val, err := evalNumber(v.c, string(rw), env)
			if err != nil {
				return nil, fail(500, "internal", "weight of %s in %s: %v", id, name, err)
			}
			w = val
		}
		weights[id] = w
	}
	return weights, nil
}
