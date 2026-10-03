package recommend

import (
	"hash/fnv"
	"sync"

	"github.com/timurcravtov/walrus/internal/domain"
	"github.com/timurcravtov/walrus/internal/schema"
)

// Experiment assignment: stateless and deterministic, so no table is
// needed and the same unit always lands in the same place.
//
//	bucket = hash(salt, unit) mod 10 000
//
// The holdout takes its buckets before any layer. Within a layer the running experiments, in id
// order, take consecutive slices of the buckets by `traffic`; a unit is in at most one of them.
// Inside an experiment the variants, control first and then by name, take slices by `share`.

const buckets = 10_000

func bucket(salt, unit string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(salt))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(unit))
	return int(h.Sum32() % buckets)
}

// view is the schema a request is served with: the tenant's, or a variant's patched copy.
type view struct {
	sch *schema.Schema
	c   *schema.Compiled
}

type variantCache struct {
	mu sync.Mutex
	m  map[string]*view
}

func newVariantCache() *variantCache { return &variantCache{m: map[string]*view{}} }

// get returns the patched, compiled schema of a variant, building it once per schema version.
func (vc *variantCache) get(base *view, exp, variant string, set map[string]any) (*view, error) {
	key := base.c.Hash + "/" + exp + "/" + variant
	vc.mu.Lock()
	defer vc.mu.Unlock()
	if v, ok := vc.m[key]; ok {
		return v, nil
	}
	patched, err := schema.ApplyPatch(base.sch, set)
	if err != nil {
		return nil, err
	}
	c, err := schema.Compile(patched, base.c.Version, schema.Hash(patched))
	if err != nil {
		return nil, err
	}
	if len(vc.m) > 256 { // a few variants per schema version; old versions age out
		clear(vc.m)
	}
	v := &view{sch: patched, c: c}
	vc.m[key] = v
	return v, nil
}

// assign decides the holdout and the experiment variant for a request to `name`. It returns the
// view to serve with (the base schema unless a non-control variant applies), the assignment, and
// whether the user is held out.
func (s *Service) assign(base *view, name string, user domain.UserID, recID string, cx map[string]domain.Value) (*Assignment, *view, bool) {
	sch := base.sch

	if h := sch.Holdout; h != nil && user != "" {
		if _, ok := specFor(sch, h.Recommender); ok && bucket("holdout", string(user)) < int(h.Share*buckets) {
			return nil, base, true
		}
	}

	// Group the running experiments by layer; each layer slices the buckets independently.
	byLayer := map[string][]string{}
	for _, id := range sortedNames(sch.Experiments) {
		x := sch.Experiments[id]
		if x.Status != "running" {
			continue
		}
		layer := x.Layer
		if layer == "" {
			layer = "default"
		}
		byLayer[layer] = append(byLayer[layer], id)
	}

	for _, layer := range sortedNames(byLayer) {
		cum := 0.0
		for _, id := range byLayer[layer] {
			x := sch.Experiments[id]
			lo, hi := cum, cum+x.Traffic
			cum = hi
			if x.Recommender != name {
				continue
			}
			unit := ""
			switch x.Unit {
			case "request":
				unit = recID
			case "session":
				// the request carries no session id yet, so a session unit cannot be assigned
			default:
				unit = string(user)
			}
			if unit == "" {
				continue
			}
			b := float64(bucket("layer:"+layer, unit)) / buckets
			if b < lo || b >= hi {
				continue
			}
			if x.Audience != nil {
				// $user attributes need the store; until it exists they read as null, so an
				// audience on them is not met and the unit is not enrolled.
				if holds, _ := evalBool(x.Audience.When, Seed{User: user}, cx); !holds {
					continue
				}
			}
			variant, set := pickVariant(x, unit, id)
			asg := &Assignment{ID: id, Variant: variant}
			if variant == "control" || len(set) == 0 {
				return asg, base, false
			}
			v, err := s.variants.get(base, id, variant, set)
			if err != nil {
				// A variant is validated at push, so this should not happen; serve control
				// rather than fail the request.
				return asg, base, false
			}
			return asg, v, false
		}
	}
	return nil, base, false
}

// pickVariant chooses by `share`, control first and then by name.
func pickVariant(x schema.ExperimentSpec, unit, exp string) (string, map[string]any) {
	order := []string{"control"}
	for _, n := range sortedNames(x.Variants) {
		if n != "control" {
			order = append(order, n)
		}
	}
	b := float64(bucket("exp:"+exp, unit)) / buckets
	cum := 0.0
	for _, n := range order {
		cum += x.Variants[n].Share
		if b < cum {
			return n, x.Variants[n].Set
		}
	}
	last := order[len(order)-1]
	return last, x.Variants[last].Set
}
