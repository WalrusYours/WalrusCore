// Package schema models the YAML a platform pushes. These structs describe the schema
// language itself, so a schema push changes their contents, never the code.
package schema

import (
	"slices"

	"github.com/timurcravtov/walrus/internal/domain"
)

type Schema struct {
	Version       int                          `yaml:"version"`
	Entities      map[string]EntitySpec        `yaml:"entities"`
	Interactions  map[string]InteractionSpec   `yaml:"interactions"`
	Similarity    map[string][]SimilarityTerm  `yaml:"similarity,omitempty"`
	Signals       map[string]SignalSpec        `yaml:"signals"`
	Knobs         []KnobSpec                   `yaml:"knobs,omitempty"`
	Presets       map[string]domain.KnobValues `yaml:"presets,omitempty"`
	Constraints   []Constraint                 `yaml:"constraints,omitempty"`
	Recommendable string                       `yaml:"recommendable,omitempty"`
}

type EntitySpec struct {
	Key        string                   `yaml:"key"`
	Attributes map[string]AttributeSpec `yaml:"attributes,omitempty"`
}

type AttributeSpec struct {
	Type     AttrType    `yaml:"type"`
	Of       string      `yaml:"of,omitempty"`
	Entity   string      `yaml:"entity,omitempty"`
	Dim      int         `yaml:"dim,omitempty"`
	Range    *[2]float64 `yaml:"range,omitempty"`
	Optional bool        `yaml:"optional,omitempty"`
	Computed string      `yaml:"computed,omitempty"`
}

type Transform string

const (
	TransformNone  Transform = ""
	TransformLog1p Transform = "log1p"
	TransformSqrt  Transform = "sqrt"
	TransformClamp Transform = "clamp"
)

type InteractionSpec struct {
	Weight    float64   `yaml:"weight"`
	Value     string    `yaml:"value,omitempty"`
	Transform Transform `yaml:"transform,omitempty"`
	HalfLife  Duration  `yaml:"half_life,omitempty"`
	Target    string    `yaml:"target,omitempty"`
}

type Metric string

const (
	MetricJaccard  Metric = "jaccard"
	MetricCosine   Metric = "cosine"
	MetricEquals   Metric = "equals"
	MetricLogRatio Metric = "log_ratio"
)

var Metrics = []Metric{MetricJaccard, MetricCosine, MetricEquals, MetricLogRatio}

func (m Metric) Valid() bool { return slices.Contains(Metrics, m) }

type SimilarityTerm struct {
	On     StringList `yaml:"on,omitempty"`
	Via    string     `yaml:"via,omitempty"`
	Metric Metric     `yaml:"metric"`
	Weight float64    `yaml:"weight,omitempty"`
}

// SignalSpec keeps type-specific keys (window, on, half_life...) in Params, so a new signal
// type needs no change here.
type SignalSpec struct {
	Type    string         `yaml:"type"`
	Default float64        `yaml:"default"`
	Params  map[string]any `yaml:",inline"`
}

type KnobSpec struct {
	ID    string            `yaml:"id"`
	Label string            `yaml:"label"`
	Range [2]float64        `yaml:"range"`
	Maps  map[string]string `yaml:"maps"`
}

// Constraint is a hard filter; exactly one of Require or Exclude is set.
type Constraint struct {
	Require *Condition `yaml:"require,omitempty"`
	Exclude *Condition `yaml:"exclude,omitempty"`
}

type Condition struct {
	Attribute  string   `yaml:"attribute,omitempty"`
	Equals     any      `yaml:"equals,omitempty"`
	In         string   `yaml:"in,omitempty"`
	Gt         string   `yaml:"gt,omitempty"`
	Lt         string   `yaml:"lt,omitempty"`
	Interacted []string `yaml:"interacted,omitempty"`
	Contains   string   `yaml:"contains,omitempty"`
	CountGTE   int      `yaml:"count_gte,omitempty"`
	Within     Duration `yaml:"within,omitempty"`
	When       string   `yaml:"when,omitempty"`
}
