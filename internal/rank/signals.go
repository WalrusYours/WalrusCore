package rank

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/timurcravtov/walrus/internal/domain"
	"github.com/timurcravtov/walrus/internal/geo"
	"github.com/timurcravtov/walrus/internal/schema"
)

// signalType is one kind of signal a schema can declare.
//
// prepare runs once per snapshot and works out whatever does not depend on weights; it may be nil.
// score turns the candidates into one raw number each, and returns why(c), which says in words
// what it found for candidate c (why may be nil, and returns "" when there is nothing to say).
type signalType struct {
	prepare func(s *snapshot, id string, spec schema.SignalSpec) any
	score   func(rk *ranking, id string, spec schema.SignalSpec) (raw []float64, why func(c int) string)
}

// signalTypes holds every signal type the ranker knows. A schema may use a type that is not here;
// the ranker then skips that signal, says so once in the log, and the schema push warns about it.
var signalTypes = map[string]signalType{
	"item_neighbors":   {score: itemNeighbors},
	"co_occurrence":    {prepare: prepareCoOccurrence, score: coOccurrence},
	"user_neighbors":   {prepare: prepareUsers, score: userNeighbours},
	"attribute_target": {score: attributeTarget},
	"proximity":        {score: proximity},
	"global_count":     {prepare: prepareGlobalCount, score: globalCount},
	"low_exposure":     {prepare: prepareExposure, score: lowExposure},
	"age_decay":        {score: ageDecay},
	"context_match":    {score: contextMatch},
	"provided":         {score: providedScores},
}

func itemNeighbors(rk *ranking, _ string, spec schema.SignalSpec) ([]float64, func(int) string) {
	sel, weights := rk.selectTerms(strList(spec.Params["terms"])), rk.termWeights()
	raw, best := rk.bySeed(func(cand, k int) float64 { return weighted(rk.sims[cand][k], weights, sel) })
	return raw, func(c int) string {
		seeds := best(c)
		if len(seeds) == 0 {
			return ""
		}
		if text := rk.termPhrase(rk.candidates[c], seeds[0], sel, weights); text != "" {
			return text
		}
		return "Similar to " + rk.names(seeds)
	}
}

func prepareCoOccurrence(s *snapshot, id string, _ schema.SignalSpec) any {
	p, _ := s.pairingFor(id)
	return p
}

func coOccurrence(rk *ranking, id string, spec schema.SignalSpec) ([]float64, func(int) string) {
	p := rk.prepared[id].(*pairing)
	raw, best := rk.bySeed(func(cand, k int) float64 { return p.value(rk.seed[k], cand) })
	return raw, func(c int) string {
		seeds := best(c)
		if len(seeds) == 0 {
			return ""
		}
		return rk.say(spec, "Added to the same playlists as {seed_items}, {count} times", map[string]string{
			"seed_items": rk.names(seeds),
			"count":      fmt.Sprint(p.co[seeds[0]][rk.candidates[c]]),
		})
	}
}

func prepareUsers(s *snapshot, _ string, _ schema.SignalSpec) any { return s.userProfiles() }

// userNeighbours scores each candidate by what the most similar users have: the similarity-weighted
// average of their weight on it, as a share of the total similarity. It needs min_neighbors users
// with the item before it counts at all.
func userNeighbours(rk *ranking, _ string, spec schema.SignalSpec) ([]float64, func(int) string) {
	m := rk.userModel()
	need := max(1, intParam(spec.Params["min_neighbors"]))
	raw := make([]float64, len(rk.candidates))
	if m.totalSim > 0 {
		for c, i := range rk.candidates {
			if m.holders[i] < need {
				continue
			}
			sum := 0.0
			for _, n := range m.neighbours {
				sum += n.sim * n.vec[i]
			}
			raw[c] = sum / m.totalSim
		}
	}
	return raw, func(c int) string {
		n := m.holders[rk.candidates[c]]
		if n < need {
			return ""
		}
		who := "1 person"
		if n > 1 {
			who = fmt.Sprintf("%d people", n)
		}
		return rk.say(spec, "Also in the playlists of "+who+" with similar taste", nil)
	}
}

// proximity favours what is near the place the request comes from: 2^(-km / half_distance), so a
// place at the half distance scores 0.5. Without a position in the request every candidate scores
// the same.
func proximity(rk *ranking, _ string, spec schema.SignalSpec) ([]float64, func(int) string) {
	raw := make([]float64, len(rk.candidates))
	attr := str(spec.Params["on"])
	from, ok := rk.resolve(str(spec.Params["to"]))
	if !ok || attr == "" {
		return raw, nil
	}
	half := 10.0
	if f, isNum := spec.Params["half_distance"].(float64); isNum && f > 0 {
		half = f
	} else if n, isInt := spec.Params["half_distance"].(int); isInt && n > 0 {
		half = float64(n)
	}
	km := func(c int) (float64, bool) { return geo.Km(rk.items[rk.candidates[c]].Attrs[attr], from) }
	for c := range rk.candidates {
		if d, ok := km(c); ok {
			raw[c] = math.Exp2(-d / half)
		}
	}
	return raw, func(c int) string {
		d, ok := km(c)
		switch {
		case !ok:
			return ""
		case d < 1:
			return rk.say(spec, "Under 1 km away", nil)
		case d < 10:
			return rk.say(spec, fmt.Sprintf("%.1f km away", d), nil)
		}
		return rk.say(spec, fmt.Sprintf("%.0f km away", d), nil)
	}
}

func attributeTarget(rk *ranking, _ string, spec schema.SignalSpec) ([]float64, func(int) string) {
	raw := make([]float64, len(rk.candidates))
	attr := str(spec.Params["on"])
	target, ok := rk.target(str(spec.Params["target"]), attr)
	lo, hi := rk.scale(attr)
	if attr == "" || !ok || hi <= lo {
		return raw, nil
	}
	for c, i := range rk.candidates {
		if f, ok := rk.items[i].Attrs[attr].AsFloat(); ok {
			raw[c] = max(0, 1-math.Abs(f-target)/(hi-lo))
		}
	}
	return raw, func(c int) string {
		f, ok := rk.items[rk.candidates[c]].Attrs[attr].AsFloat()
		if !ok {
			return ""
		}
		return rk.say(spec, "Close to the list's usual {attr} ({value} against {target})", map[string]string{
			"attr": humanise(attr), "value": num(f), "target": num(target),
		})
	}
}

// target is the number a candidate's attribute is compared to: the seed's mean, a literal, or a
// context value.
func (s *snapshot) target(ref, attr string) (float64, bool) {
	switch {
	case ref == "seed.mean":
		sum, n := 0.0, 0
		for _, seedItem := range s.seed {
			if f, ok := s.items[seedItem].Attrs[attr].AsFloat(); ok {
				sum += f
				n++
			}
		}
		return sum / float64(max(n, 1)), n > 0
	case strings.HasPrefix(ref, "$"):
		return s.env[ref].AsFloat()
	}
	var f float64
	_, err := fmt.Sscanf(ref, "%g", &f)
	return f, err == nil
}

// globalCounts is how often each item was picked in a signal's window.
type globalCounts map[domain.EntityID]float64

func prepareGlobalCount(s *snapshot, _ string, spec schema.SignalSpec) any {
	since := time.Time{}
	if d, ok := dur(spec.Params["window"]); ok {
		since = s.now.Add(-d)
	}
	of := strList(spec.Params["of"])
	counts := globalCounts{}
	for _, it := range s.interactions {
		counted := s.positive(it.Type)
		if len(of) > 0 {
			counted = contains(of, it.Type)
		}
		if counted && !it.TS.Before(since) {
			counts[it.Target]++
		}
	}
	return counts
}

func globalCount(rk *ranking, id string, spec schema.SignalSpec) ([]float64, func(int) string) {
	counts := rk.prepared[id].(globalCounts)
	raw := make([]float64, len(rk.candidates))
	for c, i := range rk.candidates {
		raw[c] = math.Log1p(counts[rk.items[i].ID])
	}
	window := str(spec.Params["window"])
	return raw, func(c int) string {
		n := int(counts[rk.items[rk.candidates[c]].ID])
		if n == 0 {
			return ""
		}
		text := "Picked " + plural(n, "time")
		if window != "" {
			text += " in the last " + window
		}
		return rk.say(spec, text, nil)
	}
}

// exposure is how many interactions each item has, and from how many people. The store holds no
// impressions, so interactions stand in for exposure.
type exposure struct {
	counts map[domain.EntityID]float64
	people map[domain.EntityID]int
}

func prepareExposure(s *snapshot, _ string, _ schema.SignalSpec) any {
	e := exposure{counts: map[domain.EntityID]float64{}, people: map[domain.EntityID]int{}}
	seen := map[domain.EntityID]map[domain.UserID]bool{}
	for _, it := range s.interactions {
		e.counts[it.Target]++
		if seen[it.Target] == nil {
			seen[it.Target] = map[domain.UserID]bool{}
		}
		if !seen[it.Target][it.User] {
			seen[it.Target][it.User] = true
			e.people[it.Target]++
		}
	}
	return e
}

// lowExposure favours items few people have touched.
func lowExposure(rk *ranking, id string, spec schema.SignalSpec) ([]float64, func(int) string) {
	e := rk.prepared[id].(exposure)
	raw := make([]float64, len(rk.candidates))
	for c, i := range rk.candidates {
		raw[c] = 1 / math.Sqrt(1+e.counts[rk.items[i].ID])
	}
	return raw, func(c int) string {
		switch n := e.people[rk.items[rk.candidates[c]].ID]; n {
		case 0:
			return rk.say(spec, "Nobody has played or added it yet", nil)
		case 1:
			return rk.say(spec, "Only one person has played or added it", nil)
		default:
			return rk.say(spec, fmt.Sprintf("Only %d people have played or added it", n), nil)
		}
	}
}

func ageDecay(rk *ranking, _ string, spec schema.SignalSpec) ([]float64, func(int) string) {
	raw := make([]float64, len(rk.candidates))
	half, ok := dur(spec.Params["half_life"])
	attr := str(spec.Params["on"])
	if attr == "" {
		if lc := rk.sch.Entities[rk.typ].Lifecycle; lc != nil {
			attr = lc.Created // age_decay without on: reads the entity's lifecycle.created
		}
	}
	if !ok || half <= 0 || attr == "" {
		return raw, nil
	}
	for c, i := range rk.candidates {
		if t, ok := rk.items[i].Attrs[attr].AsTime(); ok {
			raw[c] = math.Exp2(-float64(rk.now.Sub(t)) / float64(half))
		}
	}
	return raw, func(c int) string {
		t, ok := rk.items[rk.candidates[c]].Attrs[attr].AsTime()
		if !ok {
			return ""
		}
		return rk.say(spec, fmt.Sprintf("Released in %d", t.Year()), nil)
	}
}

// contextMatch is the share of the context's words that appear in the item's set attribute.
func contextMatch(rk *ranking, _ string, spec schema.SignalSpec) ([]float64, func(int) string) {
	raw := make([]float64, len(rk.candidates))
	words, ok := rk.env[str(spec.Params["against"])].AsSet()
	attr := str(spec.Params["on"])
	if !ok || len(words) == 0 || attr == "" {
		return raw, nil
	}
	matched := func(i int) []string {
		have, _ := rk.items[i].Attrs[attr].AsSet()
		var hit []string
		for _, w := range words {
			if contains(have, w) {
				hit = append(hit, w)
			}
		}
		return hit
	}
	for c, i := range rk.candidates {
		raw[c] = float64(len(matched(i))) / float64(len(words))
	}
	return raw, func(c int) string {
		hit := matched(rk.candidates[c])
		if len(hit) == 0 {
			return ""
		}
		return rk.say(spec, "Matches "+list(quote(hit), 3)+" from the title", nil)
	}
}

func providedScores(rk *ranking, _ string, spec schema.SignalSpec) ([]float64, func(int) string) {
	raw := make([]float64, len(rk.candidates))
	scores := rk.in.Provided[str(spec.Params["name"])]
	for c, i := range rk.candidates {
		raw[c] = scores[rk.items[i].ID]
	}
	return raw, func(int) string { return rk.say(spec, "Suggested by the platform", nil) }
}
