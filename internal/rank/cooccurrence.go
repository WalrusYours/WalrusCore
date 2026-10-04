package rank

import "github.com/timurcravtov/walrus/internal/schema"

// pairing counts how often items appear in the same group of events: songs in one playlist,
// products in one basket. Only pairs with a seed item are counted.
type pairing struct {
	groups  int                 // how many groups there are
	n       map[int]int         // item -> groups it is in
	co      map[int]map[int]int // seed item -> item -> groups they share
	measure string
	support int // pairs seen together fewer times count as nothing
}

func (s *snapshot) newPairing(spec schema.SignalSpec) *pairing {
	types := strList(spec.Params["of"])
	groupBy := str(spec.Params["group_by"])
	members := map[string]map[int]bool{}
	for _, it := range s.interactions {
		i, ok := s.index[it.Target]
		key := it.Fields[groupBy]
		if !ok || key == "" || !contains(types, it.Type) {
			continue
		}
		if members[key] == nil {
			members[key] = map[int]bool{}
		}
		members[key][i] = true
	}

	p := &pairing{groups: len(members), n: map[int]int{}, co: map[int]map[int]int{}, measure: str(spec.Params["measure"]), support: max(1, intParam(spec.Params["min_support"]))}
	for _, group := range members {
		for i := range group {
			p.n[i]++
			if !s.inSeed[i] {
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

// pairingFor returns the pairing of a co_occurrence signal, counting it once per snapshot.
func (s *snapshot) pairingFor(signal string) (*pairing, bool) {
	if p, ok := s.pairings[signal]; ok {
		return p, true
	}
	spec, ok := s.sch.Signals[signal]
	if !ok || spec.Type != "co_occurrence" {
		return nil, false
	}
	p := s.newPairing(spec)
	s.pairings[signal] = p
	return p, true
}

// value is how strongly item c goes with seed item seedItem: lift by default, or the plain count,
// or Jaccard over the groups.
func (p *pairing) value(seedItem, c int) float64 {
	both := p.co[seedItem][c]
	if both < p.support {
		return 0
	}
	switch p.measure {
	case "count":
		return float64(both)
	case "jaccard":
		return float64(both) / float64(p.n[seedItem]+p.n[c]-both)
	}
	return float64(both) * float64(p.groups) / (float64(p.n[seedItem]) * float64(p.n[c]))
}
