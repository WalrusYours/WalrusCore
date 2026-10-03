package similarity

import "github.com/timurcravtov/walrus/internal/domain"

// ItemNeighbor is one precomputed neighbour of an item.
type ItemNeighbor struct {
	Item domain.EntityID
	Sim  float64
}

// ItemNeighbors precomputes the top-k Jaccard neighbours of every item on one set attribute
// (ALGORITHMS.md 3.3). Items sharing nothing with an item are not its neighbours. The result
// does not depend on any weight, so it can be built once and reused across knob changes.
func ItemNeighbors(items []domain.Entity, attr string, k int) map[domain.EntityID][]ItemNeighbor {
	sets := make([]domain.Value, len(items))
	for i, e := range items {
		sets[i] = e.Attrs[attr]
	}
	idx := NewJaccardIndex(sets)

	out := make(map[domain.EntityID][]ItemNeighbor, len(items))
	for i, e := range items {
		top := idx.Top(i, k)
		ns := make([]ItemNeighbor, len(top))
		for n, t := range top {
			ns[n] = ItemNeighbor{Item: items[t.Index].ID, Sim: t.Score}
		}
		out[e.ID] = ns
	}
	return out
}
