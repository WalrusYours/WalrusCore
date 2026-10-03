package schema

import (
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/timurcravtov/walrus/internal/schema/expr"
)

// Compiled schema: built once per version, read-only, safe to share.
type Compiled struct {
	Schema  *Schema // YAML data, read-only
	Version int
	Hash    string
	Ranked  string // ranked entity

	// signals, by index
	Signals     []Signal
	SignalIndex map[string]int
	Defaults    []float64

	// meta-parameters set by knobs
	MetaNames    []string
	MetaIndex    map[string]int
	MetaDefaults []float64

	Knobs         []Knob // schema order
	KnobIndex     map[string]int
	Presets       map[string]Preset
	DefaultPreset *Preset // presets.default

	Interactions map[string]Interaction
	Computers    map[string]*Computer // per entity type
}

type Signal struct {
	ID      string
	Type    string
	Default float64
	Params  map[string]any
	Locked  bool
}

type Knob struct {
	ID       string
	Label    string
	Min, Max float64
	Bindings []Binding
	Locked   bool
}

// Binding: knob value to a signal or meta weight.
type Binding struct {
	Target string
	Signal int // index into Compiled.Signals, or -1
	Meta   int // index into Compiled.MetaNames, or -1
	Fn     func(x float64) float64
}

// Preset knob values, aligned with Knobs.
type Preset struct {
	Name   string
	Values []float64
	Set    []bool
}

// Interaction: weight, decay and transform.
type Interaction struct {
	Name      string
	Weight    float64
	Value     string // raw value name
	Transform func(v float64) float64
	HalfLife  time.Duration
	Target    string // target entity
	Locked    bool
}

var metaDefaults = map[string]float64{
	"interactions.half_life_scale": 1,
	"constraint.energy_center":     0.5,
}

// EdgeWeight: schema weight times the transformed value.
func (i Interaction) EdgeWeight(value *float64) (float64, error) {
	if i.Value == "" {
		return i.Weight, nil
	}
	if value == nil {
		return 0, fmt.Errorf("interaction %q needs a value (%s)", i.Name, i.Value)
	}
	if math.IsNaN(*value) || math.IsInf(*value, 0) {
		return 0, fmt.Errorf("interaction %q: value must be finite", i.Name)
	}
	return i.Weight * i.Transform(*value), nil
}

// Compile a validated schema.
func Compile(s *Schema, version int, hash string) (*Compiled, error) {
	ranked, ok := s.Ranked()
	if !ok {
		return nil, fmt.Errorf("schema has no ranked entity")
	}
	c := &Compiled{Schema: s, Version: version, Hash: hash, Ranked: ranked}

	for _, id := range s.SignalIDs() {
		sg := s.Signals[id]
		c.SignalIndex = ensure(c.SignalIndex)
		c.SignalIndex[id] = len(c.Signals)
		c.Signals = append(c.Signals, Signal{ID: id, Type: sg.Type, Default: sg.Default, Params: sg.Params, Locked: sg.Locked})
		c.Defaults = append(c.Defaults, sg.Default)
	}

	if err := c.compileKnobs(s); err != nil {
		return nil, err
	}

	c.Interactions = make(map[string]Interaction, len(s.Interactions))
	for _, name := range s.InteractionTypes() {
		spec := s.Interactions[name]
		tf, err := compileTransform(spec)
		if err != nil {
			return nil, fmt.Errorf("interaction %q: %w", name, err)
		}
		target := spec.Target
		if target == "" {
			target = ranked
		}
		c.Interactions[name] = Interaction{
			Name: name, Weight: spec.Weight, Value: spec.Value, Transform: tf,
			HalfLife: spec.HalfLife.Std(), Target: target, Locked: spec.Locked,
		}
	}

	c.Computers = make(map[string]*Computer, len(s.Entities))
	for _, et := range s.EntityTypes() {
		comp, err := s.Entities[et].NewComputer()
		if err != nil {
			return nil, fmt.Errorf("entity %q: %w", et, err)
		}
		c.Computers[et] = comp
	}
	return c, nil
}

func ensure(m map[string]int) map[string]int {
	if m == nil {
		return map[string]int{}
	}
	return m
}

func (c *Compiled) compileKnobs(s *Schema) error {
	c.KnobIndex = make(map[string]int, len(s.Knobs))
	meta := map[string]bool{}

	for i, k := range s.Knobs {
		c.KnobIndex[k.ID] = i
		knob := Knob{ID: k.ID, Label: k.Label, Min: k.Range[0], Max: k.Range[1], Locked: k.Locked}
		for _, target := range sortedKeys(k.Maps) {
			fn, err := expr.CompileFloat(k.Maps[target], "x")
			if err != nil {
				return fmt.Errorf("knob %q maps.%s: %w", k.ID, target, err)
			}
			b := Binding{Target: target, Signal: -1, Meta: -1, Fn: fn}
			if idx, ok := c.SignalIndex[target]; ok {
				b.Signal = idx
			} else {
				meta[target] = true
			}
			knob.Bindings = append(knob.Bindings, b)
		}
		c.Knobs = append(c.Knobs, knob)
	}

	c.MetaNames = slices.Sorted(mapsKeys(meta))
	c.MetaIndex = make(map[string]int, len(c.MetaNames))
	for i, name := range c.MetaNames {
		c.MetaIndex[name] = i
		c.MetaDefaults = append(c.MetaDefaults, metaDefaults[name])
	}
	for ki := range c.Knobs {
		for bi := range c.Knobs[ki].Bindings {
			if b := &c.Knobs[ki].Bindings[bi]; b.Signal < 0 {
				b.Meta = c.MetaIndex[b.Target]
			}
		}
	}

	c.Presets = make(map[string]Preset, len(s.Presets))
	for name, vals := range s.Presets {
		p := Preset{Name: name, Values: make([]float64, len(c.Knobs)), Set: make([]bool, len(c.Knobs))}
		for kid, v := range vals {
			idx, ok := c.KnobIndex[kid]
			if !ok {
				return fmt.Errorf("preset %q sets unknown knob %q", name, kid)
			}
			p.Values[idx], p.Set[idx] = v, true
		}
		c.Presets[name] = p
	}
	if d, ok := c.Presets["default"]; ok {
		c.DefaultPreset = &d
	}
	return nil
}

func compileTransform(spec InteractionSpec) (func(float64) float64, error) {
	if spec.Value == "" {
		return func(v float64) float64 { return v }, nil
	}
	switch spec.Transform {
	case "", "identity":
		return func(v float64) float64 { return v }, nil
	case TransformLog1p:
		return func(v float64) float64 { return math.Log1p(math.Max(v, 0)) }, nil
	case TransformSqrt:
		return func(v float64) float64 { return math.Sqrt(math.Max(v, 0)) }, nil
	case TransformClamp:
		return func(v float64) float64 { return math.Min(math.Max(v, 0), 1) }, nil
	}
	return expr.CompileFloat(string(spec.Transform), "v")
}

// SignalWeights by id, for responses.
func (c *Compiled) SignalWeights(weights []float64) map[string]float64 {
	out := make(map[string]float64, len(c.Signals))
	for i, s := range c.Signals {
		out[s.ID] = weights[i]
	}
	return out
}
