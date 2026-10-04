// Package similarity holds the metrics that compare two items on one similarity term. Each metric
// is registered by the name a schema uses (`metric: jaccard`); adding one is a Register call, with
// no change anywhere else.
package similarity

import (
	"math"
	"slices"
	"sync"

	"github.com/timurcravtov/walrus/internal/domain"
)

// Pair is what a metric compares: the attributes of a term on two items. Range gives the smallest
// and largest value of a numeric attribute over the catalogue, for metrics that need a scale.
type Pair struct {
	A, B  map[string]domain.Value
	Attrs []string
	Range func(attr string) (lo, hi float64)
}

// Metric scores a pair in [0, 1]. ok is false when either item lacks what the metric needs; the
// term is then left out of the comparison instead of counting as a mismatch.
type Metric func(p Pair) (value float64, ok bool)

var (
	mu      sync.RWMutex
	metrics = map[string]Metric{}
)

// Register adds a metric under a name. Registering a name twice replaces the first.
func Register(name string, m Metric) {
	mu.Lock()
	defer mu.Unlock()
	metrics[name] = m
}

func Lookup(name string) (Metric, bool) {
	mu.RLock()
	defer mu.RUnlock()
	m, ok := metrics[name]
	return m, ok
}

// Names lists the registered metrics in order.
func Names() []string {
	mu.RLock()
	defer mu.RUnlock()
	names := make([]string, 0, len(metrics))
	for n := range metrics {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}

func init() {
	Register("jaccard", jaccard)
	Register("equals", equals)
	Register("cosine", cosine)
	Register("log_ratio", logRatio)
	Register("closeness", closeness)
}

// jaccard is the overlap of two sets: |A∩B| / |A∪B|.
func jaccard(p Pair) (float64, bool) {
	a, b := p.A[p.Attrs[0]], p.B[p.Attrs[0]]
	if a.IsNull() || b.IsNull() {
		return 0, false
	}
	return Jaccard(a, b), true
}

// equals is 1 for the same value, 0 otherwise.
func equals(p Pair) (float64, bool) {
	a, b := p.A[p.Attrs[0]], p.B[p.Attrs[0]]
	if a.IsNull() || b.IsNull() {
		return 0, false
	}
	if a.Equal(b) {
		return 1, true
	}
	return 0, true
}

// logRatio is how close two positive numbers are as a ratio, 0 at ten times apart.
func logRatio(p Pair) (float64, bool) {
	a, okA := p.A[p.Attrs[0]].AsFloat()
	b, okB := p.B[p.Attrs[0]].AsFloat()
	if !okA || !okB || a <= 0 || b <= 0 {
		return 0, false
	}
	return max(0, 1-math.Abs(math.Log(a)-math.Log(b))/math.Log(10)), true
}

// closeness is 1 minus the gap between two numbers as a share of the catalogue's range.
func closeness(p Pair) (float64, bool) {
	a, okA := p.A[p.Attrs[0]].AsFloat()
	b, okB := p.B[p.Attrs[0]].AsFloat()
	if !okA || !okB {
		return 0, false
	}
	lo, hi := p.Range(p.Attrs[0])
	if hi <= lo {
		return 1, true
	}
	return max(0, 1-math.Abs(a-b)/(hi-lo)), true
}

// cosine is the angle between two vectors: one vector attribute, or a group of numeric attributes,
// each scaled to [0, 1] over the catalogue so no attribute outweighs another by its units.
func cosine(p Pair) (float64, bool) {
	if len(p.Attrs) == 1 {
		if a, ok := p.A[p.Attrs[0]].AsVector(); ok {
			b, ok := p.B[p.Attrs[0]].AsVector()
			if !ok || len(a) != len(b) {
				return 0, false
			}
			return cos(widen(a), widen(b)), true
		}
	}
	a, okA := scaled(p.A, p.Attrs, p.Range)
	b, okB := scaled(p.B, p.Attrs, p.Range)
	if !okA || !okB {
		return 0, false
	}
	return cos(a, b), true
}

func scaled(attrs map[string]domain.Value, names []string, rng func(string) (float64, float64)) ([]float64, bool) {
	out := make([]float64, len(names))
	for i, n := range names {
		f, ok := attrs[n].AsFloat()
		if !ok {
			return nil, false
		}
		if lo, hi := rng(n); hi > lo {
			out[i] = (f - lo) / (hi - lo)
		}
	}
	return out, true
}

func widen(v []float32) []float64 {
	out := make([]float64, len(v))
	for i, f := range v {
		out[i] = float64(f)
	}
	return out
}

func cos(a, b []float64) float64 {
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
