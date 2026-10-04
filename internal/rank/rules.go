package rank

import (
	"slices"

	"github.com/timurcravtov/walrus/internal/schema"
)

// A rule reshapes the ranked list after scoring.
type ruleFn func(rk *ranking, r schema.Rule, ranked []scoredCand) []scoredCand

// rules holds every rule kind the ranker knows, by kind. A kind that is not here is skipped, said
// once in the log, and warned about when the schema is pushed.
var rules = map[string]ruleFn{
	"quota": capPerQuota,
}

// ruleKind names the action a rule takes; a rule sets exactly one.
func ruleKind(r schema.Rule) string {
	switch {
	case r.Place != nil:
		return "place"
	case r.Boost != nil:
		return "boost"
	case r.Bury:
		return "bury"
	case r.Quota != nil && r.Quota.Attribute != "":
		return "quota"
	case r.Quota != nil:
		return "quota with min_share"
	case r.Cap != nil:
		return "cap"
	case r.Pin != nil:
		return "pin"
	}
	return "unknown"
}

// applyRules runs the recommender's rules in order.
func (rk *ranking) applyRules(ranked []scoredCand) []scoredCand {
	for _, id := range rk.spec.Rules {
		i := slices.IndexFunc(rk.sch.Rules, func(r schema.Rule) bool { return r.ID == id })
		if i < 0 {
			continue
		}
		r := rk.sch.Rules[i]
		if len(r.For) > 0 && !slices.Contains(r.For, rk.typ) {
			continue
		}
		if r.When != "" && !rk.holds(r.When) {
			continue
		}
		fn, ok := rules[ruleKind(r)]
		if !ok {
			rk.ranker.warnOnce("rule", ruleKind(r))
			continue
		}
		ranked = fn(rk, r, ranked)
	}
	return ranked
}

// capPerQuota keeps any `per` consecutive items from holding more than `max` with the same value of
// the attribute (one song per artist in every ten, say). An item that would break the cap waits for
// the next position where it fits; if nothing fits, the best remaining item goes.
func capPerQuota(rk *ranking, r schema.Rule, ranked []scoredCand) []scoredCand {
	limit, per, attr := r.Quota.Max, r.Quota.Per, r.Quota.Attribute
	if limit <= 0 || per <= 1 {
		return ranked
	}
	value := func(c scoredCand) (string, bool) {
		v := rk.items[rk.index[c.item.Item]].Attrs[attr]
		return v.String(), !v.IsNull()
	}

	out := make([]scoredCand, 0, len(ranked))
	rest := slices.Clone(ranked)
	for len(rest) > 0 {
		window := out[max(0, len(out)-(per-1)):]
		pick := slices.IndexFunc(rest, func(c scoredCand) bool {
			v, has := value(c)
			if !has {
				return true
			}
			n := 0
			for _, prev := range window {
				if pv, ok := value(prev); ok && pv == v {
					n++
				}
			}
			return n < limit
		})
		if pick < 0 {
			pick = 0
		}
		out = append(out, rest[pick])
		rest = slices.Delete(rest, pick, pick+1)
	}
	return out
}
