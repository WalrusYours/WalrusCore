package rank

import (
	"slices"
	"strings"
	"time"

	"github.com/timurcravtov/walrus/internal/domain"
	"github.com/timurcravtov/walrus/internal/geo"
	"github.com/timurcravtov/walrus/internal/schema"
)

// applyConstraints removes every candidate that breaks one of the recommender's hard filters.
// Weights never come into it, and nothing a user moves can bring a removed item back.
func (s *snapshot) applyConstraints() {
	var active []schema.Constraint
	for _, id := range s.spec.Constraints {
		i := slices.IndexFunc(s.sch.Constraints, func(c schema.Constraint) bool { return c.ID == id })
		if i < 0 {
			continue
		}
		c := s.sch.Constraints[i]
		if len(c.For) > 0 && !slices.Contains(c.For, s.typ) {
			continue
		}
		active = append(active, c)
	}

	kept := s.candidates[:0]
	for _, i := range s.candidates {
		if !slices.ContainsFunc(active, func(c schema.Constraint) bool { return !s.passes(c, i) }) {
			kept = append(kept, i)
		}
	}
	s.candidates = kept
}

// passes reports whether item i satisfies the constraint. A constraint that reads something the
// request does not have (a $user attribute, a context field that was not sent) does not apply.
func (s *snapshot) passes(c schema.Constraint, i int) bool {
	cond, exclude := c.Require, false
	if c.Exclude != nil {
		cond, exclude = c.Exclude, true
	}
	if cond == nil {
		return true
	}
	if cond.When != "" && !s.holds(cond.When) {
		return true
	}
	match, applies := s.matches(cond, i)
	if !applies {
		return true
	}
	return match != exclude
}

// holds evaluates a condition of the schema. One that cannot be evaluated does not hold.
func (s *snapshot) holds(src string) bool {
	e, err := s.c.Expr(src)
	if err != nil {
		return false
	}
	v, err := e.Eval(s.env)
	if err != nil {
		return false
	}
	b, ok := v.AsBool()
	return ok && b
}

// matches reports whether the item meets the condition, and whether the condition could be
// evaluated at all.
func (s *snapshot) matches(cond *schema.Condition, i int) (match, applies bool) {
	switch {
	case cond.InSeed:
		return s.inSeed[i], true

	case len(cond.Interacted) > 0:
		if s.user == "" {
			return false, false
		}
		since := time.Time{}
		if cond.Within > 0 {
			since = s.now.Add(-cond.Within.Std())
		}
		n := 0
		for _, it := range s.interactions {
			if it.User == s.user && it.Target == s.items[i].ID && slices.Contains(cond.Interacted, it.Type) && !it.TS.Before(since) {
				n++
			}
		}
		return n >= max(cond.CountGTE, 1), true

	case cond.Attribute != "":
		return s.attributeMatches(cond, s.items[i].Attrs[cond.Attribute])
	}
	return false, false
}

func (s *snapshot) attributeMatches(cond *schema.Condition, have domain.Value) (match, applies bool) {
	switch {
	case cond.Contains != "":
		want, ok := s.resolve(cond.Contains)
		if !ok {
			return false, false
		}
		if _, isSet := want.AsSet(); isSet || hasSet(have) {
			return overlap(have, want), true
		}
		text, _ := want.AsString()
		str, _ := have.AsString()
		return strings.Contains(str, text), true

	case cond.Equals != nil:
		want, ok := s.literal(cond.Equals)
		if !ok {
			return false, false
		}
		if _, isSet := want.AsSet(); isSet {
			return overlap(have, want), true
		}
		return have.Equal(want), true

	case cond.In != "":
		want, ok := s.resolve(cond.In)
		if !ok {
			return false, false
		}
		return overlap(have, want), true

	case cond.WithinKm > 0:
		from, ok := s.resolve(cond.Of)
		if !ok {
			return false, false
		}
		km, located := geo.Km(have, from)
		return located && km <= cond.WithinKm, true
	}

	for _, cmp := range []struct {
		ref  string
		test func(have, bound float64) bool
	}{
		{cond.Gt, func(h, b float64) bool { return h > b }},
		{cond.Gte, func(h, b float64) bool { return h >= b }},
		{cond.Lt, func(h, b float64) bool { return h < b }},
		{cond.Lte, func(h, b float64) bool { return h <= b }},
	} {
		if cmp.ref == "" {
			continue
		}
		bound, ok := s.number(cmp.ref)
		if !ok {
			return false, false
		}
		f, hasValue := have.AsFloat()
		return hasValue && cmp.test(f, bound), true
	}
	return false, false
}

// hasSet reports whether a value is a set.
func hasSet(v domain.Value) bool {
	_, ok := v.AsSet()
	return ok
}

// members are the strings a value stands for: its elements for a set, itself for a string.
func members(v domain.Value) []string {
	if set, ok := v.AsSet(); ok {
		return set
	}
	if str, ok := v.AsString(); ok {
		return []string{str}
	}
	return nil
}

// overlap reports whether two values share a member.
func overlap(a, b domain.Value) bool {
	want := members(b)
	return slices.ContainsFunc(members(a), func(m string) bool { return slices.Contains(want, m) })
}

// resolve reads a $-reference from the request, or takes the text as it is. $seed.<attr> is the set
// of that attribute's values on the seed items.
func (s *snapshot) resolve(ref string) (domain.Value, bool) {
	switch {
	case strings.HasPrefix(ref, "$seed."):
		return s.seedValues(strings.TrimPrefix(ref, "$seed."))
	case strings.HasPrefix(ref, "$"):
		v, ok := s.env[ref]
		return v, ok && !v.IsNull()
	}
	return domain.Str(ref), true
}

// seedValues is the distinct values of an attribute over the seed items, as a set; false when the
// seed has none.
func (s *snapshot) seedValues(attr string) (domain.Value, bool) {
	var vals []string
	for _, i := range s.seed {
		vals = append(vals, members(s.items[i].Attrs[attr])...)
	}
	if len(vals) == 0 {
		return domain.Null(), false
	}
	return domain.Set(vals...), true
}

func (s *snapshot) literal(v any) (domain.Value, bool) {
	switch t := v.(type) {
	case bool:
		return domain.Bool(t), true
	case int:
		return domain.Num(float64(t)), true
	case float64:
		return domain.Num(t), true
	case string:
		return s.resolve(t)
	}
	return domain.Value{}, false
}

func (s *snapshot) number(ref string) (float64, bool) {
	v, ok := s.literal(ref)
	if !ok {
		return 0, false
	}
	return v.AsFloat()
}
