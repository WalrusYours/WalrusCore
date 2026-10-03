package schema

import (
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/timurcravtov/walrus/internal/domain"
	"github.com/timurcravtov/walrus/internal/schema/expr"
)

type computedAttr struct {
	name string
	spec AttributeSpec
	expr *expr.Expr
}

// Computer fills an entity type's computed attributes from its other attributes.
type Computer struct {
	order []computedAttr
}

var computedTypes = []AttrType{TypeInt, TypeFloat, TypeString, TypeCategorical, TypeBool}

// NewComputer compiles the entity's computed attributes and orders them so each is
// evaluated after the attributes it depends on. It fails on a bad expression, an unknown
// reference, an unsupported result type, or a dependency cycle.
func (e EntitySpec) NewComputer() (*Computer, error) {
	byName := map[string]computedAttr{}
	for _, name := range slices.Sorted(mapsKeys(e.Attributes)) {
		a := e.Attributes[name]
		if a.Computed == "" {
			continue
		}
		if !slices.Contains(computedTypes, a.Type) {
			return nil, fmt.Errorf("attribute %q: computed values can be int, float, string, categorical or bool, not %s", name, a.Type)
		}
		x, err := expr.Compile(a.Computed)
		if err != nil {
			return nil, fmt.Errorf("attribute %q: %w", name, err)
		}
		for _, v := range x.Vars() {
			if _, ok := e.Attributes[v]; !ok {
				return nil, fmt.Errorf("attribute %q: expression refers to %q, which is not an attribute", name, v)
			}
		}
		byName[name] = computedAttr{name, a, x}
	}

	var order []computedAttr
	state := map[string]int{} // 0 new, 1 visiting, 2 done
	var visit func(name string, path []string) error
	visit = func(name string, path []string) error {
		c, ok := byName[name]
		if !ok {
			return nil // a plain attribute: nothing to compute
		}
		switch state[name] {
		case 2:
			return nil
		case 1:
			return fmt.Errorf("computed attributes form a cycle: %s", strings.Join(append(path, name), " -> "))
		}
		state[name] = 1
		for _, dep := range c.expr.Vars() {
			if err := visit(dep, append(path, name)); err != nil {
				return err
			}
		}
		state[name] = 2
		order = append(order, c)
		return nil
	}
	for _, name := range slices.Sorted(mapsKeys(byName)) {
		if err := visit(name, nil); err != nil {
			return nil, err
		}
	}
	return &Computer{order: order}, nil
}

// Apply returns attrs plus the computed attributes. Attributes whose inputs are missing
// are left out. The platform must not send a computed attribute: Apply rejects it.
func (c *Computer) Apply(attrs map[string]domain.Value) (map[string]domain.Value, error) {
	out := make(map[string]domain.Value, len(attrs)+len(c.order))
	for k, v := range attrs {
		out[k] = v
	}
	for _, ca := range c.order {
		if _, sent := attrs[ca.name]; sent {
			return nil, fmt.Errorf("attribute %q is computed and must not be sent", ca.name)
		}
	}
	for _, ca := range c.order {
		v, err := ca.expr.Eval(expr.Env(out))
		if err != nil {
			return nil, fmt.Errorf("attribute %q: %w", ca.name, err)
		}
		if v.IsNull() {
			continue
		}
		v, err = fit(ca.spec.Type, v)
		if err != nil {
			return nil, fmt.Errorf("attribute %q: %w", ca.name, err)
		}
		out[ca.name] = v
	}
	return out, nil
}

func fit(t AttrType, v domain.Value) (domain.Value, error) {
	if v.Kind() != t.Kind() {
		return domain.Null(), fmt.Errorf("expression gave %s, attribute is %s", v.Kind(), t)
	}
	if t == TypeInt {
		f, _ := v.AsFloat()
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return domain.Null(), fmt.Errorf("result is not a finite number")
		}
		return domain.Num(math.Round(f)), nil
	}
	return v, nil
}
