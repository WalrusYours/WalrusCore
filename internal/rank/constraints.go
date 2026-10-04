package rank

import (
	"slices"
	"strings"
	"time"

	"github.com/timurcravtov/walrus/internal/domain"
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
		text, _ := want.AsString()
		if set, isSet := have.AsSet(); isSet {
			return slices.Contains(set, text), true
		}
		str, _ := have.AsString()
		return strings.Contains(str, text), true

	case cond.Equals != nil:
		want, ok := s.literal(cond.Equals)
		if !ok {
			return false, false
		}
		return have.Equal(want), true

	case cond.In != "":
		want, ok := s.resolve(cond.In)
		if !ok {
			return false, false
		}
		set, _ := want.AsSet()
		str, _ := have.AsString()
		return slices.Contains(set, str), true

	case cond.Gt != "" || cond.Lt != "":
		f, hasValue := have.AsFloat()
		ref, above := cond.Gt, true
		if ref == "" {
			ref, above = cond.Lt, false
		}
		bound, ok := s.number(ref)
		if !ok {
			return false, false
		}
		if above {
			return hasValue && f > bound, true
		}
		return hasValue && f < bound, true
	}
	return false, false
}

// resolve reads a $-reference from the request, or takes the text as it is.
func (s *snapshot) resolve(ref string) (domain.Value, bool) {
	if strings.HasPrefix(ref, "$") {
		v, ok := s.env[ref]
		return v, ok && !v.IsNull()
	}
	return domain.Str(ref), true
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
