package schema

import (
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"

	"github.com/timurcravtov/walrus/internal/schema/expr"
)

var (
	identRe   = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	userRefRe = regexp.MustCompile(`\$user\.([A-Za-z_][A-Za-z0-9_]*)`)
)

var SignalTypes = []string{
	"item_neighbors", "user_neighbors", "own_history", "global_count",
	"age_decay", "low_exposure", "attribute_match", "diversity_rerank",
}

var MetaTargets = []string{"interactions.half_life_scale", "constraint.energy_center"}

var builtinTransforms = []string{"", "identity", "log1p", "sqrt", "clamp"}

type Issue struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

// Ranked returns the entity type that is recommended: Recommendable, or the only entity
// besides "user".
func (s *Schema) Ranked() (string, bool) {
	if s.Recommendable != "" {
		_, ok := s.Entities[s.Recommendable]
		return s.Recommendable, ok
	}
	var others []string
	for _, name := range s.EntityTypes() {
		if name != "user" {
			others = append(others, name)
		}
	}
	if len(others) == 1 {
		return others[0], true
	}
	return "", false
}

// Validate checks everything Parse cannot: references, identifiers, types, expressions and
// computed attributes. It returns all issues at once, in a stable order.
func Validate(s *Schema) []Issue {
	v := &validator{s: s}
	v.run()
	return v.issues
}

type validator struct {
	s      *Schema
	issues []Issue
	ranked string
}

func (v *validator) add(path, format string, args ...any) {
	v.issues = append(v.issues, Issue{Path: path, Message: fmt.Sprintf(format, args...)})
}

func (v *validator) ident(path, name string) {
	if !identRe.MatchString(name) {
		v.add(path, "identifier %q must match ^[a-z][a-z0-9_]*$", name)
	}
}

func (v *validator) run() {
	s := v.s
	if s.Version < 1 {
		v.add("version", "version must be an integer of at least 1")
	}
	v.entities()
	if _, ok := s.Entities["user"]; !ok && len(s.Entities) > 0 {
		v.add("entities.user", `an entity named "user" is required (constraints and signals refer to $user)`)
	}
	if r, ok := s.Ranked(); ok {
		v.ranked = r
	} else if len(s.Entities) > 0 {
		if s.Recommendable != "" {
			v.add("recommendable", "%q is not a declared entity", s.Recommendable)
		} else {
			v.add("recommendable", `name the entity that is ranked (more than one entity besides "user" is declared)`)
		}
	}
	v.interactions()
	v.similarity()
	v.signals()
	v.knobs()
	v.presets()
	v.constraints()
}

func (v *validator) entities() {
	if len(v.s.Entities) == 0 {
		v.add("entities", "declare at least one entity")
		return
	}
	for _, name := range v.s.EntityTypes() {
		e := v.s.Entities[name]
		base := "entities." + name
		v.ident(base, name)
		if e.Key == "" {
			v.add(base+".key", "key is required (the name of the id field, normally id)")
		}
		attrs := slices.Sorted(mapsKeys(e.Attributes))
		for _, an := range attrs {
			a := e.Attributes[an]
			p := base + ".attributes." + an
			v.ident(p, an)
			v.attribute(p, a)
		}
		if _, err := e.NewComputer(); err != nil {
			v.add(base, "%v", err)
		}
	}
}

func (v *validator) attribute(p string, a AttributeSpec) {
	if !a.Type.Valid() {
		v.add(p+".type", "unknown type %q (use one of %s)", a.Type, joinTypes())
		return
	}
	switch a.Type {
	case TypeRef:
		if _, ok := v.s.Entities[a.Entity]; !ok {
			v.add(p+".entity", "ref must name a declared entity, got %q", a.Entity)
		}
	case TypeSet:
		if a.Of != "" && a.Of != "string" {
			v.add(p+".of", `set elements must be "string", got %q`, a.Of)
		}
	case TypeVector:
		if a.Dim < 0 {
			v.add(p+".dim", "dim must not be negative")
		}
	}
	if a.Range != nil {
		if a.Type != TypeFloat && a.Type != TypeInt {
			v.add(p+".range", "range applies only to float and int attributes")
		} else if !(a.Range[0] < a.Range[1]) {
			v.add(p+".range", "range must be [min, max] with min < max")
		}
	}
	if a.Computed != "" && a.Optional {
		v.add(p+".optional", "a computed attribute is absent when its inputs are missing; do not mark it optional")
	}
}

func joinTypes() string {
	names := make([]string, len(AttrTypes))
	for i, t := range AttrTypes {
		names[i] = string(t)
	}
	return strings.Join(names, ", ")
}

func (v *validator) interactions() {
	if len(v.s.Interactions) == 0 {
		v.add("interactions", "declare at least one interaction")
		return
	}
	for _, name := range v.s.InteractionTypes() {
		i := v.s.Interactions[name]
		p := "interactions." + name
		v.ident(p, name)
		if math.IsNaN(i.Weight) || math.IsInf(i.Weight, 0) {
			v.add(p+".weight", "weight must be a finite number")
		}
		if i.HalfLife < 0 {
			v.add(p+".half_life", "half_life must not be negative")
		}
		if i.Target != "" {
			if _, ok := v.s.Entities[i.Target]; !ok {
				v.add(p+".target", "target must be a declared entity, got %q", i.Target)
			}
		}
		if i.Value == "" && i.Transform != "" {
			v.add(p+".transform", "transform needs a value to transform (set value: <name>)")
		}
		v.transform(p+".transform", i.Transform)
	}
}

func (v *validator) transform(p string, t Transform) {
	if slices.Contains(builtinTransforms, string(t)) {
		return
	}
	x, err := expr.Compile(string(t))
	if err != nil {
		v.add(p, "unknown transform %q (use identity, log1p, sqrt, clamp, or an expression in v): %v", t, err)
		return
	}
	for _, name := range x.Vars() {
		if name != "v" {
			v.add(p, "transform expression may only use v (the raw value), got %q", name)
		}
	}
}

func (v *validator) similarity() {
	for _, et := range slices.Sorted(mapsKeys(v.s.Similarity)) {
		e, ok := v.s.Entities[et]
		if !ok {
			v.add("similarity."+et, "not a declared entity")
			continue
		}
		for i, t := range v.s.Similarity[et] {
			p := fmt.Sprintf("similarity.%s[%d]", et, i)
			if !t.Metric.Valid() {
				v.add(p+".metric", "metric must be one of jaccard, cosine, equals, log_ratio")
			}
			switch {
			case len(t.On) > 0 && t.Via != "":
				v.add(p, "use either on or via, not both")
			case len(t.On) > 0:
				for _, on := range t.On {
					if _, ok := e.Attributes[on]; !ok {
						v.add(p+".on", "%q is not an attribute of %s", on, et)
					}
				}
				if t.Weight <= 0 || math.IsNaN(t.Weight) {
					v.add(p+".weight", "weight must be greater than 0")
				}
			case t.Via == "interactions":
			case t.Via != "":
				v.add(p+".via", `via must be "interactions", got %q`, t.Via)
			default:
				v.add(p, "a term needs on (an attribute) or via: interactions")
			}
		}
	}
}

func (v *validator) rankedAttr(p, attr string) (AttributeSpec, bool) {
	if v.ranked == "" {
		return AttributeSpec{}, false
	}
	a, ok := v.s.Entities[v.ranked].Attributes[attr]
	if !ok {
		v.add(p, "%q is not an attribute of %s", attr, v.ranked)
	}
	return a, ok
}

func (v *validator) signals() {
	if len(v.s.Signals) == 0 {
		v.add("signals", "declare at least one signal")
		return
	}
	for _, id := range v.s.SignalIDs() {
		sg := v.s.Signals[id]
		p := "signals." + id
		v.ident(p, id)
		if !slices.Contains(SignalTypes, sg.Type) {
			v.add(p+".type", "unknown signal type %q (use one of %s)", sg.Type, strings.Join(SignalTypes, ", "))
		}
		if math.IsNaN(sg.Default) || math.IsInf(sg.Default, 0) {
			v.add(p+".default", "default must be a finite number")
		}
		for _, key := range []string{"window", "half_life"} {
			if raw, ok := sg.Params[key]; ok {
				d, err := ParseDuration(fmt.Sprint(raw))
				if err != nil || d <= 0 {
					v.add(p+"."+key, "use a positive duration such as 3d, 12h, 2w")
				}
			}
		}
		if raw, ok := sg.Params["on"]; ok {
			on := fmt.Sprint(raw)
			a, found := v.rankedAttr(p+".on", on)
			if found && sg.Type == "age_decay" && a.Type != TypeTimestamp {
				v.add(p+".on", "age_decay needs a timestamp attribute, %q is %s", on, a.Type)
			}
		} else if sg.Type == "age_decay" || sg.Type == "attribute_match" || sg.Type == "diversity_rerank" {
			v.add(p+".on", "%s needs on: <attribute>", sg.Type)
		}
		if sg.Type == "attribute_match" {
			against, _ := sg.Params["against"].(string)
			if against == "" {
				v.add(p+".against", `attribute_match needs against: "$user.<attribute>"`)
			} else {
				v.userRefs(p+".against", against)
			}
		}
	}
}

func (v *validator) userRefs(p, text string) {
	user := v.s.Entities["user"]
	for _, m := range userRefRe.FindAllStringSubmatch(text, -1) {
		if _, ok := user.Attributes[m[1]]; !ok {
			v.add(p, "$user.%s: %q is not an attribute of user", m[1], m[1])
		}
	}
}

func (v *validator) knobs() {
	seen := map[string]bool{}
	for i, k := range v.s.Knobs {
		p := fmt.Sprintf("knobs[%d]", i)
		switch {
		case !identRe.MatchString(k.ID):
			v.add(p+".id", "id must match ^[a-z][a-z0-9_]*$")
		case seen[k.ID]:
			v.add(p+".id", "duplicate knob id %q", k.ID)
		}
		seen[k.ID] = true
		if strings.TrimSpace(k.Label) == "" {
			v.add(p+".label", "label is required (plain language, shown to users)")
		}
		if !(k.Range[0] < k.Range[1]) {
			v.add(p+".range", "range must be [min, max] with min < max")
		}
		if len(k.Maps) == 0 {
			v.add(p+".maps", "map the knob onto at least one signal")
		}
		for _, target := range slices.Sorted(mapsKeys(k.Maps)) {
			tp := p + ".maps." + target
			if _, ok := v.s.Signals[target]; !ok && !slices.Contains(MetaTargets, target) {
				v.add(tp, "%q is neither a declared signal nor a known meta-parameter", target)
			}
			x, err := expr.Compile(k.Maps[target])
			if err != nil {
				v.add(tp, "expression %q: %v", k.Maps[target], err)
				continue
			}
			for _, name := range x.Vars() {
				if name != "x" {
					v.add(tp, "expression may only use x, got %q", name)
				}
			}
		}
	}
}

func (v *validator) presets() {
	for _, name := range slices.Sorted(mapsKeys(v.s.Presets)) {
		v.ident("presets."+name, name)
		for _, kid := range slices.Sorted(mapsKeys(v.s.Presets[name])) {
			p := "presets." + name + "." + kid
			k, ok := v.s.Knob(kid)
			if !ok {
				v.add(p, "%q is not a declared knob (presets set knobs, not signals)", kid)
				continue
			}
			if val := v.s.Presets[name][kid]; val < k.Range[0] || val > k.Range[1] {
				v.add(p, "%v is outside the knob range [%v, %v]", val, k.Range[0], k.Range[1])
			}
		}
	}
}

func (v *validator) constraints() {
	for i, c := range v.s.Constraints {
		p := fmt.Sprintf("constraints[%d]", i)
		var cond *Condition
		switch {
		case c.Require != nil && c.Exclude != nil:
			v.add(p, "use either require or exclude, not both")
			continue
		case c.Require != nil:
			cond, p = c.Require, p+".require"
		case c.Exclude != nil:
			cond, p = c.Exclude, p+".exclude"
		default:
			v.add(p, "a constraint needs require or exclude")
			continue
		}
		v.condition(p, cond)
	}
}

func (v *validator) condition(p string, c *Condition) {
	hasAttr, hasInter := c.Attribute != "", len(c.Interacted) > 0
	if hasAttr == hasInter {
		v.add(p, "a condition needs either attribute or interacted")
		return
	}
	if hasAttr {
		v.rankedAttr(p+".attribute", c.Attribute)
		ops := 0
		for _, set := range []bool{c.Equals != nil, c.In != "", c.Gt != "", c.Lt != "", c.Contains != ""} {
			if set {
				ops++
			}
		}
		if ops != 1 {
			v.add(p, "an attribute condition needs exactly one of equals, in, gt, lt, contains")
		}
	} else {
		for _, it := range c.Interacted {
			if _, ok := v.s.Interactions[it]; !ok {
				v.add(p+".interacted", "%q is not a declared interaction", it)
			}
		}
	}
	if (c.CountGTE != 0 || c.Within != 0) && !hasInter {
		v.add(p, "count_gte and within apply only to interacted conditions")
	}
	for field, text := range map[string]string{"in": c.In, "gt": c.Gt, "lt": c.Lt, "contains": c.Contains, "when": c.When} {
		v.userRefs(p+"."+field, text)
	}
	if s, ok := c.Equals.(string); ok {
		v.userRefs(p+".equals", s)
	}
}
