package rank

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/timurcravtov/walrus/internal/domain"
	"github.com/timurcravtov/walrus/internal/schema"
)

// A signal turns the candidates into one raw number each. evidence says, per candidate, which
// seed items it came from, for the reason shown to the user.
type signalFn func(x *run, id string, spec schema.SignalSpec) (raw []float64, evidence [][]int)

// registry holds every signal type the ranker knows. A schema may use a type that is not here;
// the ranker then skips that signal and says so once in the log.
var registry = map[string]signalFn{
	"item_neighbors":   itemNeighbors,
	"co_occurrence":    coOccurrence,
	"attribute_target": attributeTarget,
	"global_count":     globalCount,
	"low_exposure":     lowExposure,
	"age_decay":        ageDecay,
	"context_match":    contextMatch,
	"provided":         providedScores,
}

var defaultReason = map[string]string{
	"item_neighbors":   "Similar to {seed_items}",
	"co_occurrence":    "Often added together with {seed_items}",
	"attribute_target": "Fits the rest of the list",
	"global_count":     "Popular",
	"low_exposure":     "Something you may not have heard",
	"age_decay":        "New",
	"context_match":    "Matches the title",
}

type column struct {
	id       string
	spec     schema.SignalSpec
	weight   float64
	norm     []float64
	evidence [][]int
}

// score computes every signal with a weight, normalises each within the candidates, and sums
// weight times normalised value. The per-signal products are kept: they are the explanation.
func (x *run) score() []scoredCand {
	var cols []column
	for _, id := range x.signalIDs() {
		w := x.in.Weights[id]
		if w == 0 {
			continue
		}
		spec, ok := x.spec(id)
		if !ok {
			continue
		}
		fn, ok := registry[spec.Type]
		if !ok {
			x.warnOnce("signal type", spec.Type)
			continue
		}
		raw, evidence := fn(x, id, spec)
		cols = append(cols, column{id: id, spec: spec, weight: w, norm: normalise(raw, spec.Normalise), evidence: evidence})
	}

	out := make([]scoredCand, len(x.cands))
	for c, i := range x.cands {
		item := domain.ScoredItem{Item: x.ents[i].ID, Signals: make(map[string]float64, len(cols))}
		contrib := make([]float64, len(cols))
		for k, col := range cols {
			contrib[k] = col.weight * col.norm[c]
			item.Signals[col.id] = contrib[k]
			item.Score += contrib[k]
		}
		out[c] = scoredCand{item: item}

		// the reason comes from the strongest signal that has something to say
		order := make([]int, len(cols))
		for k := range order {
			order[k] = k
		}
		sort.SliceStable(order, func(a, b int) bool { return contrib[order[a]] > contrib[order[b]] })
		for _, k := range order {
			if contrib[k] <= 0 {
				break
			}
			var ev []int
			if cols[k].evidence != nil {
				ev = cols[k].evidence[c]
			}
			if r := x.reason(cols[k], ev); r != "" {
				out[c].reason = r
				break
			}
		}
	}
	sort.SliceStable(out, func(a, b int) bool {
		if out[a].item.Score != out[b].item.Score {
			return out[a].item.Score > out[b].item.Score
		}
		return out[a].item.Item < out[b].item.Item
	})
	return out
}

func (x *run) reason(col column, evidence []int) string {
	text := col.spec.Explain.Plain
	if text == "" {
		text = col.spec.Explain.Locales["en"]
	}
	if text == "" {
		text = defaultReason[col.spec.Type]
	}
	if text == "" {
		return ""
	}
	if strings.Contains(text, "{seed_items}") {
		if len(evidence) == 0 {
			return ""
		}
		names := make([]string, 0, 2)
		for _, i := range evidence[:min(2, len(evidence))] {
			names = append(names, x.name(i))
		}
		text = strings.ReplaceAll(text, "{seed_items}", strings.Join(names, " and "))
	}
	return text
}

// normalise rescales one signal over the candidates so weights mean the same for every signal.
// A constant signal carries no information and becomes 0.5 everywhere.
func normalise(raw []float64, how string) []float64 {
	out := make([]float64, len(raw))
	if how == "none" {
		copy(out, raw)
		return out
	}
	if how == "rank" {
		order := make([]int, len(raw))
		for i := range order {
			order[i] = i
		}
		sort.SliceStable(order, func(a, b int) bool { return raw[order[a]] < raw[order[b]] })
		for rank, i := range order {
			out[i] = float64(rank+1) / float64(len(raw))
		}
		return out
	}
	lo, hi := math.Inf(1), math.Inf(-1)
	for _, v := range raw {
		lo, hi = min(lo, v), max(hi, v)
	}
	for i, v := range raw {
		if hi > lo {
			out[i] = (v - lo) / (hi - lo)
		} else {
			out[i] = 0.5
		}
	}
	return out
}

// bySeed scores every candidate against each seed item and folds the results with the seed
// aggregate. The two seed items behind the best values are kept as evidence.
func (x *run) bySeed(per func(cand, seed int) float64) ([]float64, [][]int) {
	raw := make([]float64, len(x.cands))
	evidence := make([][]int, len(x.cands))
	for c, i := range x.cands {
		vals := make([]float64, len(x.seed))
		for k, s := range x.seed {
			vals[k] = per(i, s)
		}
		raw[c] = x.combine(vals)

		order := make([]int, len(x.seed))
		for k := range order {
			order[k] = k
		}
		sort.SliceStable(order, func(a, b int) bool { return vals[order[a]] > vals[order[b]] })
		for _, k := range order[:min(2, len(order))] {
			if vals[k] > 0 {
				evidence[c] = append(evidence[c], x.seed[k])
			}
		}
	}
	return raw, evidence
}

func itemNeighbors(x *run, _ string, spec schema.SignalSpec) ([]float64, [][]int) {
	ts := x.terms(strList(spec.Params["terms"]))
	return x.bySeed(func(c, s int) float64 { return x.sim(c, s, ts) })
}

func coOccurrence(x *run, _ string, spec schema.SignalSpec) ([]float64, [][]int) {
	p := x.pairing(spec)
	return x.bySeed(func(c, s int) float64 { return p.value(s, c) })
}

func attributeTarget(x *run, _ string, spec schema.SignalSpec) ([]float64, [][]int) {
	raw := make([]float64, len(x.cands))
	attr := str(spec.Params["on"])
	target, ok := x.target(str(spec.Params["target"]), attr)
	if attr == "" || !ok {
		return raw, nil
	}
	span := x.scale(attr)
	width := span[1] - span[0]
	for c, i := range x.cands {
		if f, ok := x.ents[i].Attrs[attr].AsFloat(); ok && width > 0 {
			raw[c] = max(0, 1-math.Abs(f-target)/width)
		}
	}
	return raw, nil
}

// target is the number a candidate's attribute is compared to: the seed's mean, a literal, or a
// context value.
func (x *run) target(ref, attr string) (float64, bool) {
	switch {
	case ref == "seed.mean":
		sum, n := 0.0, 0
		for _, s := range x.seed {
			if f, ok := x.ents[s].Attrs[attr].AsFloat(); ok {
				sum += f
				n++
			}
		}
		return sum / float64(max(n, 1)), n > 0
	case strings.HasPrefix(ref, "$"):
		f, ok := x.env[ref].AsFloat()
		return f, ok
	}
	var f float64
	_, err := fmt.Sscanf(ref, "%g", &f)
	return f, err == nil
}

func globalCount(x *run, _ string, spec schema.SignalSpec) ([]float64, [][]int) {
	since := time.Time{}
	if d, ok := dur(spec.Params["window"]); ok {
		since = x.now.Add(-d)
	}
	of := strList(spec.Params["of"])
	counts := make(map[domain.EntityID]float64)
	for _, it := range x.ints {
		counted := x.positive(it.Type)
		if len(of) > 0 {
			counted = contains(of, it.Type)
		}
		if counted && !it.TS.Before(since) {
			counts[it.Target]++
		}
	}
	raw := make([]float64, len(x.cands))
	for c, i := range x.cands {
		raw[c] = math.Log1p(counts[x.ents[i].ID])
	}
	return raw, nil
}

// lowExposure favours items few people have touched. Exposure here is the number of interactions
// on the item, since the store holds no impressions.
func lowExposure(x *run, _ string, _ schema.SignalSpec) ([]float64, [][]int) {
	counts := make(map[domain.EntityID]float64)
	for _, it := range x.ints {
		counts[it.Target]++
	}
	raw := make([]float64, len(x.cands))
	for c, i := range x.cands {
		raw[c] = 1 / math.Sqrt(1+counts[x.ents[i].ID])
	}
	return raw, nil
}

func ageDecay(x *run, _ string, spec schema.SignalSpec) ([]float64, [][]int) {
	raw := make([]float64, len(x.cands))
	half, ok := dur(spec.Params["half_life"])
	attr := str(spec.Params["on"])
	if !ok || half <= 0 || attr == "" {
		return raw, nil
	}
	for c, i := range x.cands {
		if t, ok := x.ents[i].Attrs[attr].AsTime(); ok {
			raw[c] = math.Exp2(-float64(x.now.Sub(t)) / float64(half))
		}
	}
	return raw, nil
}

// contextMatch is the share of the context's words that appear in the item's set attribute.
func contextMatch(x *run, _ string, spec schema.SignalSpec) ([]float64, [][]int) {
	raw := make([]float64, len(x.cands))
	words, ok := x.env[str(spec.Params["against"])].AsSet()
	attr := str(spec.Params["on"])
	if !ok || len(words) == 0 || attr == "" {
		return raw, nil
	}
	for c, i := range x.cands {
		have, _ := x.ents[i].Attrs[attr].AsSet()
		hit := 0
		for _, w := range words {
			if contains(have, w) {
				hit++
			}
		}
		raw[c] = float64(hit) / float64(len(words))
	}
	return raw, nil
}

func providedScores(x *run, _ string, spec schema.SignalSpec) ([]float64, [][]int) {
	raw := make([]float64, len(x.cands))
	scores := x.in.Provided[str(spec.Params["name"])]
	for c, i := range x.cands {
		raw[c] = scores[x.ents[i].ID]
	}
	return raw, nil
}

// pairing counts how often items appear in the same group of events: songs in one playlist,
// products in one basket.
type pairing struct {
	groups  int
	n       map[int]int
	co      map[int]map[int]int
	measure string
	support int
}

func (x *run) pairing(spec schema.SignalSpec) *pairing {
	types := strList(spec.Params["of"])
	groupBy := str(spec.Params["group_by"])
	members := map[string]map[int]bool{}
	for _, it := range x.ints {
		if !contains(types, it.Type) {
			continue
		}
		i, ok := x.idx[it.Target]
		if !ok {
			continue
		}
		key := it.Fields[groupBy]
		if key == "" {
			continue
		}
		if members[key] == nil {
			members[key] = map[int]bool{}
		}
		members[key][i] = true
	}

	p := &pairing{groups: len(members), n: map[int]int{}, co: map[int]map[int]int{}, measure: str(spec.Params["measure"]), support: 1}
	if f, ok := spec.Params["min_support"].(int); ok && f > 0 {
		p.support = f
	}
	for _, group := range members {
		for i := range group {
			p.n[i]++
			if !x.inSd[i] {
				continue
			}
			for j := range group {
				if j == i {
					continue
				}
				if p.co[i] == nil {
					p.co[i] = map[int]int{}
				}
				p.co[i][j]++
			}
		}
	}
	return p
}

// value is how strongly item c goes with seed item s. Pairs seen together fewer than min_support
// times count as nothing.
func (p *pairing) value(s, c int) float64 {
	both := p.co[s][c]
	if both == 0 || both < p.support {
		return 0
	}
	switch p.measure {
	case "count":
		return float64(both)
	case "jaccard":
		return float64(both) / float64(p.n[s]+p.n[c]-both)
	}
	return float64(both) * float64(p.groups) / (float64(p.n[s]) * float64(p.n[c]))
}

// togetherWithSeed are the items that share a group with a seed item often enough to count.
func (x *run) togetherWithSeed(spec schema.SignalSpec, cap int) []int {
	p := x.pairing(spec)
	best := make([]float64, len(x.ents))
	for _, s := range x.seed {
		for c := range p.co[s] {
			best[c] = max(best[c], p.value(s, c))
		}
	}
	return topIndices(best, cap, x.inSd)
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func strList(v any) []string {
	list, _ := v.([]any)
	out := make([]string, 0, len(list))
	for _, e := range list {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func dur(v any) (time.Duration, bool) {
	s, ok := v.(string)
	if !ok {
		return 0, false
	}
	d, err := schema.ParseDuration(s)
	return d.Std(), err == nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
