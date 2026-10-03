package similarity

import (
	"math/bits"
	"sort"

	"github.com/timurcravtov/walrus/internal/domain"
)

// maxBitsetVocab is the largest vocabulary that is stored as bitmasks: 1024 bits is 16 words,
// so one comparison is at most 16 AND/OR/popcount steps. Larger vocabularies fall back to
// sorted-slice comparison.
const maxBitsetVocab = 1024

// Jaccard returns |A∩B| / |A∪B| for two set values. Values that are not sets, or two empty
// sets, score 0. domain.Set keeps members sorted and unique, so a merge walk is enough.
func Jaccard(a, b domain.Value) float64 {
	as, okA := a.AsSet()
	bs, okB := b.AsSet()
	if !okA || !okB {
		return 0
	}
	return jaccardSorted(as, bs)
}

func jaccardSorted(a, b []string) float64 {
	inter, i, j := 0, 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] == b[j]:
			inter++
			i++
			j++
		case a[i] < b[j]:
			i++
		default:
			j++
		}
	}
	union := len(a) + len(b) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

// JaccardIndex scores Jaccard between the sets of one pool of items, addressed by position.
// NewJaccardIndex picks the representation from the pool itself: bitmasks when the distinct
// members fit maxBitsetVocab, sorted slices otherwise. Callers see no difference.
type JaccardIndex struct {
	masks [][]uint64 // set when bitset mode is on
	sets  [][]string // set when bitset mode is off
	words int
}

// NewJaccardIndex builds an index over sets[i]. Non-set values count as empty sets.
func NewJaccardIndex(sets []domain.Value) *JaccardIndex {
	members := make([][]string, len(sets))
	vocab := map[string]int{}
	for i, v := range sets {
		m, _ := v.AsSet()
		members[i] = m
		for _, s := range m {
			if _, ok := vocab[s]; !ok {
				vocab[s] = len(vocab)
			}
		}
	}
	if len(vocab) > maxBitsetVocab {
		return &JaccardIndex{sets: members}
	}
	words := (len(vocab) + 63) / 64
	masks := make([][]uint64, len(members))
	for i, m := range members {
		mask := make([]uint64, words)
		for _, s := range m {
			id := vocab[s]
			mask[id/64] |= 1 << (id % 64)
		}
		masks[i] = mask
	}
	return &JaccardIndex{masks: masks, words: words}
}

// Bitset reports which representation was chosen.
func (x *JaccardIndex) Bitset() bool { return x.masks != nil }

func (x *JaccardIndex) Len() int {
	if x.Bitset() {
		return len(x.masks)
	}
	return len(x.sets)
}

// Score returns the Jaccard similarity of items i and j.
func (x *JaccardIndex) Score(i, j int) float64 {
	if !x.Bitset() {
		return jaccardSorted(x.sets[i], x.sets[j])
	}
	var inter, union int
	a, b := x.masks[i], x.masks[j]
	for w := range x.words {
		inter += bits.OnesCount64(a[w] & b[w])
		union += bits.OnesCount64(a[w] | b[w])
	}
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

// Neighbor is one result of Top.
type Neighbor struct {
	Index int
	Score float64
}

// Top returns the k items most similar to seed, best first, excluding the seed and items
// that share nothing with it. Ties keep the lower index first so results are stable.
func (x *JaccardIndex) Top(seed, k int) []Neighbor {
	out := make([]Neighbor, 0, x.Len())
	for j := range x.Len() {
		if j == seed {
			continue
		}
		if s := x.Score(seed, j); s > 0 {
			out = append(out, Neighbor{j, s})
		}
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].Score > out[b].Score })
	if len(out) > k {
		out = out[:k]
	}
	return out
}
