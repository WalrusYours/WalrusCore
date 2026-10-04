package rank

import (
	"slices"

	"github.com/timurcravtov/walrus/internal/schema"
)

// applyRules reshapes the ranked list with the recommender's rules. Only quotas that cap an
// attribute are supported so far; the other rule kinds are skipped with a log line.
func (x *run) applyRules(ranked []scoredCand) []scoredCand {
	for _, id := range x.in.Spec.Rules {
		i := slices.IndexFunc(x.sch.Rules, func(r schema.Rule) bool { return r.ID == id })
		if i < 0 {
			continue
		}
		r := x.sch.Rules[i]
		if len(r.For) > 0 && !slices.Contains(r.For, x.typ) {
			continue
		}
		if r.When != "" && !x.holds(r.When) {
			continue
		}
		if r.Quota != nil && r.Quota.Attribute != "" {
			ranked = x.capPer(ranked, r.Quota.Attribute, r.Quota.Max, r.Quota.Per)
			continue
		}
		x.warnOnce("rule", id)
	}
	return ranked
}

// capPer keeps any `per` consecutive items from holding more than max with the same value of the
// attribute (one song per artist in every ten, say). An item that would break the cap waits for
// the next position where it fits; if nothing fits, the best remaining item goes.
func (x *run) capPer(ranked []scoredCand, attr string, limit, per int) []scoredCand {
	if limit <= 0 || per <= 1 {
		return ranked
	}
	valueOf := func(c scoredCand) string {
		return x.ents[x.idx[c.item.Item]].Attrs[attr].String()
	}
	hasValue := func(c scoredCand) bool {
		return !x.ents[x.idx[c.item.Item]].Attrs[attr].IsNull()
	}

	out := make([]scoredCand, 0, len(ranked))
	rest := slices.Clone(ranked)
	for len(rest) > 0 {
		from := max(0, len(out)-(per-1))
		pick := 0
		for k, c := range rest {
			if !hasValue(c) {
				pick = k
				break
			}
			n := 0
			for _, prev := range out[from:] {
				if hasValue(prev) && valueOf(prev) == valueOf(c) {
					n++
				}
			}
			if n < limit {
				pick = k
				break
			}
		}
		out = append(out, rest[pick])
		rest = slices.Delete(rest, pick, pick+1)
	}
	return out
}
