package rank

import (
	"sort"
	"time"

	"github.com/timurcravtov/walrus/internal/schema"
)

// A candidate source proposes items for the request. Sources never read weights.
type sourceFn func(s *snapshot, src schema.CandidateSource, cap int) []int

// sources holds every candidate source the ranker knows, by the name a schema uses.
var sources = map[string]sourceFn{
	"item_neighbors": func(s *snapshot, _ schema.CandidateSource, cap int) []int { return s.neighbours(cap) },
	"co_occurrence":  coListedWithSeed,
	"user_neighbors": func(s *snapshot, _ schema.CandidateSource, cap int) []int { return s.likedByNeighbours(cap) },
	"factors":        func(s *snapshot, src schema.CandidateSource, cap int) []int { return s.factorCandidates(src, cap) },
	"popular":        func(s *snapshot, _ schema.CandidateSource, cap int) []int { return s.popular(cap) },
	"fresh":          func(s *snapshot, _ schema.CandidateSource, cap int) []int { return s.fresh(cap) },
}

// generate builds the candidate set from the recommender's sources. With no sources declared, every
// item of the type is a candidate.
func (s *snapshot) generate() {
	seen := map[int]bool{}
	add := func(idxs []int) {
		for _, i := range idxs {
			if !seen[i] {
				seen[i] = true
				s.candidates = append(s.candidates, i)
			}
		}
	}

	if len(s.spec.Candidates) == 0 {
		for i := range s.items[:min(len(s.items), maxCandidates)] {
			add([]int{i})
		}
		return
	}
	for _, src := range s.spec.Candidates {
		fn, ok := sources[src.Source]
		if !ok {
			s.ranker.warnOnce("candidate source", src.Source)
			continue
		}
		cap := src.Cap
		if cap <= 0 {
			cap = defaultCap
		}
		add(fn(s, src, cap))
	}
	s.candidates = s.candidates[:min(len(s.candidates), maxCandidates)]
}

// neighbours are the items most similar to any seed item, on the schema's own term weights.
func (s *snapshot) neighbours(cap int) []int {
	weights, all := s.declaredWeights(), s.selectTerms(nil)
	best := make([]float64, len(s.items))
	vals := make([]float64, len(s.terms))
	for i := range s.items {
		if s.inSeed[i] {
			continue
		}
		for _, seedItem := range s.seed {
			for t := range s.terms {
				v, ok := s.termValue(i, seedItem, s.terms[t])
				if !ok {
					v = nan
				}
				vals[t] = v
			}
			best[i] = max(best[i], weighted(vals, weights, all))
		}
	}
	return topIndices(best, cap, s.inSeed)
}

func coListedWithSeed(s *snapshot, src schema.CandidateSource, cap int) []int {
	p, ok := s.pairingFor(src.Signal)
	if !ok {
		s.ranker.warnOnce("candidate source", src.Source+" without a co_occurrence signal")
		return nil
	}
	best := make([]float64, len(s.items))
	for _, seedItem := range s.seed {
		for c := range p.co[seedItem] {
			best[c] = max(best[c], p.value(seedItem, c))
		}
	}
	return topIndices(best, cap, s.inSeed)
}

// popular are the items with the most interactions.
func (s *snapshot) popular(cap int) []int {
	counts := make([]float64, len(s.items))
	for _, it := range s.interactions {
		if i, ok := s.index[it.Target]; ok {
			counts[i]++
		}
	}
	return topIndices(counts, cap, s.inSeed)
}

// fresh are the newest items, by the entity's lifecycle.created attribute. An entity with no
// lifecycle.created has no newest.
func (s *snapshot) fresh(cap int) []int {
	attr := ""
	if lc := s.sch.Entities[s.typ].Lifecycle; lc != nil {
		attr = lc.Created
	}
	var when []time.Time
	idxs := make([]int, 0, len(s.items))
	for i, e := range s.items {
		if t, ok := e.Attrs[attr].AsTime(); ok && !s.inSeed[i] {
			idxs = append(idxs, i)
			when = append(when, t)
		}
	}
	order := make([]int, len(idxs))
	for k := range order {
		order[k] = k
	}
	sort.SliceStable(order, func(a, b int) bool { return when[order[a]].After(when[order[b]]) })
	out := make([]int, 0, min(cap, len(order)))
	for _, k := range order[:min(cap, len(order))] {
		out = append(out, idxs[k])
	}
	return out
}

// topIndices are the positions with the highest positive scores, at most cap of them.
func topIndices(score []float64, cap int, skip map[int]bool) []int {
	idxs := make([]int, 0, len(score))
	for i, v := range score {
		if v > 0 && !skip[i] {
			idxs = append(idxs, i)
		}
	}
	sort.SliceStable(idxs, func(a, b int) bool { return score[idxs[a]] > score[idxs[b]] })
	return idxs[:min(len(idxs), cap)]
}
