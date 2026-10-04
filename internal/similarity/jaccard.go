package similarity

import (
	"github.com/timurcravtov/walrus/internal/domain"
)

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
