// Package recommend runs one recommendation request.
package recommend

import (
	"math"
	"sort"

	"github.com/timurcravtov/walrus/internal/domain"
	"github.com/timurcravtov/walrus/internal/similarity"
)

// SeedLimit is how many profile items seed candidate generation (ALGORITHMS.md 5).
const SeedLimit = 50

// Content is the signal id used in explain output.
const Content = "content"

type Input struct {
	Profile   map[domain.EntityID]float64
	Neighbors map[domain.EntityID][]similarity.ItemNeighbor
	Limit     int
}

// Result is a scored item plus the profile items that explain it.
type Result struct {
	domain.ScoredItem
	Via []domain.EntityID // strongest contributors first, at most 3
}

// Recommend generates candidates from the neighbours of the user's best-liked items, drops
// everything the user already has in their profile, scores the rest with the item_neighbors
// signal and returns the top Limit. Candidate generation reads no weight; only the final sort
// would, once more signals exist.
//
// content(i) = Σ_j P[j]·sim(i,j) / Σ_j |P[j]|, over profile items j that list i as a neighbour
// (ALGORITHMS.md 6). Negative profile entries pull the score down but never seed candidates.
func Recommend(in Input) []Result {
	type acc struct {
		num, den float64
		seeded   bool
		via      []contribution
	}
	cands := map[domain.EntityID]*acc{}
	seeds := seedItems(in.Profile)

	for j, pj := range in.Profile {
		for _, n := range in.Neighbors[j] {
			if _, seen := in.Profile[n.Item]; seen {
				continue
			}
			a := cands[n.Item]
			if a == nil {
				a = &acc{}
				cands[n.Item] = a
			}
			a.num += pj * n.Sim
			a.den += math.Abs(pj)
			a.via = append(a.via, contribution{j, pj * n.Sim})
			if _, ok := seeds[j]; ok {
				a.seeded = true
			}
		}
	}

	out := make([]Result, 0, len(cands))
	for id, a := range cands {
		if !a.seeded || a.den == 0 {
			continue
		}
		raw := a.num / a.den
		out = append(out, Result{
			ScoredItem: domain.ScoredItem{Item: id, Score: raw, Signals: map[string]float64{Content: raw}},
			Via:        topVia(a.via, 3),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].Item < out[j].Item // stable across runs
	})
	if in.Limit > 0 && len(out) > in.Limit {
		out = out[:in.Limit]
	}
	return out
}

type contribution struct {
	item  domain.EntityID
	value float64
}

// seedItems are the SeedLimit positive profile entries with the largest weight.
func seedItems(p map[domain.EntityID]float64) map[domain.EntityID]struct{} {
	type kv struct {
		id domain.EntityID
		w  float64
	}
	pos := make([]kv, 0, len(p))
	for id, w := range p {
		if w > 0 {
			pos = append(pos, kv{id, w})
		}
	}
	sort.Slice(pos, func(i, j int) bool {
		if pos[i].w != pos[j].w {
			return pos[i].w > pos[j].w
		}
		return pos[i].id < pos[j].id
	})
	if len(pos) > SeedLimit {
		pos = pos[:SeedLimit]
	}
	out := make(map[domain.EntityID]struct{}, len(pos))
	for _, e := range pos {
		out[e.id] = struct{}{}
	}
	return out
}

func topVia(cs []contribution, n int) []domain.EntityID {
	sort.Slice(cs, func(i, j int) bool {
		if cs[i].value != cs[j].value {
			return cs[i].value > cs[j].value
		}
		return cs[i].item < cs[j].item
	})
	if len(cs) > n {
		cs = cs[:n]
	}
	out := make([]domain.EntityID, 0, len(cs))
	for _, c := range cs {
		if c.value > 0 {
			out = append(out, c.item)
		}
	}
	return out
}
