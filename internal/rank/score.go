package rank

import (
	"math"
	"sort"
	"strings"

	"github.com/timurcravtov/walrus/internal/domain"
	"github.com/timurcravtov/walrus/internal/recommend"
	"github.com/timurcravtov/walrus/internal/schema"
)

var nan = math.NaN()

// ranking is one request scored on a snapshot: the snapshot says what is compared, the request's
// weights say how much each part counts.
type ranking struct {
	*snapshot
	in      recommend.RankInput
	seedAgg float64    // how seed items combine: 0 is the mean, 1 is the best match
	model   *userModel // the request's neighbours, when a signal needs them
}

func newRanking(s *snapshot, in recommend.RankInput) *ranking {
	rk := &ranking{snapshot: s, in: in}
	rk.seedAgg = rk.aggregate()
	return rk
}

// aggregate reads how seed items combine: the knob that drives seed_aggregate, else the
// recommender's own setting.
func (rk *ranking) aggregate() float64 {
	if v, ok := rk.in.Meta["recommenders."+rk.recommender+".seed_aggregate"]; ok {
		return min(1, max(0, v))
	}
	if rk.spec.SeedAggregate == "max" {
		return 1
	}
	return 0
}

// combine folds one value per seed item into one number.
func (rk *ranking) combine(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	sum, best := 0.0, vals[0]
	for _, v := range vals {
		sum += v
		best = max(best, v)
	}
	return (1-rk.seedAgg)*sum/float64(len(vals)) + rk.seedAgg*best
}

// bySeed scores every candidate against each seed item and folds the results with the seed
// aggregate. best(c) lists the (up to two) seed items candidate c matched most, best first.
func (rk *ranking) bySeed(per func(cand, seedPos int) float64) (raw []float64, best func(c int) []int) {
	raw = make([]float64, len(rk.candidates))
	vals := make([][]float64, len(rk.candidates))
	for c, cand := range rk.candidates {
		vals[c] = make([]float64, len(rk.seed))
		for k := range rk.seed {
			vals[c][k] = per(cand, k)
		}
		raw[c] = rk.combine(vals[c])
	}
	best = func(c int) []int {
		order := make([]int, len(rk.seed))
		for k := range order {
			order[k] = k
		}
		sort.SliceStable(order, func(a, b int) bool { return vals[c][order[a]] > vals[c][order[b]] })
		var out []int
		for _, k := range order[:min(2, len(order))] {
			if vals[c][k] > 0 {
				out = append(out, rk.seed[k])
			}
		}
		return out
	}
	return raw, best
}

type column struct {
	id     string
	spec   schema.SignalSpec
	weight float64
	norm   []float64
	why    func(c int) string
}

type scoredCand struct {
	item    domain.ScoredItem
	reason  string
	because map[string]string
}

// score computes every signal with a weight, normalises each within the candidates, and sums
// weight times normalised value. The per-signal products are kept: they are the explanation.
func (rk *ranking) score() []scoredCand {
	var cols []column
	for _, id := range rk.signalIDs() {
		w := rk.in.Weights[id]
		spec, ok := rk.sch.Signals[id]
		if w == 0 || !ok {
			continue
		}
		t, ok := signalTypes[spec.Type]
		if !ok {
			rk.ranker.warnOnce("signal type", spec.Type)
			continue
		}
		raw, why := t.score(rk, id, spec)
		cols = append(cols, column{id: id, spec: spec, weight: w, norm: normalise(raw, spec.Normalise), why: why})
	}

	type row struct {
		cand    int // position in candidates
		item    domain.ScoredItem
		contrib []float64
	}
	rows := make([]row, len(rk.candidates))
	for c, i := range rk.candidates {
		item := domain.ScoredItem{Item: rk.items[i].ID, Signals: make(map[string]float64, len(cols))}
		contrib := make([]float64, len(cols))
		for k, col := range cols {
			contrib[k] = col.weight * col.norm[c]
			item.Signals[col.id] = contrib[k]
			item.Score += contrib[k]
		}
		rows[c] = row{cand: c, item: item, contrib: contrib}
	}
	sort.SliceStable(rows, func(a, b int) bool {
		if rows[a].item.Score != rows[b].item.Score {
			return rows[a].item.Score > rows[b].item.Score
		}
		return rows[a].item.Item < rows[b].item.Item
	})

	// words are worked out only for the items that will be shown, with some to spare
	explained := max(rk.in.Limit, 20)
	out := make([]scoredCand, len(rows))
	for n, r := range rows {
		out[n] = scoredCand{item: r.item}
		if n < explained {
			out[n].because, out[n].reason = explain(cols, r.cand, r.contrib)
		}
	}
	return out
}

// explain says why an item is where it is. Each signal that contributed gets a sentence of its
// own; the one-line reason joins the two strongest, the second only when it carries real weight.
func explain(cols []column, c int, contrib []float64) (map[string]string, string) {
	order := make([]int, len(cols))
	for k := range order {
		order[k] = k
	}
	sort.SliceStable(order, func(a, b int) bool { return contrib[order[a]] > contrib[order[b]] })

	because := map[string]string{}
	var lines []string
	var top float64
	for _, k := range order {
		if contrib[k] <= 0 || cols[k].why == nil {
			continue
		}
		text := cols[k].why(c)
		if text == "" {
			continue
		}
		because[cols[k].id] = text
		switch {
		case len(lines) == 0:
			lines, top = append(lines, text), contrib[k]
		case len(lines) == 1 && contrib[k] >= 0.4*top:
			lines = append(lines, text)
		}
	}
	return because, strings.Join(lines, " · ")
}

// normalise rescales one signal over the candidates so weights mean the same for every signal.
// A constant signal carries no information and becomes 0.5 everywhere.
func normalise(raw []float64, how string) []float64 {
	out := make([]float64, len(raw))
	switch how {
	case "none":
		copy(out, raw)
		return out
	case "rank":
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
