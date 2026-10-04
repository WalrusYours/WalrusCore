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
	identRe    = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	userRefRe  = regexp.MustCompile(`\$user\.([A-Za-z_][A-Za-z0-9_]*)`)
	ctxRefRe   = regexp.MustCompile(`\$context\.([A-Za-z_][A-Za-z0-9_]*)`)
	localeRe   = regexp.MustCompile(`^[a-z]{2}(-[A-Z]{2})?$`)
	reservedEn = []string{"cross"} // `similarity.cross` holds cross-type terms
)

// SignalTypes lists every registered signal type. A new type is code: add it here and to
// signalKeys with its parameters, then teach the validator its references.
var SignalTypes = []string{
	"item_neighbors", "user_neighbors", "own_history", "global_count",
	"age_decay", "low_exposure", "attribute_match", "diversity_rerank", "trend",
	"co_occurrence", "sequence", "mutual_connections", "attribute_target", "attribute_value",
	"context_match", "provided", "formula", "satiation", "recurrence",
}

// trendKeys are the parameters of a `trend` signal.
var trendKeys = []string{"window", "baseline", "against", "on", "of", "count", "min", "ratio"}

// signalKeys are the type-specific keys each signal type accepts. The keys
// every type accepts (default, for, from, normalise...) are SignalSpec fields.
var signalKeys = map[string][]string{
	"item_neighbors":     {"terms"},
	"user_neighbors":     {"of", "min_neighbors"},
	"own_history":        {"of"},
	"global_count":       {"window", "of"},
	"trend":              trendKeys,
	"age_decay":          {"on", "half_life"},
	"low_exposure":       {"of"},
	"attribute_match":    {"on", "against"},
	"diversity_rerank":   {"on"},
	"co_occurrence":      {"of", "group_by", "order", "window", "min_support", "measure"},
	"sequence":           {"of", "gap"},
	"mutual_connections": {"via"},
	"attribute_target":   {"on", "target"},
	"attribute_value":    {"on"},
	"context_match":      {"on", "against"},
	"provided":           {"name"},
	"formula":            {"expr"},
	"satiation":          {"of", "by", "level"},
	"recurrence":         {"of"},
}

var MetaTargets = []string{"interactions.half_life_scale", "constraint.energy_center"}

var builtinTransforms = []string{"", "identity", "log1p", "sqrt", "clamp"}

type Issue struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

// Ranked returns the first recommendable entity type: the one the implicit default
// recommender ranks.
func (s *Schema) Ranked() (string, bool) {
	all, ok := s.RankedAll()
	if !ok || len(all) == 0 {
		return "", false
	}
	return all[0], true
}

// RankedAll returns the recommendable entity types: `recommendable`, or the only entity
// besides "user". ok is false when one of them is not declared, or none can be inferred.
func (s *Schema) RankedAll() ([]string, bool) {
	if len(s.Recommendable) > 0 {
		for _, r := range s.Recommendable {
			if _, ok := s.Entities[r]; !ok {
				return s.Recommendable, false
			}
		}
		return s.Recommendable, true
	}
	var others []string
	for _, name := range s.EntityTypes() {
		if name != "user" {
			others = append(others, name)
		}
	}
	if len(others) == 1 {
		return others, true
	}
	return nil, false
}

// Validate checks everything Parse cannot: references, identifiers, types, expressions,
// computed attributes, tiers and experiment variants. It returns all issues at once, in a
// stable order.
func Validate(s *Schema) []Issue {
	v := &validator{s: s}
	v.run()
	return v.issues
}

type validator struct {
	s       *Schema
	issues  []Issue
	ranked  string   // first recommendable entity
	rankedN []string // every recommendable entity
	// scoredBy is every entity some recommender or `recommendable` returns.
	scoredBy []string
	// pending are experiment variants, checked as whole schemas once the base is checked.
	pending []pendingVariant
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
	v.meta()
	v.entities()
	if _, ok := s.Entities["user"]; !ok && len(s.Entities) > 0 {
		v.add("entities.user", `an entity named "user" is required (constraints and signals refer to $user)`)
	}
	if all, ok := s.RankedAll(); ok {
		v.rankedN, v.ranked = all, all[0]
	} else if len(s.Entities) > 0 {
		switch {
		case len(s.Recommendable) > 0:
			for _, r := range s.Recommendable {
				if _, ok := s.Entities[r]; !ok {
					v.add("recommendable", "%q is not a declared entity", r)
				}
			}
		default:
			v.add("recommendable", `name the entity that is ranked (more than one entity besides "user" is declared)`)
		}
	}
	v.scoredBy = slices.Clone(v.rankedN)
	for _, name := range sortedKeys(s.Recommenders) {
		for _, e := range s.Recommenders[name].For {
			if _, ok := s.Entities[e]; ok && !slices.Contains(v.scoredBy, e) {
				v.scoredBy = append(v.scoredBy, e)
			}
		}
	}
	v.interactions()
	v.context()
	v.similarity()
	v.signals()
	v.constraints()
	v.rules()
	v.knobs()
	v.presets()
	v.recommenders()
	v.metrics()
	v.experiments()
	v.holdout()
	v.evaluation()
	v.privacy()
	v.feedback()
	v.recurrence()
	v.checkVariants()
}

func (v *validator) entities() {
	if len(v.s.Entities) == 0 {
		v.add("entities", "declare at least one entity")
		return
	}
	vocabType := map[string]AttrType{}
	vocabAt := map[string]string{}
	for _, name := range v.s.EntityTypes() {
		e := v.s.Entities[name]
		base := "entities." + name
		v.ident(base, name)
		if slices.Contains(reservedEn, name) {
			v.add(base, "%q is reserved (similarity.cross holds cross-type terms); rename the entity", name)
		}
		if e.Key == "" {
			v.add(base+".key", "key is required (the name of the id field, normally id)")
		}
		attrs := slices.Sorted(mapsKeys(e.Attributes))
		for _, an := range attrs {
			a := e.Attributes[an]
			p := base + ".attributes." + an
			v.ident(p, an)
			v.attribute(p, a)
			if a.Vocab != "" {
				v.ident(p+".vocab", a.Vocab)
				if t, seen := vocabType[a.Vocab]; seen && t != a.Type {
					v.add(p+".vocab", "vocab %q is %s at %s but %s here; attributes sharing a vocab must share a type", a.Vocab, t, vocabAt[a.Vocab], a.Type)
				} else if !seen {
					vocabType[a.Vocab], vocabAt[a.Vocab] = a.Type, p
				}
			}
		}
		if _, err := e.NewComputer(); err != nil {
			v.add(base, "%v", err)
		}
		if e.Parent != "" {
			a, ok := e.Attributes[e.Parent]
			switch {
			case !ok:
				v.add(base+".parent", "%q is not an attribute of %s", e.Parent, name)
			case a.Type != TypeRef || a.Entity != name:
				v.add(base+".parent", "parent must be a ref attribute pointing to %s itself", name)
			}
		}
		if lc := e.Lifecycle; lc != nil {
			for key, an := range map[string]string{"created": lc.Created, "expires": lc.Expires} {
				if an == "" {
					continue
				}
				if a, ok := e.Attributes[an]; !ok {
					v.add(base+".lifecycle."+key, "%q is not an attribute of %s", an, name)
				} else if a.Type != TypeTimestamp {
					v.add(base+".lifecycle."+key, "%s needs a timestamp attribute, %q is %s", key, an, a.Type)
				}
			}
			if lc.Active != nil {
				if _, ok := e.Attributes[lc.Active.Attribute]; !ok {
					v.add(base+".lifecycle.active.attribute", "%q is not an attribute of %s", lc.Active.Attribute, name)
				}
				if len(lc.Active.In) == 0 {
					v.add(base+".lifecycle.active.in", "list the values that mean active")
				}
			}
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

var fieldTypes = []AttrType{TypeCategorical, TypeString, TypeInt, TypeFloat, TypeBool, TypeTimestamp, TypeRef}

var dedupes = []string{"none", "per_item", "per_item_per_day"}

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

		if i.Kind != "" && !slices.Contains(InteractionKinds, i.Kind) {
			v.add(p+".kind", "kind must be one of %s", strings.Join(InteractionKinds, ", "))
		}
		switch i.EffectiveKind() {
		case KindExposure:
			if i.Weight != 0 || i.Value != "" || i.Fulfils != nil || i.HalfLife != 0 {
				v.add(p, "an exposure records what was shown; it has no weight, value, fulfils or half_life")
			}
		case KindNegative:
			if i.Weight > 0 {
				v.add(p+".weight", "a negative interaction needs a weight below 0")
			}
		case KindPositive, KindConversion:
			if i.Weight < 0 {
				v.add(p+".weight", "a %s interaction needs a weight of 0 or more (use kind: negative)", i.EffectiveKind())
			}
		}
		if i.Dedupe != "" && !slices.Contains(dedupes, i.Dedupe) {
			v.add(p+".dedupe", "dedupe must be one of %s", strings.Join(dedupes, ", "))
		}
		if i.Retention < 0 {
			v.add(p+".retention", "retention must not be negative")
		}
		if i.Fulfils != nil {
			if k := i.EffectiveKind(); k == KindNegative || k == KindExposure {
				v.add(p+".fulfils", "a %s interaction cannot fulfil a need", k)
			}
			if i.Fulfils.After < 0 {
				v.add(p+".fulfils.after", "after must be 1 or more")
			}
		}
		for _, fn := range sortedKeys(i.Fields) {
			f := i.Fields[fn]
			fp := p + ".fields." + fn
			v.ident(fp, fn)
			if !slices.Contains(fieldTypes, f.Type) {
				v.add(fp+".type", "a field is categorical, string, int, float, bool, timestamp or ref, got %q", f.Type)
			}
			if f.Type == TypeRef {
				if _, ok := v.s.Entities[f.Entity]; !ok {
					v.add(fp+".entity", "ref must name a declared entity, got %q", f.Entity)
				}
			}
		}
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
		if et == "cross" {
			v.crossSimilarity()
			continue
		}
		e, ok := v.s.Entities[et]
		if !ok {
			v.add("similarity."+et, "not a declared entity")
			continue
		}
		ids := map[string]bool{}
		for i, t := range v.s.Similarity[et] {
			p := fmt.Sprintf("similarity.%s[%d]", et, i)
			if !t.Metric.Valid() {
				v.add(p+".metric", "metric must be one of jaccard, cosine, equals, log_ratio, closeness")
			}
			if len(t.Between) > 0 {
				v.add(p+".between", "between belongs to similarity.cross")
			}
			if t.K < 0 {
				v.add(p+".k", "k must be 1 or more")
			}
			userKeys := len(t.Of) > 0 || t.MinShared != 0 || t.DampPopular || len(t.From) > 0
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
				if t.Metric == MetricCloseness {
					switch a := e.Attributes[t.On[0]]; {
					case len(t.On) != 1:
						v.add(p+".on", "closeness compares one number; list one attribute, or use cosine for a group")
					case a.Type != TypeFloat && a.Type != TypeInt:
						v.add(p+".on", "closeness needs a float or int attribute, %q is %s", t.On[0], a.Type)
					}
				}
				id := t.TermID()
				v.ident(p+".id", id)
				if ids[id] {
					v.add(p+".id", "duplicate term id %q in %s; give one an explicit id", id, et)
				}
				ids[id] = true
				if t.Index != "" {
					if t.Index != "hnsw" {
						v.add(p+".index", `index must be "hnsw"`)
					}
					for _, on := range t.On {
						if a, ok := e.Attributes[on]; ok && a.Type != TypeVector {
							v.add(p+".index", "an hnsw index needs vector attributes, %q is %s", on, a.Type)
						}
					}
				}
				if userKeys {
					v.add(p, "of, min_shared, damp_popular and from apply only to via: interactions terms")
				}
			case t.Via == "interactions":
				v.interactionNames(p+".of", t.Of, func(name string, spec InteractionSpec) string {
					if !spec.Positive() {
						return "only positive interactions define similar people"
					}
					return ""
				})
				if t.MinShared < 0 {
					v.add(p+".min_shared", "min_shared must not be negative")
				}
				v.targetedEntities(p+".from", t.From)
				if t.Index != "" || t.ID != "" {
					v.add(p, "id and index apply only to attribute terms")
				}
			case t.Via != "":
				v.add(p+".via", `via must be "interactions", got %q`, t.Via)
			default:
				v.add(p, "a term needs on (an attribute) or via: interactions")
			}
		}
	}
}

// crossSimilarity checks similarity.cross: terms comparing two entity types through an
// attribute both have with the same vocab.
func (v *validator) crossSimilarity() {
	for i, t := range v.s.Similarity["cross"] {
		p := fmt.Sprintf("similarity.cross[%d]", i)
		if len(t.Between) != 2 || t.Between[0] == t.Between[1] {
			v.add(p+".between", "between names two different entity types")
			continue
		}
		if !t.Metric.Valid() {
			v.add(p+".metric", "metric must be one of jaccard, cosine, equals, log_ratio")
		}
		if t.Weight <= 0 || math.IsNaN(t.Weight) {
			v.add(p+".weight", "weight must be greater than 0")
		}
		if t.Via != "" {
			v.add(p+".via", "a cross term compares attributes; via does not apply")
		}
		if len(t.On) != 1 {
			v.add(p+".on", "a cross term compares one attribute")
			continue
		}
		var vocab []string
		for _, et := range t.Between {
			e, ok := v.s.Entities[et]
			if !ok {
				v.add(p+".between", "%q is not a declared entity", et)
				continue
			}
			a, ok := e.Attributes[t.On[0]]
			if !ok {
				v.add(p+".on", "%q is not an attribute of %s", t.On[0], et)
				continue
			}
			vocab = append(vocab, a.Vocab)
		}
		if len(vocab) == 2 && (vocab[0] == "" || vocab[0] != vocab[1]) {
			v.add(p+".on", "%q must have the same vocab on %s and %s to be comparable", t.On[0], t.Between[0], t.Between[1])
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

// attrIn checks that attr exists on every entity in ents and returns one spec per entity
// that has it.
func (v *validator) attrIn(p string, ents []string, attr string) ([]AttributeSpec, bool) {
	var out []AttributeSpec
	ok := len(ents) > 0
	for _, et := range ents {
		e, declared := v.s.Entities[et]
		if !declared {
			ok = false
			continue
		}
		a, has := e.Attributes[attr]
		if !has {
			v.add(p, "%q is not an attribute of %s", attr, et)
			ok = false
			continue
		}
		out = append(out, a)
	}
	return out, ok
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
		if allowed, known := signalKeys[sg.Type]; known {
			for _, key := range slices.Sorted(mapsKeys(sg.Params)) {
				if !slices.Contains(allowed, key) {
					if len(allowed) == 0 {
						v.add(p+"."+key, "unknown key for a %s signal (it takes only the common keys)", sg.Type)
					} else {
						v.add(p+"."+key, "unknown key for a %s signal (use %s)", sg.Type, strings.Join(allowed, ", "))
					}
				}
			}
		}
		v.signalCommon(p, sg)
		v.signalParams(p, sg)
	}
}

// signalEntities are the entities a signal scores: its `for`, or every recommendable entity.
func (v *validator) signalEntities(sg SignalSpec) []string {
	if len(sg.For) > 0 {
		var out []string
		for _, e := range sg.For {
			if _, ok := v.s.Entities[e]; ok {
				out = append(out, e)
			}
		}
		return out
	}
	return v.rankedN
}

func (v *validator) signalCommon(p string, sg SignalSpec) {
	for _, e := range sg.For {
		if !slices.Contains(v.scoredBy, e) {
			v.add(p+".for", "%q is not recommended by anything (list it in recommendable or a recommender's for)", e)
		}
	}
	v.targetedEntities(p+".from", sg.From)
	if sg.Normalise != "" && !slices.Contains(Normalisations, sg.Normalise) {
		v.add(p+".normalise", "normalise must be one of %s", strings.Join(Normalisations, ", "))
	}
	if sg.Transform != "" {
		v.transform(p+".transform", Transform(sg.Transform))
	}
	if sg.Cap != nil && (*sg.Cap <= 0 || *sg.Cap > 1) {
		v.add(p+".cap", "cap must be in (0, 1]")
	}
	v.text(p+".label", sg.Label, false)
	v.text(p+".explain", sg.Explain, false)
}

// targetedEntities checks a `from` list: each entity must be the target of some interaction.
func (v *validator) targetedEntities(p string, ents StringList) {
	for _, e := range ents {
		if _, ok := v.s.Entities[e]; !ok {
			v.add(p, "%q is not a declared entity", e)
			continue
		}
		targeted := false
		for _, name := range v.s.InteractionTypes() {
			t := v.s.Interactions[name].Target
			if t == e || t == "" && e == v.ranked {
				targeted = true
				break
			}
		}
		if !targeted {
			v.add(p, "no interaction targets %s, so there is no history to learn from", e)
		}
	}
}

// interactionNames checks a list of interaction names; check returns a problem with a
// declared interaction, or "".
func (v *validator) interactionNames(p string, names []string, check func(string, InteractionSpec) string) {
	for i, name := range names {
		ip := fmt.Sprintf("%s[%d]", p, i)
		spec, ok := v.s.Interactions[name]
		if !ok {
			v.add(ip, "%q is not a declared interaction", name)
			continue
		}
		if check != nil {
			if msg := check(name, spec); msg != "" {
				v.add(ip, "%q: %s", name, msg)
			}
		}
	}
}

// listParam reads a parameter that is a name or a list of names.
func listParam(raw any) ([]string, bool) {
	switch x := raw.(type) {
	case string:
		return []string{x}, true
	case []any:
		out := make([]string, 0, len(x))
		for _, item := range x {
			s, ok := item.(string)
			if !ok {
				return nil, false
			}
			out = append(out, s)
		}
		return out, true
	}
	return nil, false
}

func (v *validator) durationParam(p string, params map[string]any, key string, required bool) (Duration, bool) {
	raw, ok := params[key]
	if !ok {
		if required {
			v.add(p+"."+key, "%s is required (a duration such as 3d, 12h, 2w)", key)
		}
		return 0, false
	}
	d, err := ParseDuration(fmt.Sprint(raw))
	if err != nil || d <= 0 {
		v.add(p+"."+key, "use a positive duration such as 3d, 12h, 2w")
		return 0, false
	}
	return d, true
}

func (v *validator) userRefs(p, text string) {
	user := v.s.Entities["user"]
	for _, m := range userRefRe.FindAllStringSubmatch(text, -1) {
		if _, ok := user.Attributes[m[1]]; !ok {
			v.add(p, "$user.%s: %q is not an attribute of user", m[1], m[1])
		}
	}
	for _, m := range ctxRefRe.FindAllStringSubmatch(text, -1) {
		if _, ok := v.s.Context[m[1]]; !ok {
			v.add(p, "$context.%s: %q is not a declared context field", m[1], m[1])
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
		if k.Label.IsZero() {
			v.add(p+".label", "label is required (plain language, shown to users)")
		} else {
			v.text(p+".label", k.Label, true)
		}
		for name, t := range map[string]Text{"low": k.Low, "high": k.High, "group": k.Group, "help": k.Help} {
			v.text(p+"."+name, t, false)
		}
		v.knobKind(p, k)
		r := k.EffectiveRange()
		if !(r[0] < r[1]) {
			v.add(p+".range", "range must be [min, max] with min < max")
		}
		if k.Default != nil {
			d := *k.Default
			switch {
			case d < r[0] || d > r[1]:
				v.add(p+".default", "%v is outside the knob range [%v, %v]", d, r[0], r[1])
			case k.EffectiveKind() == KnobChoice && !slices.ContainsFunc(k.Options, func(o KnobOption) bool { return o.Value == d }):
				v.add(p+".default", "%v is not one of the option values", d)
			}
		}
		for _, r := range k.Scope {
			if !v.recommenderExists(r) {
				v.add(p+".scope", "%q is not a declared recommender", r)
			}
		}
		if k.DependsOn != "" {
			if _, ok := v.s.Knob(k.DependsOn); !ok || k.DependsOn == k.ID {
				v.add(p+".depends_on", "%q must name another declared knob", k.DependsOn)
			}
		}
		if len(k.Maps) == 0 {
			v.add(p+".maps", "map the knob onto at least one signal")
		}
		for _, target := range slices.Sorted(mapsKeys(k.Maps)) {
			tp := p + ".maps." + target
			v.knobTarget(tp, target)
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

func (v *validator) knobKind(p string, k KnobSpec) {
	switch k.EffectiveKind() {
	case KnobSlider:
		if len(k.Options) > 0 {
			v.add(p+".options", "options belong to a choice knob")
		}
	case KnobToggle:
		if k.Range != [2]float64{} && k.Range != [2]float64{0, 1} {
			v.add(p+".range", "a toggle is 0 or 1; leave out range or use [0, 1]")
		}
		if len(k.Options) > 0 {
			v.add(p+".options", "options belong to a choice knob")
		}
	case KnobChoice:
		if len(k.Options) < 2 {
			v.add(p+".options", "a choice needs at least two options")
		}
		values := map[float64]bool{}
		for i, o := range k.Options {
			op := fmt.Sprintf("%s.options[%d]", p, i)
			if values[o.Value] {
				v.add(op+".value", "duplicate option value %v", o.Value)
			}
			values[o.Value] = true
			if o.Label.IsZero() {
				v.add(op+".label", "every option needs a label")
			} else {
				v.text(op+".label", o.Label, true)
			}
			if k.Range != [2]float64{} && (o.Value < k.Range[0] || o.Value > k.Range[1]) {
				v.add(op+".value", "%v is outside the knob range", o.Value)
			}
		}
	default:
		v.add(p+".kind", "kind must be slider, toggle or choice")
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
			r := k.EffectiveRange()
			val := v.s.Presets[name][kid]
			switch {
			case val < r[0] || val > r[1]:
				v.add(p, "%v is outside the knob range [%v, %v]", val, r[0], r[1])
			case k.EffectiveKind() == KnobChoice && !slices.ContainsFunc(k.Options, func(o KnobOption) bool { return o.Value == val }):
				v.add(p, "%v is not one of the option values of %s", val, kid)
			}
		}
	}
}

func (v *validator) constraints() {
	ids := map[string]bool{}
	for i, c := range v.s.Constraints {
		p := fmt.Sprintf("constraints[%d]", i)
		if c.ID != "" {
			v.ident(p+".id", c.ID)
			if ids[c.ID] {
				v.add(p+".id", "duplicate constraint id %q", c.ID)
			}
			ids[c.ID] = true
		}
		for _, e := range c.For {
			if !slices.Contains(v.scoredBy, e) {
				v.add(p+".for", "%q is not recommended by anything", e)
			}
		}
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
		ents := v.rankedN
		if len(c.For) > 0 {
			ents = c.For
		}
		v.condition(p, cond, ents)
	}
}

func (v *validator) condition(p string, c *Condition, ents []string) {
	hasAttr, hasInter := c.Attribute != "", len(c.Interacted) > 0
	if c.InSeed {
		if hasAttr || hasInter {
			v.add(p+".in_seed", "in_seed stands alone; put other conditions in their own constraint")
		}
		return
	}
	if hasAttr == hasInter {
		v.add(p, "a condition needs either attribute or interacted")
		return
	}
	if hasAttr {
		if len(ents) == 1 && ents[0] == v.ranked {
			v.rankedAttr(p+".attribute", c.Attribute)
		} else {
			v.attrIn(p+".attribute", ents, c.Attribute)
		}
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
