package rank

import (
	"sort"
)

// generate builds the candidate set from the recommender's sources. It never reads weights. With
// no sources declared, every item of the type is a candidate.
func (x *run) generate() {
	seen := map[int]bool{}
	add := func(idxs []int) {
		for _, i := range idxs {
			if !seen[i] {
				seen[i] = true
				x.cands = append(x.cands, i)
			}
		}
	}

	if len(x.in.Spec.Candidates) == 0 {
		all := make([]int, 0, len(x.ents))
		for i := range x.ents {
			all = append(all, i)
		}
		add(all[:min(len(all), maxCandidates)])
		return
	}
	for _, src := range x.in.Spec.Candidates {
		cap := src.Cap
		if cap <= 0 {
			cap = defaultCap
		}
		switch src.Source {
		case "item_neighbors":
			add(x.neighbours(cap))
		case "co_occurrence":
			spec, ok := x.spec(src.Signal)
			if !ok {
				x.warnOnce("candidate source", src.Source+" without a known signal")
				continue
			}
			add(x.togetherWithSeed(spec, cap))
		case "popular":
			add(x.popular(cap))
		default:
			x.warnOnce("candidate source", src.Source)
		}
	}
	if len(x.cands) > maxCandidates {
		x.cands = x.cands[:maxCandidates]
	}
}

// neighbours are the items most similar to any seed item.
func (x *run) neighbours(cap int) []int {
	ts := x.terms(nil)
	best := make([]float64, len(x.ents))
	for i := range x.ents {
		if x.inSd[i] {
			continue
		}
		for _, s := range x.seed {
			best[i] = max(best[i], x.sim(i, s, ts))
		}
	}
	return topIndices(best, cap, x.inSd)
}

// popular are the items with the most interactions.
func (x *run) popular(cap int) []int {
	counts := make([]float64, len(x.ents))
	for _, it := range x.ints {
		if i, ok := x.idx[it.Target]; ok {
			counts[i]++
		}
	}
	return topIndices(counts, cap, x.inSd)
}

func topIndices(score []float64, cap int, skip map[int]bool) []int {
	idxs := make([]int, 0, len(score))
	for i, s := range score {
		if s > 0 && !skip[i] {
			idxs = append(idxs, i)
		}
	}
	sort.SliceStable(idxs, func(a, b int) bool { return score[idxs[a]] > score[idxs[b]] })
	return idxs[:min(len(idxs), cap)]
}
