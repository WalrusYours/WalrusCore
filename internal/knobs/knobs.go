// Package knobs: knob values to signal weights, from the compiled schema.
package knobs

import (
	"errors"
	"fmt"
	"math"
	"slices"

	"github.com/timurcravtov/walrus/internal/schema"
)

var (
	ErrUnknownPreset = errors.New("unknown preset")
	ErrUnknownKnob   = errors.New("unknown knob")
	ErrBadValue      = errors.New("knob value must be a finite number")
)

// Input, lowest to highest: defaults, default preset, Preset, Saved, Overrides.
type Input struct {
	Preset    string
	Saved     map[string]float64
	Overrides map[string]float64
}

// Resolved: slices aligned with Signals, MetaNames and Knobs.
type Resolved struct {
	Weights []float64
	Meta    []float64
	Knobs   []float64 // after clamping
	Clamped []string  // clamped knobs
}

// Resolve weights for one request.
func Resolve(c *schema.Compiled, in Input) (Resolved, error) {
	var r Resolved
	err := ResolveInto(c, in, &r)
	return r, err
}

// ResolveInto reuses dst, no allocations.
func ResolveInto(c *schema.Compiled, in Input, dst *Resolved) error {
	dst.Weights = append(dst.Weights[:0], c.Defaults...)
	dst.Meta = append(dst.Meta[:0], c.MetaDefaults...)
	dst.Clamped = dst.Clamped[:0]
	dst.Knobs = slices.Grow(dst.Knobs[:0], len(c.Knobs))[:len(c.Knobs)]

	for i, k := range c.Knobs {
		dst.Knobs[i] = (k.Min + k.Max) / 2
	}
	if c.DefaultPreset != nil {
		applyPreset(dst.Knobs, c.DefaultPreset)
	}
	if in.Preset != "" {
		p, ok := c.Presets[in.Preset]
		if !ok {
			return fmt.Errorf("%w %q", ErrUnknownPreset, in.Preset)
		}
		applyPreset(dst.Knobs, &p)
	}

	var errs []error
	for _, values := range [...]map[string]float64{in.Saved, in.Overrides} {
		for id, v := range values {
			i, ok := c.KnobIndex[id]
			switch {
			case !ok:
				errs = append(errs, fmt.Errorf("%w %q", ErrUnknownKnob, id))
			case math.IsNaN(v) || math.IsInf(v, 0):
				errs = append(errs, fmt.Errorf("%w: %q", ErrBadValue, id))
			default:
				dst.Knobs[i] = v
			}
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}

	for i, k := range c.Knobs {
		x := dst.Knobs[i]
		if x < k.Min || x > k.Max {
			dst.Clamped = append(dst.Clamped, k.ID)
			x = math.Min(math.Max(x, k.Min), k.Max)
			dst.Knobs[i] = x
		}
		for _, b := range k.Bindings {
			v := b.Fn(x)
			if math.IsNaN(v) || math.IsInf(v, 0) {
				return fmt.Errorf("knob %q maps %s to a non-finite value at %v", k.ID, b.Target, x)
			}
			if b.Signal >= 0 {
				dst.Weights[b.Signal] = v
			} else {
				dst.Meta[b.Meta] = v
			}
		}
	}
	slices.Sort(dst.Clamped)
	return nil
}

func applyPreset(knobs []float64, p *schema.Preset) {
	for i, set := range p.Set {
		if set {
			knobs[i] = p.Values[i]
		}
	}
}
