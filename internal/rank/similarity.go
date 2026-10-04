package rank

import (
	"math"
	"slices"

	"github.com/timurcravtov/walrus/internal/domain"
	"github.com/timurcravtov/walrus/internal/schema"
	"github.com/timurcravtov/walrus/internal/similarity"
)

type term struct {
	attrs  []string
	metric schema.Metric
	weight float64
}

// terms returns the similarity terms of the ranked type. only, when not empty, picks terms by id.
// Terms that compare users (via: interactions) are not item terms.
func (x *run) terms(only []string) []term {
	var out []term
	for _, t := range x.sch.Similarity[x.typ] {
		if t.Via != "" || len(t.On) == 0 {
			continue
		}
		if len(only) > 0 && !slices.Contains(only, t.TermID()) {
			continue
		}
		w := t.Weight
		if w <= 0 {
			w = 1
		}
		out = append(out, term{attrs: t.On, metric: t.Metric, weight: w})
	}
	return out
}

// scale is the smallest and largest value of a numeric attribute over the catalogue, so that
// attributes on different scales count equally in a cosine.
func (x *run) scale(attr string) [2]float64 {
	if s, ok := x.scales[attr]; ok {
		return s
	}
	s := [2]float64{math.Inf(1), math.Inf(-1)}
	for _, e := range x.ents {
		if f, ok := e.Attrs[attr].AsFloat(); ok {
			s[0], s[1] = min(s[0], f), max(s[1], f)
		}
	}
	x.scales[attr] = s
	return s
}

// sim is the weighted mean of the terms both items have a value for, so a missing optional
// attribute does not count against an item.
func (x *run) sim(a, b int, ts []term) float64 {
	sum, total := 0.0, 0.0
	for _, t := range ts {
		if v, ok := x.termSim(x.ents[a].Attrs, x.ents[b].Attrs, t); ok {
			sum += t.weight * v
			total += t.weight
		}
	}
	if total == 0 {
		return 0
	}
	return sum / total
}

func (x *run) termSim(a, b map[string]domain.Value, t term) (float64, bool) {
	switch t.metric {
	case schema.MetricJaccard:
		va, vb := a[t.attrs[0]], b[t.attrs[0]]
		if va.IsNull() || vb.IsNull() {
			return 0, false
		}
		return similarity.Jaccard(va, vb), true

	case schema.MetricEquals:
		va, vb := a[t.attrs[0]], b[t.attrs[0]]
		if va.IsNull() || vb.IsNull() {
			return 0, false
		}
		if va.Equal(vb) {
			return 1, true
		}
		return 0, true

	case schema.MetricLogRatio:
		fa, okA := a[t.attrs[0]].AsFloat()
		fb, okB := b[t.attrs[0]].AsFloat()
		if !okA || !okB || fa <= 0 || fb <= 0 {
			return 0, false
		}
		return max(0, 1-math.Abs(math.Log(fa)-math.Log(fb))/math.Log(10)), true

	case schema.MetricCosine:
		if len(t.attrs) == 1 {
			if va, ok := a[t.attrs[0]].AsVector(); ok {
				vb, ok := b[t.attrs[0]].AsVector()
				if !ok || len(va) != len(vb) {
					return 0, false
				}
				return cosine32(va, vb), true
			}
		}
		va, okA := x.numeric(a, t.attrs)
		vb, okB := x.numeric(b, t.attrs)
		if !okA || !okB {
			return 0, false
		}
		return cosine(va, vb), true
	}
	return 0, false
}

func (x *run) numeric(attrs map[string]domain.Value, names []string) ([]float64, bool) {
	out := make([]float64, len(names))
	for i, n := range names {
		f, ok := attrs[n].AsFloat()
		if !ok {
			return nil, false
		}
		if s := x.scale(n); s[1] > s[0] {
			f = (f - s[0]) / (s[1] - s[0])
		} else {
			f = 0
		}
		out[i] = f
	}
	return out, true
}

func cosine(a, b []float64) float64 {
	var dot, na, nb float64
	for i := range a {
		dot += a[i] * b[i]
		na += a[i] * a[i]
		nb += b[i] * b[i]
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / math.Sqrt(na*nb)
}

func cosine32(a, b []float32) float64 {
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / math.Sqrt(na*nb)
}
