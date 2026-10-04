package rank

import (
	"math"
	"slices"

	"github.com/timurcravtov/walrus/internal/similarity"
)

// term is one item similarity term of the schema (`similarity.<type>`), with its metric.
type term struct {
	id     string
	attrs  []string
	metric string
	weight float64 // as the schema declares it
	fn     similarity.Metric
}

// itemTerms are the ranked type's terms that compare items by attributes. Terms that compare users
// (via: interactions) belong to users.go.
func (s *snapshot) itemTerms() []term {
	var out []term
	for _, t := range s.sch.Similarity[s.typ] {
		if t.Via != "" || len(t.On) == 0 {
			continue
		}
		fn, ok := similarity.Lookup(string(t.Metric))
		if !ok {
			s.ranker.warnOnce("similarity metric", string(t.Metric))
			continue
		}
		w := t.Weight
		if w <= 0 {
			w = 1
		}
		out = append(out, term{id: t.TermID(), attrs: t.On, metric: string(t.Metric), weight: w, fn: fn})
	}
	return out
}

func (s *snapshot) termValue(a, b int, t term) (float64, bool) {
	return t.fn(similarity.Pair{A: s.items[a].Attrs, B: s.items[b].Attrs, Attrs: t.attrs, Range: s.scale})
}

// declaredWeights are the term weights the schema gives. Candidate generation uses them, so it
// never depends on a knob.
func (s *snapshot) declaredWeights() []float64 {
	w := make([]float64, len(s.terms))
	for i, t := range s.terms {
		w[i] = t.weight
	}
	return w
}

// selectTerms marks the terms a signal compares on: those it names, or all when it names none.
func (s *snapshot) selectTerms(ids []string) []bool {
	sel := make([]bool, len(s.terms))
	for i, t := range s.terms {
		sel[i] = len(ids) == 0 || slices.Contains(ids, t.id)
	}
	return sel
}

// weighted is the weighted mean of the selected terms that have a value, so a missing optional
// attribute does not count against an item.
func weighted(vals, weights []float64, sel []bool) float64 {
	sum, total := 0.0, 0.0
	for t, v := range vals {
		if !sel[t] || math.IsNaN(v) || weights[t] <= 0 {
			continue
		}
		sum += weights[t] * v
		total += weights[t]
	}
	if total == 0 {
		return 0
	}
	return sum / total
}

// termWeights are the term weights this request scores with: the declared ones, except where a
// knob the recommender offers sets `similarity.<type>.<term>.weight`.
func (rk *ranking) termWeights() []float64 {
	w := rk.declaredWeights()
	for i, t := range rk.terms {
		target := "similarity." + rk.typ + "." + t.id + ".weight"
		if v, ok := rk.in.Meta[target]; ok && rk.knobSets(target) {
			w[i] = max(0, v)
		}
	}
	return w
}

// knobSets reports whether a knob that this recommender offers is bound to the target.
func (rk *ranking) knobSets(target string) bool {
	for _, k := range rk.sch.Knobs {
		if _, bound := k.Maps[target]; !bound {
			continue
		}
		if len(rk.spec.Knobs) > 0 && !slices.Contains(rk.spec.Knobs, k.ID) {
			continue
		}
		if len(k.Scope) > 0 && !slices.Contains(k.Scope, rk.recommender) {
			continue
		}
		return true
	}
	return false
}
