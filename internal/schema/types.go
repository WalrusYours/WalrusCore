// Package schema models the YAML a platform pushes. These structs describe the schema
// language itself, so a schema push changes their contents, never the code.
package schema

import (
	"slices"

	"github.com/timurcravtov/walrus/internal/domain"
)

type Schema struct {
	Version       int                          `yaml:"version"`
	Meta          *Meta                        `yaml:"meta,omitempty"`
	Entities      map[string]EntitySpec        `yaml:"entities"`
	Interactions  map[string]InteractionSpec   `yaml:"interactions"`
	Context       map[string]ContextField      `yaml:"context,omitempty"`
	Similarity    map[string][]SimilarityTerm  `yaml:"similarity,omitempty"`
	Signals       map[string]SignalSpec        `yaml:"signals"`
	Constraints   []Constraint                 `yaml:"constraints,omitempty"`
	Rules         []Rule                       `yaml:"rules,omitempty"`
	Knobs         []KnobSpec                   `yaml:"knobs,omitempty"`
	Presets       map[string]domain.KnobValues `yaml:"presets,omitempty"`
	Recommenders  map[string]RecommenderSpec   `yaml:"recommenders,omitempty"`
	Metrics       map[string]MetricSpec        `yaml:"metrics,omitempty"`
	Experiments   map[string]ExperimentSpec    `yaml:"experiments,omitempty"`
	Holdout       *Holdout                     `yaml:"holdout,omitempty"`
	Evaluation    *Evaluation                  `yaml:"evaluation,omitempty"`
	Privacy       *Privacy                     `yaml:"privacy,omitempty"`
	Feedback      *Feedback                    `yaml:"feedback,omitempty"`
	Recurrence    *Recurrence                  `yaml:"recurrence,omitempty"`
	Recommendable StringList                   `yaml:"recommendable,omitempty"`
}

type EntitySpec struct {
	Key        string                   `yaml:"key"`
	Parent     string                   `yaml:"parent,omitempty"`
	Lifecycle  *Lifecycle               `yaml:"lifecycle,omitempty"`
	Attributes map[string]AttributeSpec `yaml:"attributes,omitempty"`
}

// Lifecycle names the attributes that give an item its age, its end, and whether it is live.
type Lifecycle struct {
	Created string        `yaml:"created,omitempty"`
	Expires string        `yaml:"expires,omitempty"`
	Active  *ActiveStatus `yaml:"active,omitempty"`
}

type ActiveStatus struct {
	Attribute string   `yaml:"attribute"`
	In        []string `yaml:"in"`
}

type AttributeSpec struct {
	Type      AttrType    `yaml:"type"`
	Of        string      `yaml:"of,omitempty"`
	Entity    string      `yaml:"entity,omitempty"`
	Dim       int         `yaml:"dim,omitempty"`
	Range     *[2]float64 `yaml:"range,omitempty"`
	Optional  bool        `yaml:"optional,omitempty"`
	Computed  string      `yaml:"computed,omitempty"`
	Vocab     string      `yaml:"vocab,omitempty"`
	Sensitive bool        `yaml:"sensitive,omitempty"`
}

type Transform string

const (
	TransformNone  Transform = ""
	TransformLog1p Transform = "log1p"
	TransformSqrt  Transform = "sqrt"
	TransformClamp Transform = "clamp"
)

// Interaction kinds. A v1 interaction has no kind; its weight's sign decides.
const (
	KindPositive   = "positive"
	KindNegative   = "negative"
	KindExplicit   = "explicit"
	KindExposure   = "exposure"
	KindConversion = "conversion"
)

var InteractionKinds = []string{KindPositive, KindNegative, KindExplicit, KindExposure, KindConversion}

type InteractionSpec struct {
	Kind      string               `yaml:"kind,omitempty"`
	Weight    float64              `yaml:"weight"`
	Value     string               `yaml:"value,omitempty"`
	Transform Transform            `yaml:"transform,omitempty"`
	HalfLife  Duration             `yaml:"half_life,omitempty"`
	Target    string               `yaml:"target,omitempty"`
	Locked    bool                 `yaml:"locked,omitempty"`
	Dedupe    string               `yaml:"dedupe,omitempty"`
	Retention Duration             `yaml:"retention,omitempty"`
	Session   bool                 `yaml:"session,omitempty"`
	Fulfils   *Fulfils             `yaml:"fulfils,omitempty"`
	Fields    map[string]FieldSpec `yaml:"fields,omitempty"`
}

// EffectiveKind is Kind, or what the weight's sign implies for a v1 interaction.
func (i InteractionSpec) EffectiveKind() string {
	switch {
	case i.Kind != "":
		return i.Kind
	case i.Weight < 0:
		return KindNegative
	}
	return KindPositive
}

// Positive reports whether the interaction can count as liking an item.
func (i InteractionSpec) Positive() bool {
	switch i.EffectiveKind() {
	case KindPositive, KindConversion:
		return i.Weight > 0
	case KindExplicit:
		return true
	}
	return false
}

// FieldSpec is a named value an interaction event carries, such as the collection an item
// was added to.
type FieldSpec struct {
	Type     AttrType `yaml:"type"`
	Entity   string   `yaml:"entity,omitempty"`
	Optional bool     `yaml:"optional,omitempty"`
}

type Metric string

const (
	MetricJaccard  Metric = "jaccard"
	MetricCosine   Metric = "cosine"
	MetricEquals   Metric = "equals"
	MetricLogRatio Metric = "log_ratio"
	// MetricCloseness is 1 minus the gap between two numbers as a share of the catalogue's range.
	MetricCloseness Metric = "closeness"
)

var Metrics = []Metric{MetricJaccard, MetricCosine, MetricEquals, MetricLogRatio, MetricCloseness}

func (m Metric) Valid() bool { return slices.Contains(Metrics, m) }

type SimilarityTerm struct {
	ID     string     `yaml:"id,omitempty"`
	On     StringList `yaml:"on,omitempty"`
	Via    string     `yaml:"via,omitempty"`
	Metric Metric     `yaml:"metric"`
	Weight float64    `yaml:"weight,omitempty"`
	Locked bool       `yaml:"locked,omitempty"`
	K      int        `yaml:"k,omitempty"`
	Index  string     `yaml:"index,omitempty"`

	// user similarity (via: interactions)
	Of          StringList `yaml:"of,omitempty"`
	MinShared   int        `yaml:"min_shared,omitempty"`
	DampPopular bool       `yaml:"damp_popular,omitempty"`
	From        StringList `yaml:"from,omitempty"`

	// similarity.cross
	Between StringList `yaml:"between,omitempty"`
}

// TermID is the term's id, or its attribute names joined with "_".
func (t SimilarityTerm) TermID() string {
	if t.ID != "" {
		return t.ID
	}
	id := ""
	for i, on := range t.On {
		if i > 0 {
			id += "_"
		}
		id += on
	}
	return id
}

// SignalSpec keeps type-specific keys (window, on, half_life...) in Params, so a new signal
// type needs no change here. The keys every type accepts are fields.
type SignalSpec struct {
	Type      string         `yaml:"type"`
	Default   float64        `yaml:"default"`
	Locked    bool           `yaml:"locked,omitempty"`
	For       StringList     `yaml:"for,omitempty"`
	From      StringList     `yaml:"from,omitempty"`
	Normalise string         `yaml:"normalise,omitempty"`
	Transform string         `yaml:"transform,omitempty"`
	Cap       *float64       `yaml:"cap,omitempty"`
	Label     Text           `yaml:"label,omitempty"`
	Explain   Text           `yaml:"explain,omitempty"`
	Params    map[string]any `yaml:",inline"`
}

var Normalisations = []string{"minmax", "rank", "zscore", "none"}

// Knob kinds.
const (
	KnobSlider = "slider"
	KnobToggle = "toggle"
	KnobChoice = "choice"
)

type KnobSpec struct {
	ID        string            `yaml:"id"`
	Kind      string            `yaml:"kind,omitempty"`
	Label     Text              `yaml:"label"`
	Low       Text              `yaml:"low,omitempty"`
	High      Text              `yaml:"high,omitempty"`
	Group     Text              `yaml:"group,omitempty"`
	Help      Text              `yaml:"help,omitempty"`
	Range     [2]float64        `yaml:"range"`
	Options   []KnobOption      `yaml:"options,omitempty"`
	Default   *float64          `yaml:"default,omitempty"`
	Scope     []string          `yaml:"scope,omitempty"`
	DependsOn string            `yaml:"depends_on,omitempty"`
	Maps      map[string]string `yaml:"maps"`
	Locked    bool              `yaml:"locked,omitempty"`
}

type KnobOption struct {
	Value float64 `yaml:"value"`
	Label Text    `yaml:"label"`
}

// EffectiveKind is Kind, slider when unset.
func (k KnobSpec) EffectiveKind() string {
	if k.Kind == "" {
		return KnobSlider
	}
	return k.Kind
}

// EffectiveRange is the declared range, [0, 1] for a toggle, and the option values' span for
// a choice that declares none.
func (k KnobSpec) EffectiveRange() [2]float64 {
	switch {
	case k.Range != [2]float64{}:
		return k.Range
	case k.EffectiveKind() == KnobToggle:
		return [2]float64{0, 1}
	case k.EffectiveKind() == KnobChoice && len(k.Options) > 0:
		lo, hi := k.Options[0].Value, k.Options[0].Value
		for _, o := range k.Options {
			lo, hi = min(lo, o.Value), max(hi, o.Value)
		}
		return [2]float64{lo, hi}
	}
	return k.Range
}

// Constraint is a hard filter; exactly one of Require or Exclude is set.
type Constraint struct {
	ID      string     `yaml:"id,omitempty"`
	For     StringList `yaml:"for,omitempty"`
	Require *Condition `yaml:"require,omitempty"`
	Exclude *Condition `yaml:"exclude,omitempty"`
}

type Condition struct {
	Attribute  string   `yaml:"attribute,omitempty"`
	Equals     any      `yaml:"equals,omitempty"`
	In         string   `yaml:"in,omitempty"`
	Gt         string   `yaml:"gt,omitempty"`
	Gte        string   `yaml:"gte,omitempty"`
	Lt         string   `yaml:"lt,omitempty"`
	Lte        string   `yaml:"lte,omitempty"`
	WithinKm   float64  `yaml:"within_km,omitempty"` // the attribute (a geo point) is this near Of
	Of         string   `yaml:"of,omitempty"`        // the place WithinKm is measured from
	Interacted []string `yaml:"interacted,omitempty"`
	Contains   string   `yaml:"contains,omitempty"`
	CountGTE   int      `yaml:"count_gte,omitempty"`
	Within     Duration `yaml:"within,omitempty"`
	When       string   `yaml:"when,omitempty"`
	InSeed     bool     `yaml:"in_seed,omitempty"`
}
