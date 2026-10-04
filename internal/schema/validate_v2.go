package schema

import (
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/timurcravtov/walrus/internal/schema/expr"
)

// Validation of the schema v2 sections.

func (v *validator) defaultLocale() string {
	if m := v.s.Meta; m != nil && m.DefaultLocale != "" {
		return m.DefaultLocale
	}
	if m := v.s.Meta; m != nil && len(m.Locales) > 0 {
		return m.Locales[0]
	}
	return "en"
}

func (v *validator) meta() {
	m := v.s.Meta
	if m == nil {
		return
	}
	for i, l := range m.Locales {
		if !localeRe.MatchString(l) {
			v.add(fmt.Sprintf("meta.locales[%d]", i), "%q is not a locale such as en or pt-BR", l)
		}
	}
	if m.DefaultLocale != "" && len(m.Locales) > 0 && !slices.Contains(m.Locales, m.DefaultLocale) {
		v.add("meta.default_locale", "%q is not in meta.locales", m.DefaultLocale)
	}
}

// text checks a user-facing string: a locale map must have the default locale, and every key
// must be a locale.
func (v *validator) text(p string, t Text, required bool) {
	if t.IsZero() {
		if required {
			v.add(p, "text is required")
		}
		return
	}
	if t.Locales == nil {
		return
	}
	for _, l := range slices.Sorted(mapsKeys(t.Locales)) {
		if !localeRe.MatchString(l) {
			v.add(p, "%q is not a locale such as en or pt-BR", l)
		}
	}
	if strings.TrimSpace(t.Locales[v.defaultLocale()]) == "" {
		v.add(p, "missing the default locale %q", v.defaultLocale())
	}
}

var contextTypes = []AttrType{TypeCategorical, TypeString, TypeInt, TypeFloat, TypeBool, TypeTimestamp, TypeSet, TypeGeo}

func (v *validator) context() {
	for _, name := range sortedKeys(v.s.Context) {
		f := v.s.Context[name]
		p := "context." + name
		v.ident(p, name)
		if !slices.Contains(contextTypes, f.Type) {
			v.add(p+".type", "a context field is categorical, string, int, float, bool, timestamp, set or geo, got %q", f.Type)
		}
		if f.Type == TypeSet && f.Of != "" && f.Of != "string" {
			v.add(p+".of", `set elements must be "string", got %q`, f.Of)
		}
		if len(f.Values) > 0 && f.Type != TypeCategorical {
			v.add(p+".values", "values apply only to categorical fields")
		}
		if f.Range != nil && (f.Type != TypeInt && f.Type != TypeFloat || !(f.Range[0] < f.Range[1])) {
			v.add(p+".range", "range applies to int and float fields, as [min, max] with min < max")
		}
		if f.Derive != "" {
			if !slices.Contains(ContextDerivations, f.Derive) {
				v.add(p+".derive", "derive must be one of %s", strings.Join(ContextDerivations, ", "))
			}
			if f.Type != TypeInt {
				v.add(p+".type", "a derived field is an int")
			}
		}
	}
}

// Names an expression may use, by caller.
const (
	refItem = 1 << iota
	refUser
	refContext
	refSeed    // seed.size, seed.mean.<attr>, seed.median.<attr>
	refProfile // profile.size
	refAge     // age: seconds since lifecycle.created
)

// exprRefs compiles src and checks every name it uses against what the caller allows.
func (v *validator) exprRefs(p, src string, ents []string, allowed int) {
	x, err := expr.Compile(src)
	if err != nil {
		v.add(p, "expression %q: %v", src, err)
		return
	}
	for _, name := range x.Vars() {
		ns, rest, _ := strings.Cut(name, ".")
		switch {
		case ns == "$item" && allowed&refItem != 0:
			found := false
			for _, e := range ents {
				if _, ok := v.s.Entities[e].Attributes[rest]; ok {
					found = true
				}
			}
			if !found {
				v.add(p, "$item.%s: %q is not an attribute of %s", rest, rest, strings.Join(ents, " or "))
			}
		case ns == "$user" && allowed&refUser != 0:
			if _, ok := v.s.Entities["user"].Attributes[rest]; !ok {
				v.add(p, "$user.%s: %q is not an attribute of user", rest, rest)
			}
		case ns == "$context" && allowed&refContext != 0:
			if _, ok := v.s.Context[rest]; !ok {
				v.add(p, "$context.%s: %q is not a declared context field", rest, rest)
			}
		case ns == "seed" && allowed&refSeed != 0:
			stat, attr, _ := strings.Cut(rest, ".")
			switch {
			case rest == "size":
			case (stat == "mean" || stat == "median") && attr != "":
				found := false
				for _, e := range ents {
					if a, ok := v.s.Entities[e].Attributes[attr]; ok && (a.Type == TypeFloat || a.Type == TypeInt) {
						found = true
					}
				}
				if !found {
					v.add(p, "seed.%s.%s: %q is not a numeric attribute of %s", stat, attr, attr, strings.Join(ents, " or "))
				}
			default:
				v.add(p, "%s: use seed.size, seed.mean.<attribute> or seed.median.<attribute>", name)
			}
		case name == "profile.size" && allowed&refProfile != 0:
		case name == "age" && allowed&refAge != 0:
		default:
			v.add(p, "%q cannot be used here (%s)", name, describeRefs(allowed))
		}
	}
}

func describeRefs(allowed int) string {
	var out []string
	for _, r := range []struct {
		bit  int
		text string
	}{
		{refItem, "$item.<attribute>"}, {refUser, "$user.<attribute>"}, {refContext, "$context.<field>"},
		{refSeed, "seed.size, seed.mean.<attribute>"}, {refProfile, "profile.size"}, {refAge, "age"},
	} {
		if allowed&r.bit != 0 {
			out = append(out, r.text)
		}
	}
	return "use " + strings.Join(out, ", ")
}

func (v *validator) rules() {
	ids := map[string]bool{}
	for i, r := range v.s.Rules {
		p := fmt.Sprintf("rules[%d]", i)
		v.ident(p+".id", r.ID)
		if ids[r.ID] {
			v.add(p+".id", "duplicate rule id %q", r.ID)
		}
		ids[r.ID] = true
		ents := v.rankedN
		if len(r.For) > 0 {
			ents = r.For
			for _, e := range r.For {
				if !slices.Contains(v.scoredBy, e) {
					v.add(p+".for", "%q is not recommended by anything", e)
				}
			}
		}
		if r.When != "" {
			v.exprRefs(p+".when", r.When, ents, refItem|refUser|refContext|refAge)
		}
		actions := 0
		for _, set := range []bool{r.Place != nil, r.Boost != nil, r.Bury, r.Quota != nil, r.Cap != nil, r.Pin != nil} {
			if set {
				actions++
			}
		}
		if actions != 1 {
			v.add(p, "a rule needs exactly one of place, boost, bury, quota, cap, pin")
			continue
		}
		switch {
		case r.Place != nil:
			if len(r.Place.At) == 0 {
				v.add(p+".place.at", "list the positions, such as [3, 9]")
			}
			for j, at := range r.Place.At {
				if at < 1 || j > 0 && at <= r.Place.At[j-1] {
					v.add(p+".place.at", "positions start at 1 and go up")
					break
				}
			}
		case r.Boost != nil:
			if *r.Boost <= 0 || math.IsNaN(*r.Boost) {
				v.add(p+".boost", "boost must be greater than 0 (below 1 lowers, above 1 raises)")
			}
		case r.Quota != nil:
			q := r.Quota
			switch {
			case q.Attribute != "" && q.When == "":
				v.attrIn(p+".quota.attribute", ents, q.Attribute)
				if q.Max < 1 || q.Per < q.Max {
					v.add(p+".quota", "a cap quota needs max ≥ 1 and per ≥ max")
				}
				if q.MinShare != 0 || q.Top != 0 {
					v.add(p+".quota", "min_share and top belong to a share quota (with when)")
				}
			case q.When != "" && q.Attribute == "":
				v.exprRefs(p+".quota.when", q.When, ents, refItem|refUser|refContext|refAge)
				if q.MinShare <= 0 || q.MinShare > 1 || q.Top < 1 {
					v.add(p+".quota", "a share quota needs min_share in (0, 1] and top ≥ 1")
				}
				if q.Max != 0 || q.Per != 0 {
					v.add(p+".quota", "max and per belong to a cap quota (with attribute)")
				}
			default:
				v.add(p+".quota", "a quota is { attribute, max, per } or { when, min_share, top }")
			}
		case r.Cap != nil:
			spec, ok := v.s.Interactions[r.Cap.Exposure]
			if !ok || spec.EffectiveKind() != KindExposure {
				v.add(p+".cap.exposure", "%q must be an interaction with kind: exposure", r.Cap.Exposure)
			}
			if r.Cap.Max < 1 || r.Cap.Within <= 0 {
				v.add(p+".cap", "a cap needs max ≥ 1 and a positive within")
			}
		case r.Pin != nil:
			field, ok := strings.CutPrefix(r.Pin.IDs, "$context.")
			if f, declared := v.s.Context[field]; !ok || !declared || f.Type != TypeSet {
				v.add(p+".pin.ids", "ids must be $context.<a set field> listing item ids")
			}
			if r.Pin.At < 1 {
				v.add(p+".pin.at", "at starts at 1")
			}
		}
	}
}

// knobTarget checks a knob map target against the T3 vocabulary.
func (v *validator) knobTarget(p, target string) {
	if _, ok := v.s.Signals[target]; ok || slices.Contains(MetaTargets, target) {
		return
	}
	parts := strings.Split(target, ".")
	bad := func(format string, args ...any) { v.add(p, format, args...) }
	switch {
	case len(parts) == 3 && parts[0] == "signals" && parts[2] == "weight":
		if _, ok := v.s.Signals[parts[1]]; !ok {
			bad("%q is not a declared signal", parts[1])
		}
		return
	case len(parts) == 4 && parts[0] == "signals" && parts[2] == "from":
		sg, ok := v.s.Signals[parts[1]]
		switch {
		case !ok:
			bad("%q is not a declared signal", parts[1])
		case !slices.Contains(sg.From, parts[3]):
			bad("%s does not learn from %s (add it to the signal's from)", parts[1], parts[3])
		}
		return
	case len(parts) == 4 && parts[0] == "similarity" && parts[3] == "weight":
		for _, t := range v.s.Similarity[parts[1]] {
			if len(t.On) > 0 && t.TermID() == parts[2] {
				if t.Locked {
					bad("similarity term %s.%s is locked; a knob cannot scale it", parts[1], parts[2])
				}
				return
			}
		}
		bad("%q is not a similarity term of %s", parts[2], parts[1])
		return
	case len(parts) == 3 && parts[0] == "interactions" && (parts[2] == "weight_scale" || parts[2] == "half_life_scale"):
		i, ok := v.s.Interactions[parts[1]]
		switch {
		case !ok:
			bad("%q is not a declared interaction", parts[1])
		case parts[2] == "half_life_scale" && i.HalfLife == 0:
			bad("%s has no half_life to scale", parts[1])
		}
		return
	case len(parts) == 4 && parts[0] == "attribute" && parts[3] == "target":
		e, ok := v.s.Entities[parts[1]]
		if !ok {
			bad("%q is not a declared entity", parts[1])
			return
		}
		a, ok := e.Attributes[parts[2]]
		if !ok || a.Type != TypeFloat && a.Type != TypeInt || a.Range == nil {
			bad("%s.%s must be a numeric attribute with a range", parts[1], parts[2])
		}
		return
	case len(parts) == 3 && parts[0] == "rules" && parts[2] == "strength":
		for _, r := range v.s.Rules {
			if r.ID == parts[1] {
				if r.Boost == nil && !r.Bury && r.Quota == nil {
					bad("rule %s has no boost, bury or quota to scale", parts[1])
				}
				return
			}
		}
		bad("%q is not a declared rule", parts[1])
		return
	case len(parts) >= 3 && parts[0] == "recommenders":
		r, ok := v.s.Recommenders[parts[1]]
		if !ok {
			bad("%q is not a declared recommender", parts[1])
			return
		}
		seed := r.EffectiveSeed()
		switch {
		case len(parts) == 3 && parts[2] == "diversity":
			if r.Rerank == nil || r.Rerank.Diversity == nil {
				bad("recommender %s has no rerank.diversity to tune", parts[1])
			}
		case len(parts) == 3 && parts[2] == "blend_user":
			if seed != SeedItem && seed != SeedItems && seed != SeedSession {
				bad("blend_user applies to item, items and session seeds, %s has seed %s", parts[1], seed)
			}
		case len(parts) == 3 && parts[2] == "seed_aggregate":
			if seed != SeedItems && seed != SeedSession {
				bad("seed_aggregate applies to items and session seeds, %s has seed %s", parts[1], seed)
			}
		case len(parts) == 4 && parts[2] == "mix":
			if r.Mix == nil || r.Mix.By != "entity" || !slices.Contains(r.For, parts[3]) {
				bad("recommender %s does not mix %s by entity", parts[1], parts[3])
			}
		default:
			bad("%q is not a tunable recommender value (diversity, blend_user, seed_aggregate, mix.<entity>)", target)
		}
		return
	}
	if tier := PathTier(target); tier != TierT3 && tier != TierUnknown {
		bad("%q is %s; a knob can only change scoring-time (T3) values", target, tier)
		return
	}
	bad("%q is neither a declared signal nor a known meta-parameter", target)
}

func (v *validator) recommenderExists(name string) bool {
	if len(v.s.Recommenders) == 0 {
		return name == "default"
	}
	_, ok := v.s.Recommenders[name]
	return ok
}

var (
	candidateSignalType = map[string]string{
		"trend": "trend", "co_occurrence": "co_occurrence", "sequence": "sequence", "mutuals": "mutual_connections",
		"factors": "embedding",
	}
	itemSeeds = []string{SeedItem, SeedItems, SeedSession}
)

func (v *validator) recommenders() {
	constraintIDs := map[string]bool{}
	constraintByID := map[string]Constraint{}
	for _, c := range v.s.Constraints {
		if c.ID != "" {
			constraintIDs[c.ID] = true
			constraintByID[c.ID] = c
		}
	}
	ruleIDs := map[string]Rule{}
	for _, r := range v.s.Rules {
		ruleIDs[r.ID] = r
	}
	for _, name := range sortedKeys(v.s.Recommenders) {
		r := v.s.Recommenders[name]
		p := "recommenders." + name
		seed := r.EffectiveSeed()
		v.ident(p, name)
		v.text(p+".label", r.Label, false)
		if len(r.For) == 0 {
			v.add(p+".for", "for is required: the entity types it returns")
		}
		for _, e := range r.For {
			if _, ok := v.s.Entities[e]; !ok {
				v.add(p+".for", "%q is not a declared entity", e)
			}
		}
		v.recommenderSeed(p, name, r, seed)
		v.recommenderCandidates(p, name, r, seed)
		v.recommenderSignals(p, name, r, seed)
		v.recommenderWeights(p, name, r, seed)
		for _, cid := range r.Constraints {
			if !constraintIDs[cid] {
				v.add(p+".constraints", "%q is not the id of a constraint", cid)
			}
			if c, ok := constraintByID[cid]; ok && c.usesSeed() && !slices.Contains(itemSeeds, seed) {
				v.add(p+".constraints", "constraint %s refers to $seed, which needs an item, items or session seed", cid)
			}
		}
		v.recommenderRules(p, r, ruleIDs)
		v.recommenderShape(p, name, r, seed)
		v.recommenderFallbacks(p, name, r, seed)
	}
	v.fallbackCycles()
}

// recommenderSeed checks the seed type and the keys that only some seeds take.
func (v *validator) recommenderSeed(p, name string, r RecommenderSpec, seed string) {
	if r.Seed != "" && !slices.Contains(Seeds, r.Seed) {
		v.add(p+".seed", "seed must be one of %s", strings.Join(Seeds, ", "))
	}
	if r.SeedAggregate != "" {
		if seed != SeedItems && seed != SeedSession {
			v.add(p+".seed_aggregate", "seed_aggregate applies to items and session seeds")
		} else if r.SeedAggregate != "mean" && r.SeedAggregate != "max" {
			v.add(p+".seed_aggregate", "seed_aggregate must be mean or max")
		}
	}
	if r.BlendUser != nil {
		if !slices.Contains(itemSeeds, seed) {
			v.add(p+".blend_user", "blend_user applies to item, items and session seeds")
		} else if *r.BlendUser < 0 || *r.BlendUser > 1 {
			v.add(p+".blend_user", "blend_user must be in [0, 1]")
		}
	}
}

// recommenderCandidates checks each candidate source, and that the seed can feed it.
func (v *validator) recommenderCandidates(p, name string, r RecommenderSpec, seed string) {
	for i, c := range r.Candidates {
		cp := fmt.Sprintf("%s.candidates[%d]", p, i)
		if !slices.Contains(CandidateSources, c.Source) {
			v.add(cp+".source", "source must be one of %s", strings.Join(CandidateSources, ", "))
			continue
		}
		if want, needs := candidateSignalType[c.Source]; needs {
			sg, ok := v.s.Signals[c.Signal]
			if !ok || sg.Type != want {
				v.add(cp+".signal", "source %s needs signal: <a %s signal>", c.Source, want)
			}
		} else if c.Signal != "" {
			v.add(cp+".signal", "source %s takes no signal", c.Source)
		}
		if c.Cap < 0 {
			v.add(cp+".cap", "cap must not be negative")
		}
		switch c.Source {
		case "co_occurrence":
			if seed == SeedNone {
				v.add(cp+".source", "co_occurrence needs something to start from: a user (their recent items), an item, items or a session")
			}
		case "sequence":
			if !slices.Contains(itemSeeds, seed) {
				v.add(cp+".source", "sequence needs an item, items or session seed")
			}
		case "user_neighbors":
			if seed != SeedUser && seed != SeedUsers {
				v.add(cp+".source", "user_neighbors needs a user or users seed")
			}
		case "item_neighbors":
			if seed == SeedNone {
				v.add(cp+".source", "item_neighbors needs something to be similar to; seed none has nothing")
			}
		case "factors":
			if seed == SeedNone {
				v.add(cp+".source", "factors needs something to fit: a user (their history), an item, items or a session")
			}
		}
	}
}

// recommenderSignals checks that each signal exists, scores what the recommender returns, and fits
// its seed.
func (v *validator) recommenderSignals(p, name string, r RecommenderSpec, seed string) {
	for _, sid := range r.Signals {
		sg, ok := v.s.Signals[sid]
		if !ok {
			v.add(p+".signals", "%q is not a declared signal", sid)
			continue
		}
		ents := v.signalEntities(sg)
		if len(sg.For) > 0 && !slices.ContainsFunc(r.For, func(e string) bool { return slices.Contains(ents, e) }) {
			v.add(p+".signals", "%s scores %s, none of which %s returns", sid, strings.Join(sg.For, ", "), name)
		}
		switch sg.Type {
		case "co_occurrence":
			// With a user seed it starts from the user's own recent items: bought after.
			if seed == SeedNone {
				v.add(p+".signals", "%s (co_occurrence) needs something to start from; seed none has nothing", sid)
			}
		case "sequence":
			if !slices.Contains(itemSeeds, seed) {
				v.add(p+".signals", "%s (sequence) needs an item, items or session seed", sid)
			}
		case "mutual_connections":
			if seed != SeedUser || !slices.Contains(r.For, "user") {
				v.add(p+".signals", "%s (mutual_connections) recommends users to a user: for: [user], seed: user", sid)
			}
		case "attribute_target":
			if t, _ := sg.Params["target"].(string); strings.HasPrefix(t, "seed.") && seed != SeedItems && seed != SeedSession {
				v.add(p+".signals", "%s aims at %s, which needs an items or session seed", sid, t)
			}
		}
	}
}

// recommenderWeights checks the recommender's own weights: signals it uses, expressions over what
// its seed allows.
func (v *validator) recommenderWeights(p, name string, r RecommenderSpec, seed string) {
	used := r.Signals
	if len(used) == 0 {
		used = v.s.SignalIDs()
	}
	for _, sid := range sortedKeys(r.Weights) {
		wp := p + ".weights." + sid
		if !slices.Contains(used, sid) {
			v.add(wp, "%q is not a signal this recommender uses", sid)
		}
		w := r.Weights[sid]
		if _, isConst := w.Constant(); !isConst {
			allowed := refUser | refContext | refProfile
			if slices.Contains(itemSeeds, seed) {
				allowed |= refSeed
			}
			v.exprRefs(wp, string(w), r.For, allowed)
		}
	}
}

// recommenderRules checks that each rule exists and fits the recommender's limit.
func (v *validator) recommenderRules(p string, r RecommenderSpec, ruleIDs map[string]Rule) {
	maxLimit := 100
	if r.Limit != nil {
		if r.Limit.Default < 1 || r.Limit.Max < r.Limit.Default {
			v.add(p+".limit", "limit needs default ≥ 1 and max ≥ default")
		}
		maxLimit = r.Limit.Max
	}
	for _, rid := range r.Rules {
		rule, ok := ruleIDs[rid]
		if !ok {
			v.add(p+".rules", "%q is not a declared rule", rid)
			continue
		}
		if rule.Place != nil && len(rule.Place.At) > 0 && rule.Place.At[len(rule.Place.At)-1] > maxLimit {
			v.add(p+".rules", "rule %s places items at %d, past this recommender's limit of %d", rid, rule.Place.At[len(rule.Place.At)-1], maxLimit)
		}
	}
}

// recommenderShape checks re-ranking, mixing, knobs, groups and reciprocal matching.
func (v *validator) recommenderShape(p, name string, r RecommenderSpec, seed string) {
	if rr := r.Rerank; rr != nil && rr.Diversity != nil {
		v.attrIn(p+".rerank.diversity.on", r.For, rr.Diversity.On)
		if rr.Diversity.Lambda < 0 || rr.Diversity.Lambda > 1 {
			v.add(p+".rerank.diversity.lambda", "lambda must be in [0, 1]")
		}
	}
	if m := r.Mix; m != nil {
		switch {
		case len(r.For) < 2:
			v.add(p+".mix", "mix applies when a recommender returns several entity types")
		case m.By == "score":
			if len(m.Shares) > 0 {
				v.add(p+".mix.shares", "shares apply to by: entity")
			}
		case m.By == "entity":
			sum := 0.0
			for _, e := range sortedKeys(m.Shares) {
				if !slices.Contains(r.For, e) {
					v.add(p+".mix.shares", "%q is not in for", e)
				}
				if m.Shares[e] <= 0 || m.Shares[e] > 1 {
					v.add(p+".mix.shares."+e, "a share is in (0, 1]")
				}
				sum += m.Shares[e]
			}
			if len(m.Shares) != len(r.For) || math.Abs(sum-1) > 1e-9 {
				v.add(p+".mix.shares", "give every entity in for a share, summing to 1")
			}
		default:
			v.add(p+".mix.by", "by must be entity or score")
		}
	}
	for _, kid := range r.Knobs {
		if _, ok := v.s.Knob(kid); !ok {
			v.add(p+".knobs", "%q is not a declared knob", kid)
		}
	}
	if g := r.Group; g != nil {
		if seed != SeedUsers {
			v.add(p+".group", "group applies to seed: users")
		}
		if !slices.Contains([]string{"average", "least_misery", "most_pleasure"}, g.Aggregate) {
			v.add(p+".group.aggregate", "aggregate must be average, least_misery or most_pleasure")
		}
	}
	if rc := r.Reciprocal; rc != nil {
		if _, ok := v.s.Recommenders[rc.Recommender]; !ok || rc.Recommender == name {
			v.add(p+".reciprocal.recommender", "%q must name another declared recommender", rc.Recommender)
		}
		if !slices.Contains([]string{"harmonic", "min", "product"}, rc.Combine) {
			v.add(p+".reciprocal.combine", "combine must be harmonic, min or product")
		}
	}
}

// recommenderFallbacks checks each fallback: a condition over what the seed allows, and another
// recommender that returns the same kind of thing.
func (v *validator) recommenderFallbacks(p, name string, r RecommenderSpec, seed string) {
	for i, f := range r.Fallback {
		fp := fmt.Sprintf("%s.fallback[%d]", p, i)
		allowed := refUser | refContext | refProfile
		if slices.Contains(itemSeeds, seed) {
			allowed |= refSeed
		}
		if f.When == "" {
			v.add(fp+".when", "when is required")
		} else {
			v.exprRefs(fp+".when", f.When, r.For, allowed)
		}
		other, ok := v.s.Recommenders[f.Use]
		switch {
		case !ok:
			v.add(fp+".use", "%q is not a declared recommender", f.Use)
		case f.Use == name:
			v.add(fp+".use", "a recommender cannot fall back to itself")
		case !slices.ContainsFunc(other.For, func(e string) bool { return slices.Contains(r.For, e) }):
			v.add(fp+".use", "%s returns %s, nothing %s returns", f.Use, strings.Join(other.For, ", "), name)
		}
	}
}

// fallbackCycles reports a chain of fallbacks that comes back to where it started.
func (v *validator) fallbackCycles() {
	state := map[string]int{}
	var visit func(name string, path []string) bool
	visit = func(name string, path []string) bool {
		switch state[name] {
		case 1:
			v.add("recommenders."+path[0]+".fallback", "fallbacks form a cycle: %s", strings.Join(append(path, name), " -> "))
			return true
		case 2:
			return false
		}
		state[name] = 1
		for _, f := range v.s.Recommenders[name].Fallback {
			if _, ok := v.s.Recommenders[f.Use]; ok && f.Use != name && visit(f.Use, append(path, name)) {
				return true
			}
		}
		state[name] = 2
		return false
	}
	for _, name := range sortedKeys(v.s.Recommenders) {
		if state[name] == 0 && visit(name, nil) {
			return
		}
	}
}

func (v *validator) exposureKind(name string) bool {
	spec, ok := v.s.Interactions[name]
	return ok && spec.EffectiveKind() == KindExposure
}

func (v *validator) metrics() {
	for _, name := range sortedKeys(v.s.Metrics) {
		m := v.s.Metrics[name]
		p := "metrics." + name
		v.ident(p, name)
		forms := 0
		for _, set := range []bool{len(m.Ratio) > 0, m.Mean != "", m.ReturningUsers != 0, m.List != "", m.System != ""} {
			if set {
				forms++
			}
		}
		if forms != 1 {
			v.add(p, "a metric needs exactly one of ratio, mean, returning_users, list, system")
			continue
		}
		switch {
		case len(m.Ratio) > 0:
			if len(m.Ratio) != 2 {
				v.add(p+".ratio", "ratio is [numerator, denominator]")
				break
			}
			if _, ok := v.s.Interactions[m.Ratio[0]]; !ok {
				v.add(p+".ratio", "%q is not a declared interaction", m.Ratio[0])
			}
			if !v.exposureKind(m.Ratio[1]) {
				v.add(p+".ratio", "the denominator %q must be an interaction with kind: exposure", m.Ratio[1])
			}
		case m.Mean != "":
			it, field, _ := strings.Cut(m.Mean, ".")
			spec, ok := v.s.Interactions[it]
			if !ok || field != "value" || spec.Value == "" {
				v.add(p+".mean", "mean is <interaction>.value, for an interaction that carries a value")
			}
			if !v.exposureKind(m.Per) {
				v.add(p+".per", "per must be an interaction with kind: exposure")
			}
		case m.List != "":
			if !slices.Contains(ListMetrics, m.List) {
				v.add(p+".list", "list must be one of %s", strings.Join(ListMetrics, ", "))
			}
		case m.System != "":
			if m.System != "latency" {
				v.add(p+".system", "system must be latency")
			}
			if m.Quantile <= 0 || m.Quantile >= 1 {
				v.add(p+".quantile", "quantile must be in (0, 1)")
			}
		}
		if m.Attribution != 0 && len(m.Ratio) == 0 && m.Mean == "" {
			v.add(p+".attribution", "attribution applies to ratio and mean metrics")
		}
	}
}

func (v *validator) experiments() {
	traffic := map[string]float64{}
	for _, name := range sortedKeys(v.s.Experiments) {
		x := v.s.Experiments[name]
		p := "experiments." + name
		v.ident(p, name)
		if !v.recommenderExists(x.Recommender) {
			v.add(p+".recommender", "%q is not a declared recommender", x.Recommender)
		}
		if !slices.Contains(ExperimentStatuses, x.Status) {
			v.add(p+".status", "status must be one of %s", strings.Join(ExperimentStatuses, ", "))
		}
		if x.Unit != "" && !slices.Contains(ExperimentUnits, x.Unit) {
			v.add(p+".unit", "unit must be one of %s", strings.Join(ExperimentUnits, ", "))
		}
		layer := x.Layer
		if layer == "" {
			layer = "default"
		} else {
			v.ident(p+".layer", layer)
		}
		if x.Traffic <= 0 || x.Traffic > 1 {
			v.add(p+".traffic", "traffic is a share of eligible units, in (0, 1]")
		}
		if x.Status == "running" || x.Status == "paused" {
			traffic[layer] += x.Traffic
		}
		if x.Audience != nil {
			v.exprRefs(p+".audience.when", x.Audience.When, nil, refUser|refContext)
		}
		if x.Method != "" && !slices.Contains(ExperimentMethods, x.Method) {
			v.add(p+".method", "method must be one of %s", strings.Join(ExperimentMethods, ", "))
		}
		if _, ok := x.Variants["control"]; !ok {
			v.add(p+".variants", "a variant named control is required: the baseline")
		}
		if len(x.Variants) < 2 {
			v.add(p+".variants", "an experiment needs control and at least one other variant")
		}
		sum := 0.0
		for _, vn := range sortedKeys(x.Variants) {
			vr := x.Variants[vn]
			vp := p + ".variants." + vn
			v.ident(vp, vn)
			if vr.Share <= 0 || vr.Share > 1 {
				v.add(vp+".share", "share must be in (0, 1]")
			}
			sum += vr.Share
			if vn == "control" && len(vr.Set) > 0 {
				v.add(vp+".set", "control is the schema as it is; it changes nothing")
			}
			v.variant(vp+".set", vr.Set, x.ShadowPrecompute)
		}
		if len(x.Variants) > 0 && math.Abs(sum-1) > 1e-9 {
			v.add(p+".variants", "variant shares must sum to 1, they sum to %v", sum)
		}
		if _, ok := v.s.Metrics[x.Goal.Metric]; !ok {
			v.add(p+".goal.metric", "%q is not a declared metric", x.Goal.Metric)
		}
		if x.Goal.Direction != "up" && x.Goal.Direction != "down" {
			v.add(p+".goal.direction", "direction must be up or down")
		}
		if x.Goal.MinEffect < 0 {
			v.add(p+".goal.min_effect", "min_effect must not be negative")
		}
		for i, g := range x.Guardrails {
			gp := fmt.Sprintf("%s.guardrails[%d]", p, i)
			if _, ok := v.s.Metrics[g.Metric]; !ok {
				v.add(gp+".metric", "%q is not a declared metric", g.Metric)
			}
			if (g.MaxIncrease == nil) == (g.Max == nil) {
				v.add(gp, "a guardrail needs exactly one of max_increase or max")
			}
		}
		if s := x.Stop; s != nil {
			if s.Confidence != 0 && (s.Confidence <= 0.5 || s.Confidence >= 1) {
				v.add(p+".stop.confidence", "confidence must be in (0.5, 1)")
			}
			if s.MinUnits < 0 {
				v.add(p+".stop.min_units", "min_units must not be negative")
			}
		}
		if g := x.Gate; g != nil && g.Offline != nil {
			if !slices.Contains(OfflineMetrics, g.Offline.Metric) {
				v.add(p+".gate.offline.metric", "metric must be one of %s", strings.Join(OfflineMetrics, ", "))
			}
			if g.Offline.MaxDrop < 0 {
				v.add(p+".gate.offline.max_drop", "max_drop must not be negative")
			}
		}
		if x.OnFinish != "" && !slices.Contains(ExperimentFinishes, x.OnFinish) {
			v.add(p+".on_finish", "on_finish must be one of %s", strings.Join(ExperimentFinishes, ", "))
		}
	}
	for _, layer := range sortedKeys(traffic) {
		if traffic[layer] > 1+1e-9 {
			v.add("experiments", "running and paused experiments in layer %s take %v of the traffic; at most 1", layer, traffic[layer])
		}
	}
}

// variant checks a variant's patch: every path's tier, then the patched schema as a whole.
func (v *validator) variant(p string, set map[string]any, shadow bool) {
	if len(set) == 0 {
		return
	}
	for _, path := range sortedKeys(set) {
		tier := v.s.TierOf(path)
		switch {
		case tier == TierUnknown:
			v.add(p, "%s: not a schema path", path)
		case tier == TierNever:
			v.add(p, "%s: a variant cannot change this section", path)
		case tier == TierT0:
			v.add(p, "%s is T0 (stored data); a variant cannot change it", path)
		case tier == TierT1 && !shadow:
			v.add(p, "%s is T1: it changes what precompute builds; set shadow_precompute: true to test it", path)
		case strings.HasPrefix(path, "constraints.") && set[path] == nil:
			v.add(p, "%s: a variant can add or change a constraint, never remove one", path)
		}
	}
	v.pending = append(v.pending, pendingVariant{p, set})
}

type pendingVariant struct {
	path string
	set  map[string]any
}

// checkVariants validates each variant as a whole schema, reporting only the problems the
// variant introduces: the base schema's own problems are already reported once.
func (v *validator) checkVariants() {
	base := map[Issue]bool{}
	for _, is := range v.issues {
		base[is] = true
	}
	for _, pv := range v.pending {
		patched, err := ApplyPatch(v.s, pv.set)
		if err != nil {
			v.add(pv.path, "%v", err)
			continue
		}
		for _, is := range Validate(patched) {
			if !base[is] {
				v.add(pv.path, "after applying the variant, %s: %s", is.Path, is.Message)
			}
		}
	}
}

func (v *validator) holdout() {
	h := v.s.Holdout
	if h == nil {
		return
	}
	if h.Share <= 0 || h.Share > 0.5 {
		v.add("holdout.share", "share must be in (0, 0.5]")
	}
	v.seedNone("holdout.recommender", h.Recommender)
}

// seedNone checks that name is a non-personalised recommender.
func (v *validator) seedNone(p, name string) {
	r, ok := v.s.Recommenders[name]
	switch {
	case !ok:
		v.add(p, "%q is not a declared recommender", name)
	case r.EffectiveSeed() != SeedNone:
		v.add(p, "%s must not be personalised (seed: none)", name)
	}
}

func (v *validator) evaluation() {
	e := v.s.Evaluation
	if e == nil {
		return
	}
	if e.Split != nil && e.Split.By != "time" && e.Split.By != "random" {
		v.add("evaluation.split.by", "by must be time or random")
	}
	for i, k := range e.K {
		if k < 1 {
			v.add(fmt.Sprintf("evaluation.k[%d]", i), "k must be 1 or more")
		}
	}
	for i, m := range e.Metrics {
		if !slices.Contains(OfflineMetrics, m) {
			v.add(fmt.Sprintf("evaluation.metrics[%d]", i), "metric must be one of %s", strings.Join(OfflineMetrics, ", "))
		}
	}
	for _, kid := range sortedKeys(e.Grid) {
		k, ok := v.s.Knob(kid)
		if !ok {
			v.add("evaluation.grid."+kid, "%q is not a declared knob", kid)
			continue
		}
		r := k.EffectiveRange()
		for _, x := range e.Grid[kid] {
			if x < r[0] || x > r[1] {
				v.add("evaluation.grid."+kid, "%v is outside the knob range [%v, %v]", x, r[0], r[1])
			}
		}
	}
}

func (v *validator) userAttr(p, name string) {
	if _, ok := v.s.Entities["user"].Attributes[name]; !ok {
		v.add(p, "%q is not an attribute of user", name)
	}
}

func (v *validator) privacy() {
	pr := v.s.Privacy
	if pr == nil {
		return
	}
	if pr.Retention != nil && pr.Retention.Default <= 0 {
		v.add("privacy.retention.default", "use a positive duration such as 365d")
	}
	if o := pr.OptOut; o != nil {
		v.userAttr("privacy.opt_out.attribute", o.Attribute)
		v.seedNone("privacy.opt_out.use", o.Use)
	}
	if c := pr.CrossTypeConsent; c != nil {
		v.userAttr("privacy.cross_type_consent.attribute", c.Attribute)
		for _, x := range c.RequiredFor {
			if x != "from" {
				v.add("privacy.cross_type_consent.required_for", "required_for lists from (cross-type history)")
			}
		}
	}
}

func (v *validator) feedback() {
	f := v.s.Feedback
	if f == nil {
		return
	}
	if len(f.Reasons) == 0 {
		v.add("feedback.reasons", "declare at least one reason")
	}
	for _, id := range sortedKeys(f.Reasons) {
		r := f.Reasons[id]
		p := "feedback.reasons." + id
		v.ident(p, id)
		v.text(p+".label", r.Label, true)
		a := r.AppliesTo
		forms := 0
		for _, set := range []bool{a.Item, a.Same != "", a.Similar != 0} {
			if set {
				forms++
			}
		}
		if forms != 1 {
			v.add(p+".applies_to", "applies_to is item, { same: <attribute> } or { similar: <0..1> }")
		}
		if a.Same != "" {
			found := false
			for _, e := range v.scoredBy {
				if _, ok := v.s.Entities[e].Attributes[a.Same]; ok {
					found = true
				}
			}
			if !found {
				v.add(p+".applies_to.same", "%q is not an attribute of any recommended entity", a.Same)
			}
		}
		if a.Similar != 0 && (a.Similar <= 0 || a.Similar > 1) {
			v.add(p+".applies_to.similar", "similar is a similarity threshold in (0, 1]")
		}
		for _, term := range a.Terms {
			found := false
			for _, e := range v.scoredBy {
				if v.hasTerm(e, term) {
					found = true
				}
			}
			if !found {
				v.add(p+".applies_to.terms", "%q is not a similarity term", term)
			}
		}
		e := r.Effect
		effects := 0
		for _, set := range []bool{e.Name != "", e.Penalty != nil, e.Taste != nil} {
			if set {
				effects++
			}
		}
		switch {
		case effects != 1:
			v.add(p+".effect", "effect is hide, satiate, { penalty: <0..1> } or { taste: <negative> }")
		case e.Name != "" && !slices.Contains(FeedbackEffects, e.Name):
			v.add(p+".effect", "effect is hide, satiate, { penalty: <0..1> } or { taste: <negative> }")
		case e.Penalty != nil && (*e.Penalty <= 0 || *e.Penalty > 1):
			v.add(p+".effect.penalty", "penalty is in (0, 1]: the share of the score taken away")
		case e.Taste != nil && *e.Taste >= 0:
			v.add(p+".effect.taste", "taste must be negative: it records a dislike")
		}
		switch r.Until {
		case "", "forever":
		case "recurrence":
			if v.s.Recurrence == nil {
				v.add(p+".until", "until: recurrence needs the recurrence section")
			}
		default:
			if d, err := ParseDuration(r.Until); err != nil || d <= 0 {
				v.add(p+".until", "until is a duration, forever or recurrence")
			}
		}
		if r.Also != "" && r.Also != "report" {
			v.add(p+".also", "also must be report")
		}
	}
}

func (v *validator) recurrence() {
	r := v.s.Recurrence
	if r == nil {
		return
	}
	if len(r.Of) == 0 {
		v.add("recurrence.of", "of is required: the interactions that fulfil a need")
	}
	v.interactionNames("recurrence.of", r.Of, func(_ string, spec InteractionSpec) string {
		if spec.Fulfils == nil {
			return "add fulfils: true to the interaction"
		}
		return ""
	})
	found := false
	for _, e := range v.scoredBy {
		if _, ok := v.s.Entities[e].Attributes[r.GroupBy]; ok {
			found = true
		}
	}
	if !found {
		v.add("recurrence.group_by", "%q is not an attribute of any recommended entity", r.GroupBy)
	}
	if l := r.Learn; l != nil {
		if l.Window <= 0 {
			v.add("recurrence.learn.window", "use a positive duration such as 2y")
		}
		if l.MinUsers < 0 {
			v.add("recurrence.learn.min_users", "min_users must not be negative")
		}
	}
	if r.NeverBelow < 0 || r.NeverBelow >= 1 {
		v.add("recurrence.never_below", "never_below is a repeat rate in [0, 1)")
	}
	if r.From != "" {
		et, an, _ := strings.Cut(r.From, ".")
		e, ok := v.s.Entities[et]
		a, has := e.Attributes[an]
		if !ok || !has || a.Type != TypeInt && a.Type != TypeFloat {
			v.add("recurrence.from", "from is <entity>.<numeric attribute>, a number of days the platform supplies")
		}
	}
}
