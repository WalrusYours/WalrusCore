package schema

import (
	"bytes"
	"fmt"
	"strconv"

	"gopkg.in/yaml.v3"
)

// Types for the schema v2 sections (.claude/SCHEMA-V2.md, Appendix A).

type Meta struct {
	Name          string   `yaml:"name,omitempty"`
	Description   string   `yaml:"description,omitempty"`
	Owners        []string `yaml:"owners,omitempty"`
	Locales       []string `yaml:"locales,omitempty"`
	DefaultLocale string   `yaml:"default_locale,omitempty"`
}

// ContextField is a value the host may send with a request, or one the engine derives from
// the request time.
type ContextField struct {
	Type     AttrType    `yaml:"type"`
	Of       string      `yaml:"of,omitempty"`
	Values   []string    `yaml:"values,omitempty"`
	Range    *[2]float64 `yaml:"range,omitempty"`
	Optional bool        `yaml:"optional,omitempty"`
	Derive   string      `yaml:"derive,omitempty"`
}

var ContextDerivations = []string{"hour", "weekday", "month"}

// Rule runs after scoring and re-ranking. Exactly one action is set.
type Rule struct {
	ID    string     `yaml:"id"`
	For   StringList `yaml:"for,omitempty"`
	When  string     `yaml:"when,omitempty"`
	Place *Place     `yaml:"place,omitempty"`
	Boost *float64   `yaml:"boost,omitempty"`
	Bury  bool       `yaml:"bury,omitempty"`
	Quota *Quota     `yaml:"quota,omitempty"`
	Cap   *Cap       `yaml:"cap,omitempty"`
	Pin   *Pin       `yaml:"pin,omitempty"`
}

type Place struct {
	At []int `yaml:"at"`
}

// Quota is either a per-attribute cap (attribute, max, per) or a minimum share of items
// matching a condition in the top positions (when, min_share, top).
type Quota struct {
	Attribute string  `yaml:"attribute,omitempty"`
	Max       int     `yaml:"max,omitempty"`
	Per       int     `yaml:"per,omitempty"`
	When      string  `yaml:"when,omitempty"`
	MinShare  float64 `yaml:"min_share,omitempty"`
	Top       int     `yaml:"top,omitempty"`
}

type Cap struct {
	Exposure string   `yaml:"exposure"`
	Max      int      `yaml:"max"`
	Within   Duration `yaml:"within"`
}

type Pin struct {
	IDs string `yaml:"ids"`
	At  int    `yaml:"at"`
}

// Seeds: what a recommendation is relative to.
const (
	SeedUser    = "user"
	SeedItem    = "item"
	SeedItems   = "items"
	SeedSession = "session"
	SeedUsers   = "users"
	SeedNone    = "none"
)

var Seeds = []string{SeedUser, SeedItem, SeedItems, SeedSession, SeedUsers, SeedNone}

type RecommenderSpec struct {
	Label         Text              `yaml:"label,omitempty"`
	For           StringList        `yaml:"for"`
	Seed          string            `yaml:"seed,omitempty"`
	SeedAggregate string            `yaml:"seed_aggregate,omitempty"`
	BlendUser     *float64          `yaml:"blend_user,omitempty"`
	Candidates    []CandidateSource `yaml:"candidates,omitempty"`
	Signals       []string          `yaml:"signals,omitempty"`
	Weights       map[string]Expr   `yaml:"weights,omitempty"`
	Constraints   []string          `yaml:"constraints,omitempty"`
	Rules         []string          `yaml:"rules,omitempty"`
	Rerank        *Rerank           `yaml:"rerank,omitempty"`
	Mix           *Mix              `yaml:"mix,omitempty"`
	Knobs         []string          `yaml:"knobs,omitempty"`
	Limit         *Limit            `yaml:"limit,omitempty"`
	Fallback      []Fallback        `yaml:"fallback,omitempty"`
	Group         *GroupSpec        `yaml:"group,omitempty"`
	Reciprocal    *Reciprocal       `yaml:"reciprocal,omitempty"`
}

// EffectiveSeed is Seed, user when unset.
func (r RecommenderSpec) EffectiveSeed() string {
	if r.Seed == "" {
		return SeedUser
	}
	return r.Seed
}

var CandidateSources = []string{
	"item_neighbors", "user_neighbors", "trend", "popular", "fresh", "unexposed",
	"co_occurrence", "sequence", "mutuals", "provided",
}

type CandidateSource struct {
	Source string `yaml:"source"`
	Signal string `yaml:"signal,omitempty"`
	Cap    int    `yaml:"cap,omitempty"`
}

type Rerank struct {
	Diversity *Diversity `yaml:"diversity,omitempty"`
}

type Diversity struct {
	On     string  `yaml:"on"`
	Lambda float64 `yaml:"lambda"`
}

type Mix struct {
	By     string             `yaml:"by"`
	Shares map[string]float64 `yaml:"shares,omitempty"`
}

type Limit struct {
	Default int `yaml:"default"`
	Max     int `yaml:"max"`
}

type Fallback struct {
	When string `yaml:"when"`
	Use  string `yaml:"use"`
}

type GroupSpec struct {
	Aggregate string `yaml:"aggregate"`
}

type Reciprocal struct {
	Recommender string `yaml:"recommender"`
	Combine     string `yaml:"combine"`
}

// MetricSpec has exactly one form: ratio, mean (with per), returning_users, list or system.
type MetricSpec struct {
	Ratio          []string `yaml:"ratio,omitempty"`
	Mean           string   `yaml:"mean,omitempty"`
	Per            string   `yaml:"per,omitempty"`
	ReturningUsers Duration `yaml:"returning_users,omitempty"`
	List           string   `yaml:"list,omitempty"`
	System         string   `yaml:"system,omitempty"`
	Quantile       float64  `yaml:"quantile,omitempty"`
	Attribution    Duration `yaml:"attribution,omitempty"`
}

var ListMetrics = []string{"ild", "coverage", "novelty", "gini"}

type ExperimentSpec struct {
	Recommender      string             `yaml:"recommender"`
	Status           string             `yaml:"status"`
	Unit             string             `yaml:"unit,omitempty"`
	Layer            string             `yaml:"layer,omitempty"`
	Traffic          float64            `yaml:"traffic"`
	Audience         *Audience          `yaml:"audience,omitempty"`
	Method           string             `yaml:"method,omitempty"`
	Variants         map[string]Variant `yaml:"variants"`
	Goal             Goal               `yaml:"goal"`
	Guardrails       []Guardrail        `yaml:"guardrails,omitempty"`
	Stop             *Stop              `yaml:"stop,omitempty"`
	Gate             *Gate              `yaml:"gate,omitempty"`
	OnFinish         string             `yaml:"on_finish,omitempty"`
	ShadowPrecompute bool               `yaml:"shadow_precompute,omitempty"`
}

var (
	ExperimentStatuses = []string{"draft", "running", "paused", "stopped", "promoted"}
	ExperimentUnits    = []string{"user", "session", "request"}
	ExperimentMethods  = []string{"ab", "interleaving", "bandit"}
	ExperimentFinishes = []string{"propose", "promote", "none"}
)

type Audience struct {
	When string `yaml:"when"`
}

// Variant is a schema patch: paths (SCHEMA-V2.md Appendix B) to values.
type Variant struct {
	Share float64        `yaml:"share"`
	Set   map[string]any `yaml:"set,omitempty"`
}

type Goal struct {
	Metric    string  `yaml:"metric"`
	Direction string  `yaml:"direction"`
	MinEffect float64 `yaml:"min_effect,omitempty"`
}

type Guardrail struct {
	Metric      string   `yaml:"metric"`
	MaxIncrease *float64 `yaml:"max_increase,omitempty"`
	Max         *float64 `yaml:"max,omitempty"`
}

type Stop struct {
	Sequential  bool     `yaml:"sequential,omitempty"`
	Confidence  float64  `yaml:"confidence,omitempty"`
	MinUnits    int      `yaml:"min_units,omitempty"`
	MaxDuration Duration `yaml:"max_duration,omitempty"`
}

type Gate struct {
	Offline *OfflineGate `yaml:"offline,omitempty"`
}

type OfflineGate struct {
	Metric  string  `yaml:"metric"`
	MaxDrop float64 `yaml:"max_drop"`
}

type Holdout struct {
	Share       float64 `yaml:"share"`
	Recommender string  `yaml:"recommender"`
}

type Evaluation struct {
	Split   *Split               `yaml:"split,omitempty"`
	K       []int                `yaml:"k,omitempty"`
	Metrics []string             `yaml:"metrics,omitempty"`
	Grid    map[string][]float64 `yaml:"grid,omitempty"`
}

type Split struct {
	By          string   `yaml:"by"`
	HoldoutLast Duration `yaml:"holdout_last,omitempty"`
}

// OfflineMetrics are the ALGORITHMS.md 13 metric names.
var OfflineMetrics = []string{"precision", "recall", "ndcg", "ild", "coverage", "novelty", "gini"}

type Privacy struct {
	Retention        *RetentionPolicy `yaml:"retention,omitempty"`
	OptOut           *OptOut          `yaml:"opt_out,omitempty"`
	CrossTypeConsent *Consent         `yaml:"cross_type_consent,omitempty"`
}

type RetentionPolicy struct {
	Default Duration `yaml:"default"`
}

type OptOut struct {
	Attribute string `yaml:"attribute"`
	Equals    any    `yaml:"equals"`
	Use       string `yaml:"use"`
}

type Consent struct {
	Attribute   string   `yaml:"attribute"`
	RequiredFor []string `yaml:"required_for"`
}

type Feedback struct {
	Reasons map[string]Reason `yaml:"reasons"`
}

// Reason is one answer to "why not interested?". The item the user reacted to supplies the
// specifics through its attributes, so a few reasons cover any catalogue.
type Reason struct {
	Label     Text      `yaml:"label"`
	AppliesTo AppliesTo `yaml:"applies_to"`
	Effect    Effect    `yaml:"effect"`
	Until     string    `yaml:"until,omitempty"`
	Also      string    `yaml:"also,omitempty"`
}

// AppliesTo is "item", { same: <attribute> } or { similar: <0..1>, terms: [...] }.
type AppliesTo struct {
	Item    bool       `yaml:"-"`
	Same    string     `yaml:"same,omitempty"`
	Similar float64    `yaml:"similar,omitempty"`
	Terms   StringList `yaml:"terms,omitempty"`
}

func (a *AppliesTo) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		if n.Value != "item" {
			return fmt.Errorf("line %d: applies_to is item, { same: <attribute> } or { similar: <0..1> }", n.Line)
		}
		*a = AppliesTo{Item: true}
		return nil
	}
	type plain AppliesTo
	var p plain
	if err := strictDecode(n, &p); err != nil {
		return err
	}
	*a = AppliesTo(p)
	return nil
}

func (a AppliesTo) MarshalYAML() (any, error) {
	if a.Item {
		return "item", nil
	}
	type plain AppliesTo
	return plain(a), nil
}

// Effect is "hide", "satiate", { penalty: <0..1> } or { taste: <negative> }.
type Effect struct {
	Name    string   `yaml:"-"`
	Penalty *float64 `yaml:"penalty,omitempty"`
	Taste   *float64 `yaml:"taste,omitempty"`
}

func (e *Effect) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		*e = Effect{Name: n.Value}
		return nil
	}
	type plain Effect
	var p plain
	if err := strictDecode(n, &p); err != nil {
		return err
	}
	*e = Effect(p)
	return nil
}

func (e Effect) MarshalYAML() (any, error) {
	if e.Name != "" {
		return e.Name, nil
	}
	type plain Effect
	return plain(e), nil
}

var FeedbackEffects = []string{"hide", "satiate"}

type Recurrence struct {
	Of         StringList `yaml:"of"`
	GroupBy    string     `yaml:"group_by"`
	Learn      *Learn     `yaml:"learn,omitempty"`
	NeverBelow float64    `yaml:"never_below,omitempty"`
	From       string     `yaml:"from,omitempty"`
}

type Learn struct {
	Window   Duration `yaml:"window"`
	MinUsers int      `yaml:"min_users,omitempty"`
}

// Fulfils is true or { after: n }: the interaction satisfies a need, after n events.
type Fulfils struct {
	After int `yaml:"after,omitempty"`
}

func (f *Fulfils) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		var b bool
		if err := n.Decode(&b); err != nil || !b {
			return fmt.Errorf("line %d: fulfils is true or { after: <count> }", n.Line)
		}
		*f = Fulfils{}
		return nil
	}
	type plain Fulfils
	var p plain
	if err := strictDecode(n, &p); err != nil {
		return err
	}
	*f = Fulfils(p)
	return nil
}

func (f Fulfils) MarshalYAML() (any, error) {
	if f.After == 0 {
		return true, nil
	}
	return map[string]int{"after": f.After}, nil
}

// Text is a user-facing string: plain, or one string per locale.
type Text struct {
	Plain   string
	Locales map[string]string
}

func (t *Text) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		*t = Text{Plain: n.Value}
		return nil
	case yaml.MappingNode:
		m := map[string]string{}
		if err := n.Decode(&m); err != nil {
			return fmt.Errorf("line %d: a text is a string or a map from locale to string", n.Line)
		}
		*t = Text{Locales: m}
		return nil
	}
	return fmt.Errorf("line %d: a text is a string or a map from locale to string", n.Line)
}

func (t Text) MarshalYAML() (any, error) {
	if t.Locales != nil {
		return t.Locales, nil
	}
	return t.Plain, nil
}

func (t Text) IsZero() bool { return t.Plain == "" && len(t.Locales) == 0 }

// In returns the text for a locale, falling back to the default locale, then to the plain
// string.
func (t Text) In(locale, fallback string) string {
	if t.Locales == nil {
		return t.Plain
	}
	if s, ok := t.Locales[locale]; ok && s != "" {
		return s
	}
	return t.Locales[fallback]
}

// Expr is a number or an expression, for values that may depend on the request.
type Expr string

func (e *Expr) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.ScalarNode {
		return fmt.Errorf("line %d: expected a number or an expression", n.Line)
	}
	*e = Expr(n.Value)
	return nil
}

func (e Expr) MarshalYAML() (any, error) {
	if f, err := strconv.ParseFloat(string(e), 64); err == nil {
		return f, nil
	}
	return string(e), nil
}

// Constant returns the value when the expression is a plain number.
func (e Expr) Constant() (float64, bool) {
	f, err := strconv.ParseFloat(string(e), 64)
	return f, err == nil
}

// strictDecode decodes a node rejecting unknown keys, like Parse does for the whole document:
// yaml.v3 does not carry KnownFields into custom unmarshalers.
func strictDecode(n *yaml.Node, out any) error {
	b, err := yaml.Marshal(n)
	if err != nil {
		return err
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("line %d: %w", n.Line, err)
	}
	return nil
}
