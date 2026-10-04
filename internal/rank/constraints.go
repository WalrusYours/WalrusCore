package rank

import (
	"slices"
	"strings"
	"time"

	"github.com/timurcravtov/walrus/internal/domain"
	"github.com/timurcravtov/walrus/internal/schema"
	"github.com/timurcravtov/walrus/internal/schema/expr"
)

// applyConstraints removes every candidate that breaks one of the recommender's hard filters.
// Weights never come into it, and nothing a user moves can bring a removed item back.
func (x *run) applyConstraints() {
	var active []schema.Constraint
	for _, id := range x.in.Spec.Constraints {
		i := slices.IndexFunc(x.sch.Constraints, func(c schema.Constraint) bool { return c.ID == id })
		if i < 0 {
			continue
		}
		c := x.sch.Constraints[i]
		if len(c.For) > 0 && !slices.Contains(c.For, x.typ) {
			continue
		}
		active = append(active, c)
	}

	kept := x.cands[:0]
	for _, i := range x.cands {
		ok := true
		for _, c := range active {
			if !x.passes(c, i) {
				ok = false
				break
			}
		}
		if ok {
			kept = append(kept, i)
		}
	}
	x.cands = kept
}

// passes reports whether item i satisfies the constraint. A constraint that reads something the
// request does not have (a $user attribute with no user entity, a context field that was not
// sent) does not apply.
func (x *run) passes(c schema.Constraint, i int) bool {
	cond, exclude := c.Require, false
	if c.Exclude != nil {
		cond, exclude = c.Exclude, true
	}
	if cond == nil {
		return true
	}
	if cond.When != "" && !x.holds(cond.When) {
		return true
	}
	match, applies := x.matches(cond, i)
	if !applies {
		return true
	}
	return match != exclude
}

func (x *run) holds(src string) bool {
	e, err := expr.Compile(src)
	if err != nil {
		return false
	}
	v, err := e.Eval(x.env)
	if err != nil {
		return false
	}
	b, ok := v.AsBool()
	return ok && b
}

// matches reports whether the item meets the condition, and whether the condition could be
// evaluated at all.
func (x *run) matches(cond *schema.Condition, i int) (match, applies bool) {
	switch {
	case cond.InSeed:
		return x.inSd[i], true

	case len(cond.Interacted) > 0:
		if x.in.User == "" {
			return false, false
		}
		need := max(cond.CountGTE, 1)
		since := time.Time{}
		if cond.Within > 0 {
			since = x.now.Add(-cond.Within.Std())
		}
		n := 0
		for _, it := range x.ints {
			if it.User == x.in.User && it.Target == x.ents[i].ID && slices.Contains(cond.Interacted, it.Type) && !it.TS.Before(since) {
				n++
			}
		}
		return n >= need, true

	case cond.Attribute != "":
		return x.attributeMatches(cond, x.ents[i].Attrs[cond.Attribute])
	}
	return false, false
}

func (x *run) attributeMatches(cond *schema.Condition, have domain.Value) (match, applies bool) {
	switch {
	case cond.Contains != "":
		want, ok := x.resolve(cond.Contains)
		if !ok {
			return false, false
		}
		s, _ := want.AsString()
		set, isSet := have.AsSet()
		if isSet {
			return slices.Contains(set, s), true
		}
		have, _ := have.AsString()
		return strings.Contains(have, s), true

	case cond.Equals != nil:
		want, ok := x.literal(cond.Equals)
		if !ok {
			return false, false
		}
		return have.Equal(want), true

	case cond.In != "":
		want, ok := x.resolve(cond.In)
		if !ok {
			return false, false
		}
		set, _ := want.AsSet()
		s, _ := have.AsString()
		return slices.Contains(set, s), true

	case cond.Gt != "" || cond.Lt != "":
		f, hasValue := have.AsFloat()
		if cond.Gt != "" {
			bound, ok := x.number(cond.Gt)
			if !ok {
				return false, false
			}
			return hasValue && f > bound, true
		}
		bound, ok := x.number(cond.Lt)
		if !ok {
			return false, false
		}
		return hasValue && f < bound, true
	}
	return false, false
}

// resolve reads a $-reference from the request, or takes the text as it is.
func (x *run) resolve(ref string) (domain.Value, bool) {
	if strings.HasPrefix(ref, "$") {
		v, ok := x.env[ref]
		return v, ok && !v.IsNull()
	}
	return domain.Str(ref), true
}

func (x *run) literal(v any) (domain.Value, bool) {
	switch t := v.(type) {
	case bool:
		return domain.Bool(t), true
	case int:
		return domain.Num(float64(t)), true
	case float64:
		return domain.Num(t), true
	case string:
		return x.resolve(t)
	}
	return domain.Value{}, false
}

func (x *run) number(ref string) (float64, bool) {
	v, ok := x.literal(ref)
	if !ok {
		return 0, false
	}
	f, isNum := v.AsFloat()
	return f, isNum
}
